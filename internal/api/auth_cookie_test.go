package api

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/user"
)

// The session cookie must be Secure when the request arrived over TLS — whether
// terminated at the app (r.TLS) or at a reverse proxy (X-Forwarded-Proto).
// Direct TLS must win: a spoofed header may only force Secure on, never off.
func TestHandleLogin_SecureCookie(t *testing.T) {
	hash, err := config.HashPassword("secret-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	store := &fakeUserStore{users: []user.User{
		{ID: 1, Username: "admin", PasswordHash: hash, Role: user.RoleAdmin},
	}}
	const body = `{"username":"admin","password":"secret-password"}`

	tests := []struct {
		name       string
		tls        bool
		xfp        string
		wantSecure bool
	}{
		{name: "plain http -> not secure", wantSecure: false},
		{name: "https proxy -> secure", xfp: "https", wantSecure: true},
		{name: "mixed case -> secure", xfp: "HTTPS", wantSecure: true},
		{name: "comma list -> secure", xfp: "https, http", wantSecure: true},
		{name: "http proxy -> not secure", xfp: "http", wantSecure: false},
		{name: "direct tls -> secure", tls: true, wantSecure: true},
		{name: "direct tls beats spoofed http header", tls: true, xfp: "http", wantSecure: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newLoginTestServer(t, store, nil)
			req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			rec := httptest.NewRecorder()
			srv.handleLogin(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			cookie := findSessionCookie(rec.Result().Cookies())
			if cookie == nil {
				t.Fatal("no groovearr_sid cookie set")
			}
			if cookie.Secure != tc.wantSecure {
				t.Fatalf("cookie.Secure = %v, want %v", cookie.Secure, tc.wantSecure)
			}
		})
	}
}

// Logout must clear the cookie with the same HttpOnly/SameSite treatment (and a
// Secure attribute matching the connection) so the browser matches and drops
// it; MaxAge<0 deletes it.
func TestHandleLogout_ClearsCookie(t *testing.T) {
	srv, sessions := newLoginTestServer(t, &fakeUserStore{}, nil)

	tests := []struct {
		name       string
		xfp        string
		wantSecure bool
	}{
		{name: "plain", wantSecure: false},
		{name: "https proxy", xfp: "https", wantSecure: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token, _, err := sessions.Create(context.Background(), user.User{ID: 1, Username: "admin", Role: user.RoleAdmin})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
			req.AddCookie(&http.Cookie{Name: "groovearr_sid", Value: token})
			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}
			rec := httptest.NewRecorder()
			srv.handleLogout(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			cookie := findSessionCookie(rec.Result().Cookies())
			if cookie == nil {
				t.Fatal("logout did not set the clearing cookie")
			}
			if cookie.MaxAge >= 0 {
				t.Fatalf("MaxAge = %d, want < 0", cookie.MaxAge)
			}
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie attrs = %+v, want HttpOnly + SameSite=Lax", cookie)
			}
			if cookie.Secure != tc.wantSecure {
				t.Fatalf("cookie.Secure = %v, want %v", cookie.Secure, tc.wantSecure)
			}
			if _, ok, err := sessions.Validate(context.Background(), token); err != nil || ok {
				t.Fatalf("logout did not invalidate the session (ok=%v, err=%v)", ok, err)
			}
		})
	}
}

// The timing-equalizer hash must be a real bcrypt hash; if it ever degrades to
// "", unknown-user logins would take a fast path and leak existence by timing.
func TestDummyLoginHashIsValid(t *testing.T) {
	if h := dummyLoginHash(); !strings.HasPrefix(h, "$2") {
		t.Fatalf("dummyLoginHash() = %q, want a bcrypt hash", h)
	}
}
