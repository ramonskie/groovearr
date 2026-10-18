package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/user"
)

const (
	// TTL is the idle window of a session. Any authenticated request whose
	// session has less than RenewAfter remaining pushes the expiry back out to
	// now+TTL, so an active browser is never logged out; 30 idle days is the
	// cutoff.
	TTL = 30 * 24 * time.Hour

	// RenewAfter is the remaining lifetime at which a session is extended —
	// half the window, matching the sliding-expiration behavior of the *arr
	// applications (ASP.NET Core renews once past the halfway point). Renewing
	// this rarely keeps the cost at most one write per session per 15 days
	// instead of one per request.
	RenewAfter = TTL / 2

	// tokenBytes is the entropy of a session token: 256 bits.
	tokenBytes = 32

	// reapInterval is how often expired rows are pruned. Validate also drops
	// an expired row it touches, so this only reclaims abandoned sessions.
	reapInterval = time.Hour

	// storeTimeout bounds the reaper's store calls, which have no request
	// deadline to inherit.
	storeTimeout = 5 * time.Second
)

// Service owns session policy: token generation, hashing, expiry, sliding
// renewal, and reaping. Persistence is delegated to its Store, so the policy is
// testable without a database and the HTTP layer never sees a raw token stored.
type Service struct {
	store Store
	log   *slog.Logger
	now   func() time.Time // overridable in tests
	done  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
}

// NewService returns a Service persisting sessions through store and starts its
// reaper. The reaper runs for the process lifetime; call Shutdown on server
// shutdown (safe to call more than once).
func NewService(store Store, log *slog.Logger) *Service {
	s := &Service{
		store: store,
		log:   log,
		now:   time.Now,
		done:  make(chan struct{}),
	}
	s.wg.Add(1)
	go s.reapLoop()
	return s
}

// HashToken returns the SHA-256 hex digest stored for a cookie value. Exported
// so the SQLite store's tests can assert that no raw token reaches the table.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create issues a new session for u and returns the raw token (for the cookie)
// and its expiry. A token generation or persistence failure returns an error and
// no session; the caller must not issue a cookie in that case.
func (s *Service) Create(ctx context.Context, u user.User) (string, time.Time, error) {
	token, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now().Truncate(time.Second)
	ses := Session{
		TokenHash: HashToken(token),
		UserID:    u.ID,
		Username:  u.Username,
		Role:      u.Role,
		CreatedAt: now,
		ExpiresAt: now.Add(TTL),
	}
	if err := s.store.Create(ctx, ses); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return token, ses.ExpiresAt, nil
}

// Validate resolves a cookie value to its session.
//
// An unknown token, an empty token, and an expired row all resolve to
// (zero, false, nil): they are ordinary "not authenticated" outcomes. An expired
// row is deleted on the spot. A store failure returns the error so the caller can
// fail closed and log it — a database outage must never authenticate anyone.
func (s *Service) Validate(ctx context.Context, token string) (Session, bool, error) {
	if token == "" {
		return Session{}, false, nil
	}
	ses, ok, err := s.store.Get(ctx, HashToken(token))
	if err != nil {
		return Session{}, false, fmt.Errorf("lookup session: %w", err)
	}
	if !ok {
		return Session{}, false, nil
	}
	if !s.now().Before(ses.ExpiresAt) {
		if err := s.store.Delete(ctx, ses.TokenHash); err != nil && s.log != nil {
			s.log.Warn("session: drop expired row failed", "error", err, "component", "session")
		}
		return Session{}, false, nil
	}
	return ses, true, nil
}

// NeedsRenewal reports whether ses is far enough through its window that an
// activity-driven renewal should extend it. It is pure: the caller decides
// whether to call Renew.
func (s *Service) NeedsRenewal(ses Session) bool {
	return ses.ExpiresAt.Sub(s.now()) < RenewAfter
}

// Renew pushes a live session's expiry out to now+TTL. The token is unchanged —
// rotating it would race parallel tabs and in-flight requests — so the caller
// only re-sends the cookie with the new expiry, and only when renewed is true
// (false means the row vanished mid-request and the cookie must not be re-sent).
func (s *Service) Renew(ctx context.Context, token string) (time.Time, bool, error) {
	expires := s.now().Truncate(time.Second).Add(TTL)
	renewed, err := s.store.Touch(ctx, HashToken(token), expires)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("renew session: %w", err)
	}
	return expires, renewed, nil
}

// Delete removes one session (logout). A missing row is not an error.
func (s *Service) Delete(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.store.Delete(ctx, HashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteByUserID retires every session of a user after a security-relevant
// account change. Callers log and ignore the error at the HTTP boundary.
func (s *Service) DeleteByUserID(ctx context.Context, userID int64) error {
	if err := s.store.DeleteByUserID(ctx, userID); err != nil {
		return fmt.Errorf("delete sessions for user %d: %w", userID, err)
	}
	return nil
}

// Prune removes expired rows. It is called by the reaper and at startup.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	n, err := s.store.DeleteExpired(ctx, s.now())
	if err != nil {
		return 0, fmt.Errorf("prune sessions: %w", err)
	}
	return n, nil
}

// Shutdown stops the reaper and waits for it to exit, so the backing database is
// not closed underneath an in-flight prune. Safe to call multiple times.
func (s *Service) Shutdown() {
	s.once.Do(func() {
		close(s.done)
		s.wg.Wait()
	})
}

// reapLoop prunes expired rows once at startup and then hourly until Shutdown.
func (s *Service) reapLoop() {
	defer s.wg.Done()
	s.reapOnce()
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.reapOnce()
		}
	}
}

// reapOnce runs one prune with its own deadline, since no request context is
// available here.
func (s *Service) reapOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if _, err := s.Prune(ctx); err != nil && s.log != nil {
		s.log.Warn("session: prune failed", "error", err, "component", "session")
	}
}

// newToken returns a 256-bit random hex token. It returns an error instead of
// panicking: an entropy failure is a request-scoped 500, not a fatal startup
// condition.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
