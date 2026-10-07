package api

import (
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/user"
)

// TestSessionStoreRoleRoundTrip verifies Create stores and Validate returns the
// full identity (UserID, Username, Role) plus the expiry it handed back. Each
// case uses its own store so the cases share no state.
func TestSessionStoreRoleRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		user user.User
	}{
		{
			name: "admin",
			user: user.User{ID: 1, Username: "admin", Role: user.RoleAdmin},
		},
		{
			name: "regular user",
			user: user.User{ID: 2, Username: "alice", Role: user.RoleUser},
		},
		{
			name: "zero role",
			user: user.User{ID: 3, Username: "legacy"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSessionStore()
			defer s.Shutdown()

			token, expires, err := s.Create(tt.user)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if token == "" {
				t.Fatal("Create returned an empty token")
			}
			if expires.IsZero() {
				t.Fatal("Create returned a zero expiry")
			}
			// Expiry must stay at the historical 7-day lifetime.
			if d := time.Until(expires); d < 6*24*time.Hour || d > 8*24*time.Hour {
				t.Errorf("expiry in %v, want ~7 days", d)
			}

			got, ok := s.Validate(token)
			if !ok {
				t.Fatal("Validate returned false for a freshly created session")
			}
			if got.UserID != tt.user.ID {
				t.Errorf("UserID = %d, want %d", got.UserID, tt.user.ID)
			}
			if got.Username != tt.user.Username {
				t.Errorf("Username = %q, want %q", got.Username, tt.user.Username)
			}
			if got.Role != tt.user.Role {
				t.Errorf("Role = %q, want %q", got.Role, tt.user.Role)
			}
			if !got.ExpiresAt.Equal(expires) {
				t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
			}
		})
	}
}

// TestSessionStoreDeleteByUserID verifies DeleteByUserID removes exactly the
// target user's sessions and leaves every other user's sessions untouched.
func TestSessionStoreDeleteByUserID(t *testing.T) {
	tests := []struct {
		name      string
		deleteID  int64
		wantValid []bool // expected validity of fixture i's token after delete
	}{
		{
			name:      "removes only the target user's sessions",
			deleteID:  1,
			wantValid: []bool{false, false, true},
		},
		{
			name:      "unknown user id removes nothing",
			deleteID:  99,
			wantValid: []bool{true, true, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSessionStore()
			defer s.Shutdown()

			fixtures := []user.User{
				{ID: 1, Username: "alice", Role: user.RoleAdmin},
				{ID: 1, Username: "alice", Role: user.RoleAdmin}, // second session, same user
				{ID: 2, Username: "bob", Role: user.RoleUser},
			}
			tokens := make([]string, len(fixtures))
			for i, u := range fixtures {
				tokens[i], _, _ = s.Create(u)
			}

			s.DeleteByUserID(tt.deleteID)

			for i, token := range tokens {
				_, ok := s.Validate(token)
				if ok != tt.wantValid[i] {
					t.Errorf("session %d (user %d) valid = %v, want %v",
						i, fixtures[i].ID, ok, tt.wantValid[i])
				}
			}
		})
	}
}

// TestSessionStoreExpiry verifies the expiry gate is unchanged: a future
// session validates, a past one is rejected and evicted.
func TestSessionStoreExpiry(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		wantValid bool
	}{
		{
			name:      "future expiry is valid",
			expiresAt: time.Now().Add(time.Hour),
			wantValid: true,
		},
		{
			name:      "past expiry is rejected",
			expiresAt: time.Now().Add(-time.Hour),
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSessionStore()
			defer s.Shutdown()

			const token = "fixed-test-token"
			s.mu.Lock()
			s.sessions[token] = session{
				UserID:    1,
				Username:  "alice",
				Role:      user.RoleAdmin,
				ExpiresAt: tt.expiresAt,
			}
			s.mu.Unlock()

			_, ok := s.Validate(token)
			if ok != tt.wantValid {
				t.Fatalf("Validate ok = %v, want %v", ok, tt.wantValid)
			}

			s.mu.RLock()
			_, stillStored := s.sessions[token]
			s.mu.RUnlock()
			if tt.wantValid && !stillStored {
				t.Error("valid session was unexpectedly evicted")
			}
			if !tt.wantValid && stillStored {
				t.Error("expired session was not evicted")
			}
		})
	}
}
