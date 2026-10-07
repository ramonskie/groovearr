package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/user"
)

// TestIdentityRoundTrip verifies that an Identity stored with
// contextWithIdentity is returned unchanged by identityFrom, including the
// ok flag. Table-driven per the testing standards.
func TestIdentityRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		id   Identity
	}{
		{
			name: "session admin",
			id:   Identity{UserID: 1, Username: "alice", Role: user.RoleAdmin},
		},
		{
			name: "session regular user",
			id:   Identity{UserID: 42, Username: "bob", Role: user.RoleUser},
		},
		{
			name: "api key admin",
			id:   Identity{Role: user.RoleAdmin, ViaAPIKey: true},
		},
		{
			name: "zero identity",
			id:   Identity{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ctx := context.Background()

			// Act
			got, ok := identityFrom(contextWithIdentity(ctx, tt.id))

			// Assert
			if !ok {
				t.Fatalf("identityFrom() ok = false, want true")
			}
			if got != tt.id {
				t.Errorf("identityFrom() = %+v, want %+v", got, tt.id)
			}
		})
	}
}

// TestIdentityFromMissing verifies that an empty context reports no identity
// and a zero value, so callers can detect unauthenticated callers.
func TestIdentityFromMissing(t *testing.T) {
	// Act
	got, ok := identityFrom(context.Background())

	// Assert
	if ok {
		t.Errorf("identityFrom() ok = true, want false")
	}
	if got != (Identity{}) {
		t.Errorf("identityFrom() = %+v, want zero Identity", got)
	}
}

// TestAdminOnly verifies the authorization gate: admins pass through, while a
// regular user and a caller with no identity are both rejected with
// 403 {"error":"forbidden"}. Table-driven per the testing standards.
func TestAdminOnly(t *testing.T) {
	tests := []struct {
		name       string
		id         *Identity
		wantStatus int
		wantBody   string
		wantCalled bool
	}{
		{
			name:       "admin passes through",
			id:         &Identity{UserID: 1, Username: "alice", Role: user.RoleAdmin},
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		{
			name:       "regular user forbidden",
			id:         &Identity{UserID: 2, Username: "bob", Role: user.RoleUser},
			wantStatus: http.StatusForbidden,
			wantBody:   `{"error":"forbidden"}`,
		},
		{
			name:       "missing identity forbidden",
			id:         nil,
			wantStatus: http.StatusForbidden,
			wantBody:   `{"error":"forbidden"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			s := &Server{}
			called := false
			next := func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}
			r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			if tt.id != nil {
				r = r.WithContext(contextWithIdentity(r.Context(), *tt.id))
			}
			rec := httptest.NewRecorder()

			// Act
			s.adminOnly(next).ServeHTTP(rec, r)

			// Assert
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if called != tt.wantCalled {
				t.Errorf("next called = %v, want %v", called, tt.wantCalled)
			}
			if tt.wantBody != "" {
				if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
					t.Errorf("body = %q, want %q", got, tt.wantBody)
				}
			}
		})
	}
}
