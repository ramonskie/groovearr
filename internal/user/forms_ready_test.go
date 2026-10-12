package user

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
)

// EnsureFormsReady is the domain rule guarding a switch to forms auth: it must
// leave a usable admin login or return an actionable sentinel. Kept at the
// domain layer (not in the API handler) so it is testable without the HTTP
// harness and does not depend on config merging.
func TestEnsureFormsReady(t *testing.T) {
	pass := "secret-password"             // 15 runes/bytes
	seven := strings.Repeat("x", 7)       // below the rune minimum
	eight := strings.Repeat("x", 8)       // exactly the rune minimum
	seventyTwo := strings.Repeat("x", 72) // exactly the byte maximum
	seventyThree := strings.Repeat("x", 73)
	// 4 runes but 8 bytes: proves the minimum is counted in runes, not bytes.
	multibyte := strings.Repeat("é", 4)

	tests := []struct {
		name        string
		store       Store
		req         FormsRequest
		wantErr     error // errors.Is target when not an infrastructure error
		wantInfra   bool
		wantCreated bool
	}{
		{
			name:        "empty store seeds admin from supplied password",
			store:       &fakeStore{},
			req:         FormsRequest{Username: "admin", NewPassword: pass},
			wantCreated: true,
		},
		{
			name:    "empty store without a supplied password refuses",
			store:   &fakeStore{},
			req:     FormsRequest{Username: "admin"},
			wantErr: ErrFirstAdminRequired,
		},
		{
			name:    "empty store without a username refuses",
			store:   &fakeStore{},
			req:     FormsRequest{NewPassword: pass},
			wantErr: ErrFirstAdminRequired,
		},
		{
			name:    "whitespace-only username refuses",
			store:   &fakeStore{},
			req:     FormsRequest{Username: "   ", NewPassword: pass},
			wantErr: ErrFirstAdminRequired,
		},
		{
			name:    "a seven-rune password refuses",
			store:   &fakeStore{},
			req:     FormsRequest{Username: "admin", NewPassword: seven},
			wantErr: ErrWeakFirstAdminPassword,
		},
		{
			name:        "an eight-rune password passes",
			store:       &fakeStore{},
			req:         FormsRequest{Username: "admin", NewPassword: eight},
			wantCreated: true,
		},
		{
			name:        "a 72-byte password passes",
			store:       &fakeStore{},
			req:         FormsRequest{Username: "admin", NewPassword: seventyTwo},
			wantCreated: true,
		},
		{
			name:    "a 73-byte password refuses rather than seeding an unverifiable hash",
			store:   &fakeStore{},
			req:     FormsRequest{Username: "admin", NewPassword: seventyThree},
			wantErr: ErrWeakFirstAdminPassword,
		},
		{
			name:    "multibyte below the rune minimum refuses",
			store:   &fakeStore{},
			req:     FormsRequest{Username: "admin", NewPassword: multibyte},
			wantErr: ErrWeakFirstAdminPassword,
		},
		{
			name:    "store with only a regular user refuses",
			store:   &fakeStore{users: []User{{ID: 1, Username: "sjoerd", Role: RoleUser}}},
			req:     FormsRequest{Username: "admin", NewPassword: pass},
			wantErr: ErrNoEnabledAdmin,
		},
		{
			name:    "store with only a disabled admin refuses",
			store:   &fakeStore{users: []User{{ID: 1, Username: "admin", Role: RoleAdmin, Disabled: true, PasswordHash: "$2a$10$x"}}},
			req:     FormsRequest{Username: "admin", NewPassword: pass},
			wantErr: ErrNoEnabledAdmin,
		},
		{
			name:    "an admin with no password hash refuses",
			store:   &fakeStore{users: []User{{ID: 1, Username: "admin", Role: RoleAdmin}}},
			req:     FormsRequest{Username: "admin", NewPassword: pass},
			wantErr: ErrNoEnabledAdmin,
		},
		{
			name:    "an admin whose hash is not bcrypt refuses",
			store:   &fakeStore{users: []User{{ID: 1, Username: "admin", Role: RoleAdmin, PasswordHash: "not-a-hash"}}},
			req:     FormsRequest{Username: "admin", NewPassword: pass},
			wantErr: ErrNoEnabledAdmin,
		},
		{
			name:  "an enabled admin passes without seeding",
			store: &fakeStore{users: []User{{ID: 1, Username: "root", Role: RoleAdmin, PasswordHash: "$2a$10$x"}}},
			req:   FormsRequest{Username: "admin", NewPassword: pass},
		},
		{
			name:  "a lost create race with a seeded admin passes",
			store: &fakeStore{createErr: ErrDuplicateUsername, usersAfter: []User{{ID: 1, Username: "admin", Role: RoleAdmin, PasswordHash: "$2a$10$x"}}},
			req:   FormsRequest{Username: "admin", NewPassword: pass},
		},
		{
			name:    "a lost create race resolving to a non-admin refuses",
			store:   &fakeStore{createErr: ErrDuplicateUsername, usersAfter: []User{{ID: 1, Username: "admin", Role: RoleUser}}},
			req:     FormsRequest{Username: "admin", NewPassword: pass},
			wantErr: ErrNoEnabledAdmin,
		},
		{
			name:      "nil store is an infrastructure error",
			store:     nil,
			req:       FormsRequest{Username: "admin", NewPassword: pass},
			wantInfra: true,
		},
		{
			name:      "a store list failure is an infrastructure error",
			store:     &fakeStore{listErr: errors.New("db down")},
			req:       FormsRequest{Username: "admin", NewPassword: pass},
			wantInfra: true,
		},
		{
			name:      "a lost create race whose re-read fails is an infrastructure error",
			store:     &fakeStore{createErr: ErrDuplicateUsername, listErrAfter: errors.New("db down")},
			req:       FormsRequest{Username: "admin", NewPassword: pass},
			wantInfra: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := EnsureFormsReady(context.Background(), tc.store, tc.req, testLogger())

			if tc.wantInfra {
				if err == nil {
					t.Fatal("want an infrastructure error, got nil")
				}
				if errors.Is(err, ErrFirstAdminRequired) ||
					errors.Is(err, ErrWeakFirstAdminPassword) ||
					errors.Is(err, ErrNoEnabledAdmin) {
					t.Fatalf("infrastructure error must not be an actionable sentinel: %v", err)
				}
				return
			}

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("EnsureFormsReady = %v, want nil", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("EnsureFormsReady = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}

			fake, ok := tc.store.(*fakeStore)
			if !ok {
				return
			}
			created := len(fake.users) > 0 && fake.createCalls > 0
			if created != tc.wantCreated {
				t.Fatalf("created=%v, want %v (users=%+v)", created, tc.wantCreated, fake.users)
			}
		})
	}
}

func TestEnsureFormsReady_SeededAdminIsVerifiable(t *testing.T) {
	store := &fakeStore{}
	req := FormsRequest{Username: "  admin  ", NewPassword: "secret-password"}

	if err := EnsureFormsReady(context.Background(), store, req, testLogger()); err != nil {
		t.Fatalf("EnsureFormsReady: %v", err)
	}
	if len(store.users) != 1 {
		t.Fatalf("users = %d, want 1", len(store.users))
	}
	got := store.users[0]
	if got.Username != "admin" { // trimmed
		t.Fatalf("username = %q, want admin", got.Username)
	}
	if got.Role != RoleAdmin || got.Disabled {
		t.Fatalf("seeded user = %+v, want enabled admin", got)
	}
	if !strings.HasPrefix(got.PasswordHash, "$2") {
		t.Fatalf("password_hash = %q, want bcrypt", got.PasswordHash)
	}
	if !config.CheckPassword(got.PasswordHash, "secret-password") {
		t.Fatal("seeded hash does not verify the supplied password")
	}
}
