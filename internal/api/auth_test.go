package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/user"
)

const testMeAPIKey = "super-secret-api-key"

// newMeTestServer builds a Server backed by an isolated session store and a
// temp config, optionally mutating the auth config before the request.
func newMeTestServer(t *testing.T, apply func(*config.Config) error) (*Server, *sessionStore) {
	t.Helper()
	cfg := testPersistence(t)
	if apply != nil {
		if err := cfg.Update(apply); err != nil {
			t.Fatalf("config update: %v", err)
		}
	}
	sessions := newSessionStore()
	t.Cleanup(sessions.Shutdown)
	return &Server{cfg: cfg, log: testAPILogger(), sessions: sessions}, sessions
}

// captureIdentity is a downstream handler that records the Identity withAuth
// injected, so tests can assert exactly what the rest of the chain would see.
type captureIdentity struct {
	id   Identity
	seen bool
}

func (c *captureIdentity) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.id, c.seen = identityFrom(r.Context())
	w.WriteHeader(http.StatusOK)
}

// TestWithAuthInjectsIdentity is the canonical table test for identity
// resolution: every transport (none / session / API key header, query, bearer /
// local bypass) and both roles, asserting the Identity a downstream handler
// observes. The unauthenticated cases assert 401 with no injected identity.
func TestWithAuthInjectsIdentity(t *testing.T) {
	tests := []struct {
		name       string
		apply      func(*config.Config) error
		prepare    func(t *testing.T, sessions *sessionStore) *http.Request
		wantStatus int
		wantSeen   bool
		wantID     Identity
	}{
		{
			name: "method none injects admin",
			apply: func(c *config.Config) error {
				c.Auth.Method = "none"
				return nil
			},
			prepare: func(_ *testing.T, _ *sessionStore) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/me", nil)
			},
			wantStatus: http.StatusOK,
			wantSeen:   true,
			wantID:     Identity{Role: user.RoleAdmin},
		},
		{
			name: "valid admin session injects session identity",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				return nil
			},
			prepare: func(t *testing.T, sessions *sessionStore) *http.Request {
				token, _, _ := sessions.Create(user.User{ID: 7, Username: "alice", Role: user.RoleAdmin})
				req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
				req.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: token})
				return req
			},
			wantStatus: http.StatusOK,
			wantSeen:   true,
			wantID:     Identity{UserID: 7, Username: "alice", Role: user.RoleAdmin},
		},
		{
			name: "valid regular-user session injects role user",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				return nil
			},
			prepare: func(t *testing.T, sessions *sessionStore) *http.Request {
				token, _, _ := sessions.Create(user.User{ID: 9, Username: "bob", Role: user.RoleUser})
				req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
				req.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: token})
				return req
			},
			wantStatus: http.StatusOK,
			wantSeen:   true,
			wantID:     Identity{UserID: 9, Username: "bob", Role: user.RoleUser},
		},
		{
			name: "api key header injects admin via_api_key",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				c.Auth.APIKey = testMeAPIKey
				return nil
			},
			prepare: func(_ *testing.T, _ *sessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
				req.Header.Set("X-Api-Key", testMeAPIKey)
				return req
			},
			wantStatus: http.StatusOK,
			wantSeen:   true,
			wantID:     Identity{Role: user.RoleAdmin, ViaAPIKey: true},
		},
		{
			name: "api key query injects admin via_api_key",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				c.Auth.APIKey = testMeAPIKey
				return nil
			},
			prepare: func(_ *testing.T, _ *sessionStore) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/me?apikey="+testMeAPIKey, nil)
			},
			wantStatus: http.StatusOK,
			wantSeen:   true,
			wantID:     Identity{Role: user.RoleAdmin, ViaAPIKey: true},
		},
		{
			name: "api key bearer injects admin via_api_key",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				c.Auth.APIKey = testMeAPIKey
				return nil
			},
			prepare: func(_ *testing.T, _ *sessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
				req.Header.Set("Authorization", "Bearer "+testMeAPIKey)
				return req
			},
			wantStatus: http.StatusOK,
			wantSeen:   true,
			wantID:     Identity{Role: user.RoleAdmin, ViaAPIKey: true},
		},
		{
			name: "local bypass injects regular user, never admin (C4)",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				c.Auth.LocalBypassSubnets = []string{"10.0.0.0/8"}
				return nil
			},
			prepare: func(_ *testing.T, _ *sessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
				req.RemoteAddr = "10.0.0.5:12345"
				return req
			},
			wantStatus: http.StatusOK,
			wantSeen:   true,
			wantID:     Identity{Role: user.RoleUser},
		},
		{
			name: "api key beats local bypass for a credential-bearing host",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				c.Auth.APIKey = testMeAPIKey
				c.Auth.LocalBypassSubnets = []string{"10.0.0.0/8"}
				return nil
			},
			prepare: func(_ *testing.T, _ *sessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
				req.RemoteAddr = "10.0.0.5:12345"
				req.Header.Set("X-Api-Key", testMeAPIKey)
				return req
			},
			wantStatus: http.StatusOK,
			wantSeen:   true,
			wantID:     Identity{Role: user.RoleAdmin, ViaAPIKey: true},
		},
		{
			name: "unauthenticated is 401 with no identity",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				c.Auth.APIKey = testMeAPIKey
				return nil
			},
			prepare: func(_ *testing.T, _ *sessionStore) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/me", nil)
			},
			wantStatus: http.StatusUnauthorized,
			wantSeen:   false,
		},
		{
			name: "invalid session is 401 with no identity",
			apply: func(c *config.Config) error {
				c.Auth.Method = "forms"
				return nil
			},
			prepare: func(_ *testing.T, _ *sessionStore) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
				req.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: "not-a-real-token"})
				return req
			},
			wantStatus: http.StatusUnauthorized,
			wantSeen:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			srv, sessions := newMeTestServer(t, tt.apply)
			req := tt.prepare(t, sessions)
			capture := &captureIdentity{}

			// Act
			rec := httptest.NewRecorder()
			srv.withAuth(capture).ServeHTTP(rec, req)

			// Assert
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d. body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if capture.seen != tt.wantSeen {
				t.Fatalf("downstream identity seen = %v, want %v", capture.seen, tt.wantSeen)
			}
			if tt.wantSeen && capture.id != tt.wantID {
				t.Errorf("downstream identity = %+v, want %+v", capture.id, tt.wantID)
			}
		})
	}
}

// meThroughAuth routes a request through the production chain
// (withAuth -> handleMe) so the injected Identity reaches the handler.
func meThroughAuth(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.withAuth(http.HandlerFunc(srv.handleMe)).ServeHTTP(rec, req)
	return rec
}

// TestHandleMe verifies the /api/me wire contract: it reports the identity
// withAuth resolved, and never echoes the API key.
func TestHandleMe(t *testing.T) {
	t.Run("auth method none reports admin", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "none"
			return nil
		})
		rec := meThroughAuth(srv, httptest.NewRequest(http.MethodGet, "/api/me", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		got := decodeMe(t, rec)
		if got.Role != "admin" {
			t.Errorf("role = %q, want admin", got.Role)
		}
		if got.Username != "" {
			t.Errorf("username = %q, want empty", got.Username)
		}
		if got.ViaAPIKey {
			t.Error("via_api_key = true, want false")
		}
	})

	t.Run("valid admin session returns username and role", func(t *testing.T) {
		srv, sessions := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			return nil
		})
		token, _, _ := sessions.Create(user.User{ID: 1, Username: "alice", Role: user.RoleAdmin})

		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: token})
		rec := meThroughAuth(srv, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d. body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		got := decodeMe(t, rec)
		if got.Username != "alice" {
			t.Errorf("username = %q, want alice", got.Username)
		}
		if got.Role != "admin" {
			t.Errorf("role = %q, want admin", got.Role)
		}
		if got.ViaAPIKey {
			t.Error("via_api_key = true, want false")
		}
	})

	t.Run("valid regular-user session returns role user", func(t *testing.T) {
		srv, sessions := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			return nil
		})
		token, _, _ := sessions.Create(user.User{ID: 2, Username: "bob", Role: user.RoleUser})

		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: token})
		rec := meThroughAuth(srv, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d. body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		got := decodeMe(t, rec)
		if got.Username != "bob" {
			t.Errorf("username = %q, want bob", got.Username)
		}
		if got.Role != "user" {
			t.Errorf("role = %q, want user", got.Role)
		}
	})

	t.Run("api key header reports via_api_key and never echoes the key", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			c.Auth.APIKey = testMeAPIKey
			return nil
		})
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("X-Api-Key", testMeAPIKey)
		rec := meThroughAuth(srv, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d. body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		got := decodeMe(t, rec)
		if !got.ViaAPIKey {
			t.Error("via_api_key = false, want true")
		}
		if got.Role != "admin" {
			t.Errorf("role = %q, want admin", got.Role)
		}
		if strings.Contains(rec.Body.String(), testMeAPIKey) {
			t.Fatalf("response echoed the API key: %s", rec.Body.String())
		}
	})

	t.Run("api key via query param reports via_api_key", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			c.Auth.APIKey = testMeAPIKey
			return nil
		})
		rec := meThroughAuth(srv, httptest.NewRequest(http.MethodGet, "/api/me?apikey="+testMeAPIKey, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		got := decodeMe(t, rec)
		if !got.ViaAPIKey {
			t.Error("via_api_key = false, want true")
		}
		if strings.Contains(rec.Body.String(), testMeAPIKey) {
			t.Fatalf("response echoed the API key: %s", rec.Body.String())
		}
	})

	t.Run("api key via bearer reports via_api_key", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			c.Auth.APIKey = testMeAPIKey
			return nil
		})
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+testMeAPIKey)
		rec := meThroughAuth(srv, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		got := decodeMe(t, rec)
		if !got.ViaAPIKey {
			t.Error("via_api_key = false, want true")
		}
		if strings.Contains(rec.Body.String(), testMeAPIKey) {
			t.Fatalf("response echoed the API key: %s", rec.Body.String())
		}
	})

	t.Run("local bypass reports regular user, not admin (C4)", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			c.Auth.LocalBypassSubnets = []string{"10.0.0.0/8"}
			return nil
		})
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.RemoteAddr = "10.0.0.5:12345"
		rec := meThroughAuth(srv, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d. body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		got := decodeMe(t, rec)
		if got.Role != "user" {
			t.Errorf("role = %q, want user", got.Role)
		}
		if got.ViaAPIKey {
			t.Error("via_api_key = true, want false")
		}
	})

	t.Run("unauthenticated is 401 when auth enabled", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			c.Auth.APIKey = testMeAPIKey
			return nil
		})
		rec := meThroughAuth(srv, httptest.NewRequest(http.MethodGet, "/api/me", nil))

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d. body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("invalid session is 401", func(t *testing.T) {
		srv, _ := newMeTestServer(t, func(c *config.Config) error {
			c.Auth.Method = "forms"
			return nil
		})
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: "not-a-real-token"})
		rec := meThroughAuth(srv, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
}

// decodeMe unmarshals a recorded /api/me response into the wire struct.
func decodeMe(t *testing.T, rec *httptest.ResponseRecorder) meResponse {
	t.Helper()
	var got meResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	return got
}

// fakeUserStore is an in-memory user.Store for login tests. GetUserByUsername
// resolves case-insensitively (mirroring the SQLite COLLATE NOCASE contract)
// and can be forced to fail so the error path is covered without a database.
type fakeUserStore struct {
	users  []user.User
	getErr error
}

var _ user.Store = (*fakeUserStore)(nil)

func (f *fakeUserStore) CreateUser(context.Context, *user.User) (int64, error) { return 0, nil }
func (f *fakeUserStore) GetUser(context.Context, int64) (*user.User, error)    { return nil, nil }

func (f *fakeUserStore) GetUserByUsername(_ context.Context, username string) (*user.User, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for i := range f.users {
		if strings.EqualFold(f.users[i].Username, username) {
			u := f.users[i]
			return &u, nil
		}
	}
	return nil, nil
}

func (f *fakeUserStore) ListUsers(context.Context) ([]user.User, error) { return f.users, nil }
func (f *fakeUserStore) UpdateUser(context.Context, *user.User) error   { return nil }
func (f *fakeUserStore) DeleteUser(context.Context, int64) error        { return nil }
func (f *fakeUserStore) UpdateUserGuarded(context.Context, int64, user.UserPatch) (*user.User, error) {
	return nil, nil
}
func (f *fakeUserStore) DeleteUserGuarded(context.Context, int64) error {
	return nil
}
func (f *fakeUserStore) CountUsers(context.Context) (int, error) { return len(f.users), nil }

// newLoginTestServer builds a Server whose handleLogin authenticates against
// the supplied user store, with an isolated session store and temp config.
// apply may override the default "forms" auth method (e.g. to test the method
// guard).
func newLoginTestServer(t *testing.T, store user.Store, apply func(*config.Config)) (*Server, *sessionStore) {
	t.Helper()
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.Auth.Method = "forms"
		if apply != nil {
			apply(c)
		}
		return nil
	}); err != nil {
		t.Fatalf("config update: %v", err)
	}
	sessions := newSessionStore()
	t.Cleanup(sessions.Shutdown)
	return &Server{cfg: cfg, log: testAPILogger(), sessions: sessions, userStore: store}, sessions
}

// findSessionCookie returns the groovearr_sid cookie, or nil when absent.
func findSessionCookie(cookies []*http.Cookie) *http.Cookie {
	for _, c := range cookies {
		if c.Name == "groovearr_sid" {
			return c
		}
	}
	return nil
}

// TestHandleLogin is the canonical table test for the login handler: admin and
// regular-user success (asserting the role round-trips into the session), each
// rejection path (wrong password, disabled, unknown), store failure, a nil
// store (fail closed), and the request-shape guards. Rejections must be
// indistinguishable and must never issue a session cookie.
func TestHandleLogin(t *testing.T) {
	adminHash, err := config.HashPassword("admin-pass")
	if err != nil {
		t.Fatalf("hash admin password: %v", err)
	}
	userHash, err := config.HashPassword("user-pass")
	if err != nil {
		t.Fatalf("hash user password: %v", err)
	}

	store := &fakeUserStore{users: []user.User{
		{ID: 1, Username: "admin", PasswordHash: adminHash, Role: user.RoleAdmin},
		{ID: 2, Username: "bob", PasswordHash: userHash, Role: user.RoleUser},
		{ID: 3, Username: "carol", PasswordHash: userHash, Role: user.RoleUser, Disabled: true},
	}}
	storeErr := &fakeUserStore{getErr: errors.New("db down")}

	tests := []struct {
		name       string
		store      user.Store
		apply      func(*config.Config)
		method     string
		body       string
		wantStatus int
		wantRole   user.Role
		wantUser   string
	}{
		{
			name:       "admin login succeeds and session keeps admin role",
			store:      store,
			method:     http.MethodPost,
			body:       `{"username":"admin","password":"admin-pass"}`,
			wantStatus: http.StatusOK,
			wantRole:   user.RoleAdmin,
			wantUser:   "admin",
		},
		{
			name:       "regular user login succeeds and session keeps user role",
			store:      store,
			method:     http.MethodPost,
			body:       `{"username":"bob","password":"user-pass"}`,
			wantStatus: http.StatusOK,
			wantRole:   user.RoleUser,
			wantUser:   "bob",
		},
		{
			name:       "username is trimmed and matched case-insensitively",
			store:      store,
			method:     http.MethodPost,
			body:       `{"username":"  ADMIN  ","password":"admin-pass"}`,
			wantStatus: http.StatusOK,
			wantRole:   user.RoleAdmin,
			wantUser:   "admin",
		},
		{
			name:       "wrong password is 401",
			store:      store,
			method:     http.MethodPost,
			body:       `{"username":"admin","password":"wrong-pass"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "disabled account is 401",
			store:      store,
			method:     http.MethodPost,
			body:       `{"username":"carol","password":"user-pass"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "unknown user is 401",
			store:      store,
			method:     http.MethodPost,
			body:       `{"username":"ghost","password":"whatever"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "store error is 500 with no session",
			store:      storeErr,
			method:     http.MethodPost,
			body:       `{"username":"admin","password":"admin-pass"}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "nil store fails closed with 500",
			store:      nil,
			method:     http.MethodPost,
			body:       `{"username":"admin","password":"admin-pass"}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "missing password is 400",
			store:      store,
			method:     http.MethodPost,
			body:       `{"username":"admin","password":""}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing username is 400",
			store:      store,
			method:     http.MethodPost,
			body:       `{"username":"","password":"admin-pass"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "non-forms auth method is rejected",
			store:      store,
			apply:      func(c *config.Config) { c.Auth.Method = "none" },
			method:     http.MethodPost,
			body:       `{"username":"admin","password":"admin-pass"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "non-POST method is 405",
			store:      store,
			method:     http.MethodGet,
			body:       ``,
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			srv, sessions := newLoginTestServer(t, tt.store, tt.apply)
			req := httptest.NewRequest(tt.method, "/api/login", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			// Act
			srv.handleLogin(rec, req)

			// Assert
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d. body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "admin-pass") || strings.Contains(rec.Body.String(), "user-pass") {
				t.Fatalf("response leaked a password: %s", rec.Body.String())
			}

			cookie := findSessionCookie(rec.Result().Cookies())
			if tt.wantStatus != http.StatusOK {
				if cookie != nil {
					t.Fatalf("unexpected session cookie on %d response", tt.wantStatus)
				}
				return
			}

			if cookie == nil {
				t.Fatal("successful login did not set the groovearr_sid cookie")
			}
			if !cookie.HttpOnly || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
				t.Errorf("cookie attrs = %+v, want HttpOnly, Path=/, SameSite=Lax", cookie)
			}
			ses, ok := sessions.Validate(cookie.Value)
			if !ok {
				t.Fatal("session token from the login cookie did not validate")
			}
			if ses.Role != tt.wantRole {
				t.Errorf("session role = %q, want %q", ses.Role, tt.wantRole)
			}
			if ses.Username != tt.wantUser {
				t.Errorf("session username = %q, want %q", ses.Username, tt.wantUser)
			}
		})
	}
}
