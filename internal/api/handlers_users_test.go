package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/user"
	usersqlite "github.com/ramonskie/groovearr/internal/user/sqlite"
)

// newUserMgmtServer builds a Server backed by a real in-memory-file SQLite user
// store plus an isolated session store. Using the real store exercises the
// transactional last-admin guards and the SQLite-backed duplicate detection,
// not a fake that would only re-assert the test's own assumptions.
func newUserMgmtServer(t *testing.T) (*Server, user.Store) {
	t.Helper()
	s, store, _ := newUserMgmtServerDB(t)
	return s, store
}

// newUserMgmtServerDB is newUserMgmtServer plus the underlying *sql.DB, so a
// test can pin a column (e.g. updated_at) to prove a handler did not write.
func newUserMgmtServerDB(t *testing.T) (*Server, user.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := usersqlite.New(db)
	if err != nil {
		t.Fatalf("new user store: %v", err)
	}
	sessions := newSessionStore()
	t.Cleanup(sessions.Shutdown)
	return &Server{userStore: store, sessions: sessions, log: testAPILogger()}, store, db
}

// seedUser inserts one account with a real bcrypt hash and returns the stored
// record (with its assigned ID).
func seedUser(t *testing.T, store user.Store, username, password string, role user.Role, disabled bool) user.User {
	t.Helper()
	hash, err := config.HashPassword(password)
	if err != nil {
		t.Fatalf("hash %q: %v", username, err)
	}
	id, err := store.CreateUser(context.Background(), &user.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Disabled:     disabled,
	})
	if err != nil {
		t.Fatalf("seed %q: %v", username, err)
	}
	u, err := store.GetUser(context.Background(), id)
	if err != nil || u == nil {
		t.Fatalf("read back %q = (%+v, %v)", username, u, err)
	}
	return *u
}

// userMux registers the real API route table so tests exercise the adminOnly
// wrapping and path parsing, not just the handler bodies.
func userMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)
	return mux
}

// dispatchUser runs one request through the real mux with id on the context.
func dispatchUser(mux *http.ServeMux, method, path string, id Identity, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, http.NoBody)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r = r.WithContext(contextWithIdentity(r.Context(), id))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// adminActor is an admin identity that is not itself a stored row — adminOnly
// reads the injected identity, so the actor need not exist in the store.
var adminActor = Identity{UserID: 100, Username: "actor", Role: user.RoleAdmin}

// newSessionAuthServer builds the user-management server with "forms" auth
// configured, so requests can be driven through the full withAuth -> route
// chain with a session cookie (newUserMgmtServer omits cfg, which withAuth
// needs).
func newSessionAuthServer(t *testing.T) (*Server, user.Store) {
	t.Helper()
	s, store := newUserMgmtServer(t)
	cfg := testPersistence(t)
	if err := cfg.Update(func(c *config.Config) error {
		c.Auth.Method = "forms"
		return nil
	}); err != nil {
		t.Fatalf("config update: %v", err)
	}
	s.cfg = cfg
	return s, store
}

// dispatchSession runs one request through the real auth chain (withAuth over
// the route table) carrying the groovearr_sid cookie, exactly as an
// authenticated SPA request would.
func dispatchSession(s *Server, mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, http.NoBody)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: token})
	rec := httptest.NewRecorder()
	s.withAuth(mux).ServeHTTP(rec, r)
	return rec
}

// TestListUsersNeverReturnsPasswordHash is the core leak guard: even though
// accounts carry a bcrypt hash, no hash (or hash marker) may reach the wire.
func TestListUsersNeverReturnsPasswordHash(t *testing.T) {
	s, store := newUserMgmtServer(t)
	seedUser(t, store, "root", "root-secret-pass", user.RoleAdmin, false)
	seedUser(t, store, "bob", "bob-secret-pass", user.RoleUser, false)

	rec := dispatchUser(userMux(s), http.MethodGet, "/api/users", adminActor, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, leak := range []string{"password_hash", "$2a$", "$2b$", "$2y$", "root-secret-pass", "bob-secret-pass"} {
		if strings.Contains(body, leak) {
			t.Errorf("GET /api/users leaked %q: %s", leak, body)
		}
	}

	var got []userResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode users: %v; body=%s", err, body)
	}
	if len(got) != 2 {
		t.Fatalf("users = %d, want 2", len(got))
	}
}

// TestCreateUser is the table-driven create contract: happy paths, defaults,
// trimming, duplicate (case-insensitive), and every validation rejection.
func TestCreateUser(t *testing.T) {
	type seed struct {
		username string
		password string
		role     user.Role
	}
	tests := []struct {
		name       string
		seed       []seed
		body       string
		wantStatus int
		wantUser   string // expected stored/response username (empty = don't check)
		wantRole   user.Role
	}{
		{
			name:       "creates admin",
			body:       `{"username":"alice","password":"password1","role":"admin"}`,
			wantStatus: http.StatusCreated,
			wantUser:   "alice",
			wantRole:   user.RoleAdmin,
		},
		{
			name:       "empty role defaults to user",
			body:       `{"username":"bob","password":"password1"}`,
			wantStatus: http.StatusCreated,
			wantUser:   "bob",
			wantRole:   user.RoleUser,
		},
		{
			name:       "username is trimmed",
			body:       `{"username":"  carol  ","password":"password1"}`,
			wantStatus: http.StatusCreated,
			wantUser:   "carol",
			wantRole:   user.RoleUser,
		},
		{
			name:       "duplicate exact is conflict",
			seed:       []seed{{"alice", "password1", user.RoleAdmin}},
			body:       `{"username":"alice","password":"password1"}`,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "duplicate case variant is conflict",
			seed:       []seed{{"alice", "password1", user.RoleAdmin}},
			body:       `{"username":"ALICE","password":"password1"}`,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "bad role is bad request",
			body:       `{"username":"dave","password":"password1","role":"superuser"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "weak password is bad request",
			body:       `{"username":"dave","password":"short"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "password over 72 bytes is bad request",
			body:       `{"username":"eve","password":"` + strings.Repeat("a", 73) + `"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "few multibyte runes is bad request (rune minimum)",
			body:       `{"username":"gina","password":"áááá"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "multibyte password counted by runes is accepted",
			body:       `{"username":"frank","password":"pässwörd1"}`,
			wantStatus: http.StatusCreated,
			wantUser:   "frank",
			wantRole:   user.RoleUser,
		},
		{
			name:       "empty username is bad request",
			body:       `{"username":"   ","password":"password1"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed body is bad request",
			body:       `{"username":`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, store := newUserMgmtServer(t)
			for _, sd := range tt.seed {
				seedUser(t, store, sd.username, sd.password, sd.role, false)
			}

			rec := dispatchUser(userMux(s), http.MethodPost, "/api/users", adminActor, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusCreated {
				return
			}

			var got userResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
			}
			if got.Username != tt.wantUser || got.Role != tt.wantRole || got.ID == 0 {
				t.Errorf("response = %+v, want username=%q role=%q with an ID", got, tt.wantUser, tt.wantRole)
			}
			// Persisted exactly as returned (trimmed).
			stored, err := store.GetUserByUsername(context.Background(), tt.wantUser)
			if err != nil || stored == nil {
				t.Fatalf("stored user %q not found: (%+v, %v)", tt.wantUser, stored, err)
			}
			if stored.Username != tt.wantUser {
				t.Errorf("stored username = %q, want %q", stored.Username, tt.wantUser)
			}
		})
	}
}

// TestUpdateUser covers partial updates, the transactional last-admin guards,
// password hashing, and the validation/not-found mapping.
func TestUpdateUser(t *testing.T) {
	tests := []struct {
		name string
		// setup seeds accounts and returns the target ID.
		setup      func(t *testing.T, store user.Store) int64
		path       string // overrides /api/users/{target} when set
		body       string
		wantStatus int
		check      func(t *testing.T, store user.Store, target int64)
	}{
		{
			name: "role change succeeds",
			setup: func(t *testing.T, store user.Store) int64 {
				seedUser(t, store, "root", "password1", user.RoleAdmin, false)
				return seedUser(t, store, "bob", "password1", user.RoleUser, false).ID
			},
			body:       `{"role":"admin"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, store user.Store, target int64) {
				u, _ := store.GetUser(context.Background(), target)
				if u == nil || u.Role != user.RoleAdmin {
					t.Errorf("role = %+v, want admin", u)
				}
			},
		},
		{
			name: "demote last admin is conflict",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			body:       `{"role":"user"}`,
			wantStatus: http.StatusConflict,
			check: func(t *testing.T, store user.Store, target int64) {
				u, _ := store.GetUser(context.Background(), target)
				if u == nil || u.Role != user.RoleAdmin {
					t.Errorf("last admin was demoted anyway: %+v", u)
				}
			},
		},
		{
			name: "disable last admin is conflict",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			body:       `{"disabled":true}`,
			wantStatus: http.StatusConflict,
			check: func(t *testing.T, store user.Store, target int64) {
				u, _ := store.GetUser(context.Background(), target)
				if u == nil || u.Disabled {
					t.Errorf("last admin was disabled anyway: %+v", u)
				}
			},
		},
		{
			name: "demote admin succeeds when another admin remains",
			setup: func(t *testing.T, store user.Store) int64 {
				seedUser(t, store, "root", "password1", user.RoleAdmin, false)
				return seedUser(t, store, "second", "password1", user.RoleAdmin, false).ID
			},
			body:       `{"role":"user"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, store user.Store, target int64) {
				u, _ := store.GetUser(context.Background(), target)
				if u == nil || u.Role != user.RoleUser {
					t.Errorf("role = %+v, want user", u)
				}
			},
		},
		{
			name: "password change is hashed, never stored plaintext",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			body:       `{"password":"newpassword"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, store user.Store, target int64) {
				u, _ := store.GetUser(context.Background(), target)
				if u == nil {
					t.Fatal("target vanished")
				}
				if u.PasswordHash == "newpassword" {
					t.Error("password stored in plaintext")
				}
				if !config.CheckPassword(u.PasswordHash, "newpassword") {
					t.Error("stored hash does not verify the new password")
				}
			},
		},
		{
			name: "bad role is bad request",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			body:       `{"role":"superuser"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "weak password is bad request",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			body:       `{"password":"short"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "password over 72 bytes is bad request",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			body:       `{"password":"` + strings.Repeat("a", 73) + `"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "empty body is a no-op success",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			body:       `{}`,
			wantStatus: http.StatusOK,
		},
		{
			name: "unknown user is not found",
			setup: func(t *testing.T, store user.Store) int64 {
				return 9999
			},
			body:       `{"role":"user"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name: "non-numeric id is bad request",
			setup: func(t *testing.T, store user.Store) int64 {
				return 1
			},
			path:       "/api/users/not-a-number",
			body:       `{"role":"admin"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, store := newUserMgmtServer(t)
			target := tt.setup(t, store)
			path := tt.path
			if path == "" {
				path = "/api/users/" + itoa(target)
			}

			rec := dispatchUser(userMux(s), http.MethodPatch, path, adminActor, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.check != nil {
				tt.check(t, store, target)
			}
		})
	}
}

// TestUpdateUserNoOpDoesNotWrite proves an empty PATCH returns 200 with the
// current row and performs no write: updated_at is pinned to the past and must
// survive, which a full-row rewrite (the old behavior) would not.
func TestUpdateUserNoOpDoesNotWrite(t *testing.T) {
	s, store, db := newUserMgmtServerDB(t)
	target := seedUser(t, store, "root", "password1", user.RoleAdmin, false)

	const pinned = "2000-01-01T00:00:00Z"
	if _, err := db.Exec(`UPDATE users SET updated_at = ? WHERE id = ?`, pinned, target.ID); err != nil {
		t.Fatalf("pin updated_at: %v", err)
	}
	before, err := store.GetUser(context.Background(), target.ID)
	if err != nil || before == nil {
		t.Fatalf("read before: (%+v, %v)", before, err)
	}

	rec := dispatchUser(userMux(s), http.MethodPatch, "/api/users/"+itoa(target.ID), adminActor, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got userResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("response updated_at = %v, want unchanged %v", got.UpdatedAt, before.UpdatedAt)
	}

	after, err := store.GetUser(context.Background(), target.ID)
	if err != nil || after == nil {
		t.Fatalf("read after: (%+v, %v)", after, err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("stored updated_at = %v; a no-op PATCH must not bump it (was %v)", after.UpdatedAt, before.UpdatedAt)
	}
}

// TestUpdateUserRoleOnlyPreservesPasswordHash is the regression guard for the
// stale-write race: a role-only PATCH must never rewrite the stored password
// hash (the old handler passed a full pre-transaction row to the store).
func TestUpdateUserRoleOnlyPreservesPasswordHash(t *testing.T) {
	s, store := newUserMgmtServer(t)
	seedUser(t, store, "root", "password1", user.RoleAdmin, false)
	target := seedUser(t, store, "bob", "bob-password", user.RoleUser, false)
	before, err := store.GetUser(context.Background(), target.ID)
	if err != nil || before == nil {
		t.Fatalf("read before: (%+v, %v)", before, err)
	}

	rec := dispatchUser(userMux(s), http.MethodPatch, "/api/users/"+itoa(target.ID), adminActor, `{"role":"admin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	after, err := store.GetUser(context.Background(), target.ID)
	if err != nil || after == nil {
		t.Fatalf("read after: (%+v, %v)", after, err)
	}
	if after.Role != user.RoleAdmin {
		t.Errorf("role = %q, want admin", after.Role)
	}
	if after.PasswordHash != before.PasswordHash {
		t.Errorf("password hash changed on a role-only PATCH")
	}
	if !config.CheckPassword(after.PasswordHash, "bob-password") {
		t.Error("stored hash no longer verifies the original password")
	}
}

// TestUpdateUserInvalidationOnActualChange proves sessions are retired only
// when a security-relevant value actually changes, not merely when a field is
// present in the body.
func TestUpdateUserInvalidationOnActualChange(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantInvalidated bool
	}{
		{name: "same role does not invalidate", body: `{"role":"user"}`, wantInvalidated: false},
		{name: "same disabled value does not invalidate", body: `{"disabled":false}`, wantInvalidated: false},
		{name: "role change invalidates", body: `{"role":"admin"}`, wantInvalidated: true},
		{name: "disable invalidates", body: `{"disabled":true}`, wantInvalidated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, store := newUserMgmtServer(t)
			seedUser(t, store, "root", "password1", user.RoleAdmin, false)
			target := seedUser(t, store, "bob", "password1", user.RoleUser, false)
			token, _, _ := s.sessions.Create(target)

			rec := dispatchUser(userMux(s), http.MethodPatch, "/api/users/"+itoa(target.ID), adminActor, tt.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			_, live := s.sessions.Validate(token)
			if wantLive := !tt.wantInvalidated; live != wantLive {
				t.Errorf("session live = %v, want %v (body %s)", live, wantLive, tt.body)
			}
		})
	}
}

// TestUpdateUserInvalidatesSessions proves a disabled/demoted account loses its
// live session immediately (the session holds a role copy).
func TestUpdateUserInvalidatesSessions(t *testing.T) {
	s, store := newUserMgmtServer(t)
	target := seedUser(t, store, "bob", "password1", user.RoleUser, false)
	token, _, _ := s.sessions.Create(target)
	if _, ok := s.sessions.Validate(token); !ok {
		t.Fatal("precondition: session should be valid")
	}

	rec := dispatchUser(userMux(s), http.MethodPatch, "/api/users/"+itoa(target.ID), adminActor, `{"disabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := s.sessions.Validate(token); ok {
		t.Error("session survived a disable; must be invalidated")
	}
}

// TestSessionInvalidationRejectsNextRequest is the H2 end-to-end guard: a user
// holding a live session whose account is security-relevantly changed by an
// admin is rejected (401) on their very next authenticated request. It drives
// the real withAuth chain with the session cookie rather than asserting on the
// session store, so a regression that stops calling DeleteByUserID fails here
// even if the store method still works.
//
// Covered mutations:
//   - disable / delete (already-shipped behavior),
//   - role change (R3 gap 5: the session carries a role copy),
//   - admin password reset (R3 gap 6),
//   - self password change (the acting admin is logged out on next request).
func TestSessionInvalidationRejectsNextRequest(t *testing.T) {
	tests := []struct {
		name string
		// self means the mutation targets the acting admin's own account, so the
		// affected session is the admin's own token.
		self bool
		// mutate performs the admin action and returns its response. targetID is
		// the target account (the caller's own id when self is true).
		mutate     func(s *Server, mux *http.ServeMux, adminToken, targetID string) *httptest.ResponseRecorder
		wantMutate int
	}{
		{
			name: "disable",
			mutate: func(s *Server, mux *http.ServeMux, adminToken, targetID string) *httptest.ResponseRecorder {
				return dispatchSession(s, mux, http.MethodPatch, "/api/users/"+targetID, adminToken, `{"disabled":true}`)
			},
			wantMutate: http.StatusOK,
		},
		{
			name: "delete",
			mutate: func(s *Server, mux *http.ServeMux, adminToken, targetID string) *httptest.ResponseRecorder {
				return dispatchSession(s, mux, http.MethodDelete, "/api/users/"+targetID, adminToken, "")
			},
			wantMutate: http.StatusOK,
		},
		{
			name: "role change",
			mutate: func(s *Server, mux *http.ServeMux, adminToken, targetID string) *httptest.ResponseRecorder {
				return dispatchSession(s, mux, http.MethodPatch, "/api/users/"+targetID, adminToken, `{"role":"admin"}`)
			},
			wantMutate: http.StatusOK,
		},
		{
			name: "password reset",
			mutate: func(s *Server, mux *http.ServeMux, adminToken, targetID string) *httptest.ResponseRecorder {
				return dispatchSession(s, mux, http.MethodPatch, "/api/users/"+targetID, adminToken, `{"password":"reset-password"}`)
			},
			wantMutate: http.StatusOK,
		},
		{
			name: "self password change",
			self: true,
			mutate: func(s *Server, mux *http.ServeMux, adminToken, targetID string) *httptest.ResponseRecorder {
				return dispatchSession(s, mux, http.MethodPatch, "/api/users/"+targetID, adminToken, `{"password":"self-password"}`)
			},
			wantMutate: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, store := newSessionAuthServer(t)
			admin := seedUser(t, store, "root", "password1", user.RoleAdmin, false)
			target := seedUser(t, store, "bob", "password1", user.RoleUser, false)

			adminToken, _, _ := s.sessions.Create(admin)
			targetToken, _, _ := s.sessions.Create(target)
			mux := userMux(s)

			// The session that must be retired, and the account it belongs to.
			presentToken, mutateID := targetToken, target.ID
			if tt.self {
				presentToken, mutateID = adminToken, admin.ID
			}

			// Precondition: the affected session authenticates before the change.
			if rec := dispatchSession(s, mux, http.MethodGet, "/api/me", presentToken, ""); rec.Code != http.StatusOK {
				t.Fatalf("precondition: GET /api/me = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}

			if rec := tt.mutate(s, mux, adminToken, itoa(mutateID)); rec.Code != tt.wantMutate {
				t.Fatalf("%s mutation status = %d, want %d (%s)", tt.name, rec.Code, tt.wantMutate, rec.Body.String())
			}

			// The very next request with the affected cookie must be rejected:
			// withAuth no longer resolves a session, so the request is 401.
			if rec := dispatchSession(s, mux, http.MethodGet, "/api/me", presentToken, ""); rec.Code != http.StatusUnauthorized {
				t.Fatalf("GET /api/me after %s = %d, want 401 (%s)", tt.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestDeleteUser covers success, the last-admin guard, self-delete prevention,
// and not-found/invalid-id mapping.
func TestDeleteUser(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, store user.Store) int64
		path       string
		actor      func(target int64) Identity
		wantStatus int
		wantGone   bool
	}{
		{
			name: "deletes a non-last user",
			setup: func(t *testing.T, store user.Store) int64 {
				seedUser(t, store, "root", "password1", user.RoleAdmin, false)
				return seedUser(t, store, "bob", "password1", user.RoleUser, false).ID
			},
			wantStatus: http.StatusOK,
			wantGone:   true,
		},
		{
			name: "last admin is conflict",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			wantStatus: http.StatusConflict,
			wantGone:   false,
		},
		{
			name: "deletes one admin when another remains",
			setup: func(t *testing.T, store user.Store) int64 {
				seedUser(t, store, "root", "password1", user.RoleAdmin, false)
				return seedUser(t, store, "second", "password1", user.RoleAdmin, false).ID
			},
			wantStatus: http.StatusOK,
			wantGone:   true,
		},
		{
			name: "self-delete is forbidden",
			setup: func(t *testing.T, store user.Store) int64 {
				return seedUser(t, store, "root", "password1", user.RoleAdmin, false).ID
			},
			actor: func(target int64) Identity {
				return Identity{UserID: target, Username: "root", Role: user.RoleAdmin}
			},
			wantStatus: http.StatusForbidden,
			wantGone:   false,
		},
		{
			name: "unknown user is not found",
			setup: func(t *testing.T, store user.Store) int64 {
				return 9999
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "non-numeric id is bad request",
			setup: func(t *testing.T, store user.Store) int64 {
				return 1
			},
			path:       "/api/users/not-a-number",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, store := newUserMgmtServer(t)
			target := tt.setup(t, store)
			id := adminActor
			if tt.actor != nil {
				id = tt.actor(target)
			}
			path := tt.path
			if path == "" {
				path = "/api/users/" + itoa(target)
			}

			rec := dispatchUser(userMux(s), http.MethodDelete, path, id, "")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusOK || tt.wantStatus == http.StatusConflict {
				u, err := store.GetUser(context.Background(), target)
				if err != nil {
					t.Fatalf("GetUser: %v", err)
				}
				if tt.wantGone && u != nil {
					t.Errorf("user %d still present after delete", target)
				}
				if !tt.wantGone && u == nil {
					t.Errorf("user %d removed despite guard", target)
				}
			}
		})
	}
}

// TestUserRoutesNonAdminForbidden proves every /api/users route is wrapped with
// adminOnly: a regular user gets a stable 403, never a handler result.
func TestUserRoutesNonAdminForbidden(t *testing.T) {
	s, _ := newUserMgmtServer(t)
	mux := userMux(s)

	regular := Identity{UserID: 2, Username: "bob", Role: user.RoleUser}
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/users"},
		{http.MethodPost, "/api/users"},
		{http.MethodPatch, "/api/users/1"},
		{http.MethodDelete, "/api/users/1"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rec := dispatchUser(mux, rt.method, rt.path, regular, `{}`)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (route may be unwrapped)", rec.Code)
			}
			if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, "forbidden") {
				t.Errorf("body = %q, want forbidden", body)
			}
		})
	}
}

// TestUserRoutesAdminReachable is the anti-over-wrapping guard: an admin passes
// the gate and reaches the store-backed handler.
func TestUserRoutesAdminReachable(t *testing.T) {
	s, _ := newUserMgmtServer(t)
	rec := dispatchUser(userMux(s), http.MethodGet, "/api/users", adminActor, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET /api/users status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// itoa formats a user ID for a request path.
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
