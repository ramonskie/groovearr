package api

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Incoming rate limits — requests per minute per client IP.
// Override via env vars: RATE_SEARCH, RATE_DOWNLOAD, RATE_SCAN.
const (
	defaultSearchRate   = 30 // GET /api/search, GET /api/discover/search
	defaultDownloadRate = 10 // POST /api/download, playlists import/sync
	defaultScanRate     = 2  // POST /api/library/scan
	defaultLoginRate    = 5  // POST /api/login
)

// rateLimitBucket configures a rate limit for a group of endpoints.
type rateLimitBucket struct {
	name   string        // bucket label (search, download, scan)
	max    int           // max requests per window
	window time.Duration // sliding window duration
}

// defaultRateBuckets returns the standard rate-limit buckets.
// Override via env vars: RATE_SEARCH, RATE_DOWNLOAD, RATE_SCAN (req/min).
func defaultRateBuckets() []rateLimitBucket {
	rateSearch := rateEnv("RATE_SEARCH", defaultSearchRate)
	rateDownload := rateEnv("RATE_DOWNLOAD", defaultDownloadRate)
	rateScan := rateEnv("RATE_SCAN", defaultScanRate)
	rateLogin := rateEnv("RATE_LOGIN", defaultLoginRate)
	return []rateLimitBucket{
		{name: "search", max: rateSearch, window: time.Minute},
		{name: "download", max: rateDownload, window: time.Minute},
		{name: "scan", max: rateScan, window: time.Minute},
		{name: "login", max: rateLogin, window: time.Minute},
	}
}

func rateEnv(key string, fallback int) int {
	s := os.Getenv(key)
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// visitor tracks request count for a single IP within a sliding window.
type visitor struct {
	count   int
	resetAt time.Time
}

// ipRateLimiter implements per-IP sliding-window rate limiting.
// A background goroutine periodically evicts expired entries.
type ipRateLimiter struct {
	mu       sync.Mutex
	buckets  []rateLimitBucket
	visitors map[string]map[string]*visitor
	log      *slog.Logger
	done     chan struct{}
	trustXFF bool
}

func newIPRateLimiter(buckets []rateLimitBucket, logger *slog.Logger) *ipRateLimiter {
	if logger == nil {
		logger = slog.Default()
	}
	v := make(map[string]map[string]*visitor, len(buckets))
	for _, b := range buckets {
		v[b.name] = make(map[string]*visitor)
	}
	l := &ipRateLimiter{
		buckets:  buckets,
		visitors: v,
		log:      logger,
		done:     make(chan struct{}),
		trustXFF: os.Getenv("TRUST_X_FORWARDED_FOR") == "true",
	}
	go l.periodicCleanup()
	return l
}

// periodicCleanup runs a background goroutine that evicts expired visitor
// entries every 5 minutes to prevent unbounded memory growth.
func (l *ipRateLimiter) periodicCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-ticker.C:
			l.mu.Lock()
			l.cleanup(time.Now())
			l.mu.Unlock()
		}
	}
}

// Shutdown stops the background cleanup goroutine.
func (l *ipRateLimiter) Shutdown() {
	select {
	case <-l.done:
		return
	default:
		close(l.done)
	}
}

// extractIP returns the client IP from the request.
// When trustXFF is true (behind a trusted reverse proxy), the leftmost
// X-Forwarded-For entry is used. Otherwise RemoteAddr is used directly.
func (l *ipRateLimiter) extractIP(r *http.Request) string {
	if l.trustXFF {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			ip := strings.TrimSpace(first)
			if ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// allow checks whether the client IP is within the rate limit for the given bucket.
func (l *ipRateLimiter) allow(ip, bucketName string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ips, ok := l.visitors[bucketName]
	if !ok {
		l.log.Error("rate limit: unrecognized bucket, denying", "bucket", bucketName, "component", "api")
		return false, 60
	}

	var bucket *rateLimitBucket
	for i := range l.buckets {
		if l.buckets[i].name == bucketName {
			bucket = &l.buckets[i]
			break
		}
	}
	if bucket == nil {
		l.log.Error("rate limit: unrecognized bucket, denying", "bucket", bucketName, "component", "api")
		return false, 60
	}

	now := time.Now()
	v, exists := ips[ip]
	if !exists || now.After(v.resetAt) {
		ips[ip] = &visitor{count: 1, resetAt: now.Add(bucket.window)}
		return true, 0
	}

	v.count++
	if v.count > bucket.max {
		remaining := int(time.Until(v.resetAt).Seconds())
		if remaining < 1 {
			remaining = 1
		}
		return false, remaining
	}

	return true, 0
}

func (l *ipRateLimiter) cleanup(now time.Time) {
	for _, ips := range l.visitors {
		for ip, v := range ips {
			if now.After(v.resetAt) {
				delete(ips, ip)
			}
		}
	}
}

// rateLimitedHandler wraps an http.Handler with rate limiting.
type rateLimitedHandler struct {
	handler    http.Handler
	limiter    *ipRateLimiter
	bucketName string
}

func (h *rateLimitedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := h.limiter.extractIP(r)
	allowed, retryAfter := h.limiter.allow(ip, h.bucketName)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		w.Header().Set("X-RateLimit-Bucket", h.bucketName)
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": fmt.Sprintf("rate limit exceeded, retry after %ds", retryAfter),
		})
		return
	}
	h.handler.ServeHTTP(w, r)
}

// withRateLimit wraps a handler with the given rate-limit bucket.
func withRateLimit(bucket string, limiter *ipRateLimiter, next http.Handler) http.Handler {
	return &rateLimitedHandler{handler: next, limiter: limiter, bucketName: bucket}
}
