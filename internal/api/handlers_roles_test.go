package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/plugin"
	"github.com/ramonskie/groovearr/internal/providers/spotify"
	"github.com/ramonskie/groovearr/internal/providers/tidal"
	"github.com/ramonskie/groovearr/internal/user"
)

// roleRoute is one route in the authorization matrix under test.
type roleRoute struct {
	group  string // settings group the route belongs to (config, jobs, ...)
	method string
	path   string
}

// adminRoutes are the settings-surface routes. They must be reachable by an
// admin and rejected with 403 for a regular user.
var adminRoutes = []roleRoute{
	{"config", http.MethodGet, "/api/config"},
	{"config", http.MethodPut, "/api/config"},
	{"config", http.MethodGet, "/api/config/sources"},
	{"config", http.MethodPost, "/api/config/test/deezer"},
	{"users", http.MethodGet, "/api/users"},
	{"users", http.MethodPost, "/api/users"},
	{"users", http.MethodPatch, "/api/users/1"},
	{"users", http.MethodDelete, "/api/users/1"},
	{"rate-limits", http.MethodGet, "/api/rate-limits"},
	{"rate-limits", http.MethodDelete, "/api/rate-limits/deezer"},
	{"jobs", http.MethodGet, "/api/jobs"},
	{"jobs", http.MethodGet, "/api/jobs/activity"},
	{"jobs", http.MethodPost, "/api/jobs/scan"},
	{"jobs", http.MethodPost, "/api/jobs/enrich"},
	{"jobs", http.MethodPost, "/api/jobs/duplicates"},
	{"jobs", http.MethodPost, "/api/jobs/organize"},
	{"jobs", http.MethodGet, "/api/jobs/organize/report"},
	{"jobs", http.MethodPost, "/api/jobs/cancel"},
	{"tracking", http.MethodGet, "/api/tracking/artists"},
	{"tracking", http.MethodPost, "/api/tracking/artists"},
	{"tracking", http.MethodGet, "/api/tracking/artists/1"},
	{"tracking", http.MethodPatch, "/api/tracking/artists/1"},
	{"tracking", http.MethodDelete, "/api/tracking/artists/1"},
	{"tracking", http.MethodGet, "/api/tracking/artists/1/albums"},
	{"tracking", http.MethodPost, "/api/tracking/artists/1/refresh"},
	{"tracking", http.MethodPost, "/api/tracking/artists/1/search-missing"},
	{"tracking", http.MethodPost, "/api/tracking/refresh"},
	{"tracking", http.MethodGet, "/api/tracking/wanted"},
	{"tracking", http.MethodPatch, "/api/tracking/albums/1"},
	{"quality-profiles", http.MethodGet, "/api/quality-profiles/presets"},
	{"quality-profiles", http.MethodPost, "/api/quality-profiles/apply-preset"},
	{"quality-profiles", http.MethodGet, "/api/quality-profiles"},
	{"quality-profiles", http.MethodPost, "/api/quality-profiles"},
	{"quality-profiles", http.MethodGet, "/api/quality-profiles/1"},
	{"quality-profiles", http.MethodPut, "/api/quality-profiles/1"},
	{"quality-profiles", http.MethodDelete, "/api/quality-profiles/1"},
	{"quality-profiles", http.MethodPut, "/api/quality-profiles/1/default"},
	{"logs", http.MethodGet, "/api/logs"},
	{"debug", http.MethodGet, "/api/debug/download/1"},
}

// sharedRoutes must stay reachable for a regular user (never 403). This is the
// anti-over-wrapping guard: a shared route accidentally gated would fail here.
var sharedRoutes = []roleRoute{
	{"health", http.MethodGet, "/api/health"},
	{"setup", http.MethodGet, "/api/setup/status"},
	{"identity", http.MethodGet, "/api/me"},
	{"auth", http.MethodPost, "/api/login"},
	{"auth", http.MethodPost, "/api/logout"},
	{"downloads", http.MethodGet, "/api/downloads"},
	{"downloads", http.MethodDelete, "/api/downloads/abc"},
	{"downloads", http.MethodPost, "/api/download"},
	{"library", http.MethodGet, "/api/library/tracks"},
	{"library", http.MethodGet, "/api/library/artists"},
	{"library", http.MethodGet, "/api/library/albums"},
	{"covers", http.MethodGet, "/api/covers/1"},
	{"artist-image", http.MethodGet, "/api/artist-image/1"},
	{"playlists", http.MethodGet, "/api/playlists"},
	{"playlists", http.MethodGet, "/api/playlists/1"},
	{"search", http.MethodPost, "/api/search"},
	{"search", http.MethodPost, "/api/albums/search"},
	{"discover", http.MethodGet, "/api/discover/providers"},
	{"events", http.MethodGet, "/api/events"},
}

// TestRouteRoleGating exercises the real route table and asserts the admin
// boundary: the settings surface is admin-only, shared routes are not.
//
// Identity is injected directly with contextWithIdentity (the mux is invoked
// without withAuth), so this test isolates authorization wiring from session
// authentication, which is covered separately.
func TestRouteRoleGating(t *testing.T) {
	limiter := newIPRateLimiter(roleTestBuckets(), testAPILogger())
	t.Cleanup(limiter.Shutdown)

	// Minimal server: user requests to admin routes stop at adminOnly before
	// any handler runs, and admin/shared requests that reach a handler on this
	// dep-less server are caught by mustNotPanic and surfaced as non-403.
	s := &Server{log: testAPILogger(), rateLimiter: limiter}
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)
	handler := mustNotPanic(mux)

	userID := Identity{UserID: 2, Username: "bob", Role: user.RoleUser}
	adminID := Identity{UserID: 1, Username: "alice", Role: user.RoleAdmin}

	t.Run("settings surface is admin-only", func(t *testing.T) {
		for _, rt := range adminRoutes {
			t.Run(rt.group+"/"+rt.method+" "+rt.path, func(t *testing.T) {
				// Regular user is forbidden, with the stable 403 body.
				rec := dispatchRole(handler, rt.method, rt.path, userID)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("user status = %d, want 403 (route may be unwrapped)", rec.Code)
				}
				if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, "forbidden") {
					t.Errorf("user body = %q, want forbidden", body)
				}

				// Admin passes the gate and reaches the handler. On this minimal
				// server the handler may panic (recovered as 500) — the point is
				// that it is never 403.
				if rec := dispatchRole(handler, rt.method, rt.path, adminID); rec.Code == http.StatusForbidden {
					t.Errorf("admin status = 403, want the route to be reachable")
				}
			})
		}
	})

	t.Run("shared surface stays open to users", func(t *testing.T) {
		for _, rt := range sharedRoutes {
			t.Run(rt.group+"/"+rt.method+" "+rt.path, func(t *testing.T) {
				if rec := dispatchRole(handler, rt.method, rt.path, userID); rec.Code == http.StatusForbidden {
					t.Errorf("user status = 403, want the shared route to be reachable")
				}
			})
		}
	})
}

// TestPluginRouteRegistrarEnforcesLevels proves the adapter is the enforcement
// point: a plugin registrar can only reach routes through the declared Admin /
// User level, and the core applies s.adminOnly to Admin routes. A plugin
// therefore cannot expose a config-mutating route to a regular user even though
// it owns its own routes (AGENTS §1/§4).
func TestPluginRouteRegistrarEnforcesLevels(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

	mux := http.NewServeMux()
	s := &Server{log: testAPILogger()}

	// A fake plugin registrar using the same surface app.go's closures use.
	register := PluginRouteRegistrar(func(r plugin.RouteRegistrar) {
		r.Admin(http.MethodGet, "/api/plugin/admin", ok)
		r.User(http.MethodGet, "/api/plugin/user", ok)
	})
	register(pluginRouteAdapter{mux: mux, adminOnly: s.adminOnly})

	userID := Identity{UserID: 2, Username: "bob", Role: user.RoleUser}
	adminID := Identity{UserID: 1, Username: "alice", Role: user.RoleAdmin}

	if rec := dispatchRole(mux, http.MethodGet, "/api/plugin/admin", userID); rec.Code != http.StatusForbidden {
		t.Errorf("user on Admin route = %d, want 403", rec.Code)
	} else if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, "forbidden") {
		t.Errorf("user body = %q, want forbidden", body)
	}
	if rec := dispatchRole(mux, http.MethodGet, "/api/plugin/user", userID); rec.Code != http.StatusOK {
		t.Errorf("user on User route = %d, want 200", rec.Code)
	}
	if rec := dispatchRole(mux, http.MethodGet, "/api/plugin/admin", adminID); rec.Code != http.StatusOK {
		t.Errorf("admin on Admin route = %d, want 200", rec.Code)
	}
	if rec := dispatchRole(mux, http.MethodGet, "/api/plugin/user", adminID); rec.Code != http.StatusOK {
		t.Errorf("admin on User route = %d, want 200", rec.Code)
	}
}

// recordingRegistrar captures how a provider declares its routes, so we can
// assert the real OAuth registrars use Admin (they mutate provider tokens).
type recordingRegistrar struct {
	admin []string
	user  []string
}

func (r *recordingRegistrar) Admin(method, path string, _ http.HandlerFunc) {
	r.admin = append(r.admin, method+" "+path)
}

func (r *recordingRegistrar) User(method, path string, _ http.HandlerFunc) {
	r.user = append(r.user, method+" "+path)
}

// TestProviderOAuthRoutesAreAdmin wires the real spotify and tidal registrars
// into a recording registrar and asserts every OAuth endpoint is declared
// Admin — they overwrite global provider credentials. Handlers are never
// invoked here (nil deps are safe because only closures are built).
func TestProviderOAuthRoutesAreAdmin(t *testing.T) {
	rec := &recordingRegistrar{}
	spotify.RegisterOAuthRoutes(rec, nil, testAPILogger(), nil, nil)
	tidal.RegisterOAuthRoutes(rec, nil, plugin.NewRegistry(), testAPILogger(), nil, nil)

	want := map[string]bool{
		"GET /api/spotify/login":    true,
		"GET /api/spotify/callback": true,
		"GET /api/tidal/login":      true,
		"GET /api/tidal/poll":       true,
	}
	for _, route := range rec.admin {
		if !want[route] {
			t.Errorf("unexpected admin route %q", route)
		}
		delete(want, route)
	}
	for route := range want {
		t.Errorf("expected admin route %q not registered", route)
	}
	if len(rec.user) != 0 {
		t.Errorf("OAuth routes registered as User, want Admin: %v", rec.user)
	}
}

// roleTestBuckets returns generous rate-limit buckets so the authorization
// test never trips a 429 (which would obscure a missing 403).
func roleTestBuckets() []rateLimitBucket {
	names := []string{"search", "download", "scan", "enrich", "duplicates", "login"}
	buckets := make([]rateLimitBucket, 0, len(names))
	for _, name := range names {
		buckets = append(buckets, rateLimitBucket{name: name, max: 10000, window: time.Minute})
	}
	return buckets
}

// dispatchRole runs one request through h with id on the request context.
func dispatchRole(h http.Handler, method, path string, id Identity) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, http.NoBody)
	req = req.WithContext(contextWithIdentity(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// mustNotPanic converts a handler panic into a 500 so a request that reached
// the (dependency-less) handler is observable as "not 403". This isolates the
// authorization gate from handler behavior: the question is whether adminOnly
// blocked the request, not whether the real handler could run here.
func mustNotPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ow := &onceWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil && !ow.wrote {
				ow.ResponseWriter.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(ow, r)
	})
}

// onceWriter records whether a header was written so the panic recovery does
// not emit a superfluous WriteHeader.
type onceWriter struct {
	http.ResponseWriter
	wrote bool
}

func (o *onceWriter) WriteHeader(code int) {
	if o.wrote {
		return
	}
	o.wrote = true
	o.ResponseWriter.WriteHeader(code)
}

func (o *onceWriter) Write(b []byte) (int, error) {
	if !o.wrote {
		o.wrote = true
		o.ResponseWriter.WriteHeader(http.StatusOK)
	}
	return o.ResponseWriter.Write(b)
}
