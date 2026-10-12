package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/user"
)

// ─── handleUpdateConfig → forms-switch safety ────────────────────────
//
// Switching auth.method to "forms" at runtime must never strip the only way
// back into the admin-only settings it protects. The canonical failure was a
// store holding only a non-admin account (or no account) plus a hot switch to
// forms: login then had nothing to authenticate as admin against, and
// PUT /api/config was itself adminOnly — a lockout. These tests pin the
// behavior: seed from a request-supplied password when the store is empty,
// refuse when no enabled admin would exist, and classify store failures as
// 500 rather than misreporting them as invalid input.

// formsSwitchServer builds the minimal Server handleUpdateConfig needs when the
// payload touches only auth: Sources stays empty, so reconcileAfterConfigUpdate
// short-circuits before any plugin work. baseAuth seeds the starting config.
func formsSwitchServer(t *testing.T, baseAuth config.AuthConfig, store user.Store) *Server {
	t.Helper()
	p := testPersistence(t)
	if err := p.Update(func(c *config.Config) error {
		c.Auth = baseAuth
		return nil
	}); err != nil {
		t.Fatalf("seed base auth: %v", err)
	}
	return &Server{
		log:        testAPILogger(),
		cfg:        p,
		bgCtx:      context.Background(),
		mdRegistry: metadata.NewRegistry(),
		userStore:  store,
	}
}

func putConfigBody(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleUpdateConfig(rec, req)
	return rec
}

func TestUpdateConfig_EnableForms_SwitchSafety(t *testing.T) {
	staleHash, err := config.HashPassword("old-secret")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	const (
		formsWithCreds = `{"auth":{"method":"forms","username":"admin","password":"secret-password"}}`
		formsNoCreds   = `{"auth":{"method":"forms"}}`
	)
	// 73-byte password: a valid non-masked plaintext that bcrypt refuses, which
	// Merge would otherwise drop silently while reporting success.
	longPasswordBody := `{"auth":{"method":"forms","username":"admin","password":"` + strings.Repeat("x", 73) + `"}}`

	tests := []struct {
		name        string
		baseAuth    config.AuthConfig
		store       user.Store
		body        string
		wantStatus  int
		wantForms   bool
		wantUsers   int    // -1 skips the count check (nil store)
		wantErrFrag string // non-empty => response body must contain it
		wantNoFrag  string // non-empty => response body must NOT contain it
	}{
		{
			name:       "seeds first admin on switch with supplied password",
			baseAuth:   config.AuthConfig{Method: ""},
			store:      &fakeUserStore{},
			body:       formsWithCreds,
			wantStatus: http.StatusOK,
			wantForms:  true,
			wantUsers:  1,
		},
		{
			name:        "refuses switch when no password is supplied to seed",
			baseAuth:    config.AuthConfig{Method: "", Username: "admin", Password: staleHash},
			store:       &fakeUserStore{},
			body:        formsNoCreds,
			wantStatus:  http.StatusBadRequest,
			wantForms:   false,
			wantUsers:   0,
			wantErrFrag: "first admin username and password",
		},
		{
			name:        "refuses switch when store has no admin",
			baseAuth:    config.AuthConfig{Method: ""},
			store:       &fakeUserStore{users: []user.User{{ID: 1, Username: "sjoerd", Role: user.RoleUser}}},
			body:        formsWithCreds,
			wantStatus:  http.StatusBadRequest,
			wantForms:   false,
			wantUsers:   1,
			wantErrFrag: "no enabled admin account",
		},
		{
			name:       "refuses switch when the only admin is disabled",
			baseAuth:   config.AuthConfig{Method: ""},
			store:      &fakeUserStore{users: []user.User{{ID: 1, Username: "admin", Role: user.RoleAdmin, Disabled: true}, {ID: 2, Username: "sjoerd", Role: user.RoleUser}}},
			body:       formsWithCreds,
			wantStatus: http.StatusBadRequest,
			wantForms:  false,
			wantUsers:  2,
		},
		{
			name:       "allows switch when an active admin exists and seeds nothing",
			baseAuth:   config.AuthConfig{Method: ""},
			store:      &fakeUserStore{users: []user.User{{ID: 1, Username: "root", Role: user.RoleAdmin, PasswordHash: "$2a$10$x"}, {ID: 2, Username: "sjoerd", Role: user.RoleUser}}},
			body:       formsWithCreds,
			wantStatus: http.StatusOK,
			wantForms:  true,
			wantUsers:  2,
		},
		{
			name:        "nil store fails closed with 500",
			baseAuth:    config.AuthConfig{Method: ""},
			store:       nil,
			body:        formsWithCreds,
			wantStatus:  http.StatusInternalServerError,
			wantForms:   false,
			wantUsers:   -1,
			wantErrFrag: "internal error",
			wantNoFrag:  "nil store",
		},
		{
			name:        "store failure is a 500, not a 400",
			baseAuth:    config.AuthConfig{Method: ""},
			store:       &fakeUserStore{listErr: errors.New("db down")},
			body:        formsWithCreds,
			wantStatus:  http.StatusInternalServerError,
			wantForms:   false,
			wantUsers:   -1,
			wantErrFrag: "internal error",
			wantNoFrag:  "db down",
		},
		{
			name:       "a payload that fails validation does not seed an account",
			baseAuth:   config.AuthConfig{Method: ""},
			store:      &fakeUserStore{},
			body:       `{"auth":{"method":"forms","username":"admin","password":"secret-password"},"logging":{"level":"bogus"}}`,
			wantStatus: http.StatusBadRequest,
			wantForms:  false,
			wantUsers:  0,
		},
		{
			name:        "a masked password does not seed an account",
			baseAuth:    config.AuthConfig{Method: "", Username: "admin", Password: staleHash},
			store:       &fakeUserStore{},
			body:        `{"auth":{"method":"forms","username":"admin","password":"********"}}`,
			wantStatus:  http.StatusBadRequest,
			wantForms:   false,
			wantUsers:   0,
			wantErrFrag: "first admin username and password",
		},
		{
			name:        "a too-short first admin password is refused",
			baseAuth:    config.AuthConfig{Method: ""},
			store:       &fakeUserStore{},
			body:        `{"auth":{"method":"forms","username":"admin","password":"short"}}`,
			wantStatus:  http.StatusBadRequest,
			wantForms:   false,
			wantUsers:   0,
			wantErrFrag: "at least 8 characters",
		},
		{
			name:        "an over-long first admin password is refused at the edge",
			baseAuth:    config.AuthConfig{Method: ""},
			store:       &fakeUserStore{},
			body:        longPasswordBody,
			wantStatus:  http.StatusBadRequest,
			wantForms:   false,
			wantUsers:   0,
			wantErrFrag: "at most 72 bytes",
		},
		{
			name:       "guard is skipped when forms is already active",
			baseAuth:   config.AuthConfig{Method: "forms", Username: "admin", Password: staleHash},
			store:      &fakeUserStore{listErr: errors.New("must not be read")},
			body:       formsNoCreds,
			wantStatus: http.StatusOK,
			wantForms:  true,
			wantUsers:  -1,
		},
		{
			name:       "disabling auth does not touch the store",
			baseAuth:   config.AuthConfig{Method: ""},
			store:      &fakeUserStore{},
			body:       `{"auth":{"method":"none"}}`,
			wantStatus: http.StatusOK,
			wantForms:  false,
			wantUsers:  0,
		},
		{
			name:       "forms to none without re-reading the store",
			baseAuth:   config.AuthConfig{Method: "forms", Username: "admin", Password: staleHash},
			store:      &fakeUserStore{listErr: errors.New("must not be read")},
			body:       `{"auth":{"method":"none"}}`,
			wantStatus: http.StatusOK,
			wantForms:  false,
			wantUsers:  -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := formsSwitchServer(t, tc.baseAuth, tc.store)

			rec := putConfigBody(t, srv, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := srv.cfg.Get().Auth.Method; (got == "forms") != tc.wantForms {
				t.Fatalf("auth.method = %q, wantForms=%v", got, tc.wantForms)
			}
			if tc.wantErrFrag != "" && !strings.Contains(rec.Body.String(), tc.wantErrFrag) {
				t.Fatalf("body = %s, want it to contain %q", rec.Body.String(), tc.wantErrFrag)
			}
			if tc.wantNoFrag != "" && strings.Contains(rec.Body.String(), tc.wantNoFrag) {
				t.Fatalf("body = %s, must NOT contain %q (internal detail leak)", rec.Body.String(), tc.wantNoFrag)
			}
			if tc.wantUsers >= 0 {
				fake, ok := tc.store.(*fakeUserStore)
				if !ok {
					t.Fatalf("wantUsers set but store is %T", tc.store)
				}
				users, _ := fake.ListUsers(context.Background())
				if len(users) != tc.wantUsers {
					t.Fatalf("store users = %d, want %d: %+v", len(users), tc.wantUsers, users)
				}
			}
		})
	}
}

// The seeded credential must be a usable login: bcrypt-hashed and verifying the
// exact password typed, as an enabled admin.
func TestUpdateConfig_EnableForms_SeedsVerifiableAdmin(t *testing.T) {
	store := &fakeUserStore{}
	srv := formsSwitchServer(t, config.AuthConfig{Method: ""}, store)

	rec := putConfigBody(t, srv, `{"auth":{"method":"forms","username":"admin","password":"secret-password"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	users, _ := store.ListUsers(context.Background())
	if len(users) != 1 {
		t.Fatalf("seeded %d users, want 1: %+v", len(users), users)
	}
	got := users[0]
	if got.Username != "admin" || got.Role != user.RoleAdmin || got.Disabled {
		t.Fatalf("seeded user = %+v, want enabled admin named admin", got)
	}
	if !strings.HasPrefix(got.PasswordHash, "$2") {
		t.Fatalf("seeded password_hash = %q, want a bcrypt hash ($2...)", got.PasswordHash)
	}
	if !config.CheckPassword(got.PasswordHash, "secret-password") {
		t.Fatal("stored hash does not verify the typed password")
	}
}
