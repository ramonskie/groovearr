package user

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
)

// testLogger returns a discard logger so bootstrap tests exercise the
// component logger path without emitting output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStore is an in-memory Store for service tests. It records create calls
// and can force count/create failures so the error paths are covered without a
// real database.
type fakeStore struct {
	users       []User
	createCalls int
	countErr    error
	createErr   error
}

var _ Store = (*fakeStore)(nil)

func (f *fakeStore) CreateUser(_ context.Context, u *User) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.createCalls++
	cp := *u
	cp.ID = int64(len(f.users) + 1)
	f.users = append(f.users, cp)
	return cp.ID, nil
}

func (f *fakeStore) GetUser(context.Context, int64) (*User, error) { return nil, nil }

func (f *fakeStore) GetUserByUsername(context.Context, string) (*User, error) { return nil, nil }

func (f *fakeStore) ListUsers(context.Context) ([]User, error) { return f.users, nil }

func (f *fakeStore) UpdateUser(context.Context, *User) error { return nil }

func (f *fakeStore) DeleteUser(context.Context, int64) error { return nil }

func (f *fakeStore) UpdateUserGuarded(context.Context, int64, UserPatch) (*User, error) {
	return nil, nil
}

func (f *fakeStore) DeleteUserGuarded(context.Context, int64) error { return nil }

func (f *fakeStore) CountUsers(context.Context) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return len(f.users), nil
}

// formsConfig builds a minimal forms-auth config for a bootstrap scenario.
func formsConfig(username, password string) config.Config {
	return config.Config{
		Auth: config.AuthConfig{Method: "forms", Username: username, Password: password},
	}
}

func TestEnsureBootstrapAdminSeedsOnEmptyForms(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store := &fakeStore{}
	cfg := formsConfig("admin", "$2a$10$prehashed")

	// Act
	if err := EnsureBootstrapAdmin(ctx, store, cfg, testLogger()); err != nil {
		t.Fatalf("EnsureBootstrapAdmin: %v", err)
	}

	// Assert
	if store.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", store.createCalls)
	}
	if len(store.users) != 1 {
		t.Fatalf("users = %d, want 1", len(store.users))
	}
	got := store.users[0]
	if got.Username != "admin" || got.PasswordHash != "$2a$10$prehashed" || got.Role != RoleAdmin || got.Disabled {
		t.Fatalf("seeded = %+v; want admin/prehash/admin-role/enabled", got)
	}
}

func TestEnsureBootstrapAdminIsIdempotent(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store := &fakeStore{}
	cfg := formsConfig("admin", "$2a$hash")
	if err := EnsureBootstrapAdmin(ctx, store, cfg, testLogger()); err != nil {
		t.Fatalf("first EnsureBootstrapAdmin: %v", err)
	}

	// Act: a second start must not seed again.
	if err := EnsureBootstrapAdmin(ctx, store, cfg, testLogger()); err != nil {
		t.Fatalf("second EnsureBootstrapAdmin: %v", err)
	}

	// Assert
	if store.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1 (second call must be a no-op)", store.createCalls)
	}
	if len(store.users) != 1 {
		t.Fatalf("users = %d, want 1", len(store.users))
	}
}

func TestEnsureBootstrapAdminNoSeed(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		cfg      config.Config
		existing []User
	}{
		{
			name: "method none",
			cfg:  config.Config{Auth: config.AuthConfig{Method: "none", Username: "admin", Password: "$2a$hash"}},
		},
		{
			name: "method empty",
			cfg:  config.Config{Auth: config.AuthConfig{Username: "admin", Password: "$2a$hash"}},
		},
		{
			name: "username empty",
			cfg:  formsConfig("", "$2a$hash"),
		},
		{
			name: "username whitespace only",
			cfg:  formsConfig("   ", "$2a$hash"),
		},
		{
			name: "password empty",
			cfg:  formsConfig("admin", ""),
		},
		{
			name:     "users already exist",
			cfg:      formsConfig("admin", "$2a$hash"),
			existing: []User{{ID: 1, Username: "someone", Role: RoleUser}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			store := &fakeStore{users: append([]User(nil), tt.existing...)}

			// Act
			if err := EnsureBootstrapAdmin(ctx, store, tt.cfg, testLogger()); err != nil {
				t.Fatalf("EnsureBootstrapAdmin: %v", err)
			}

			// Assert
			if store.createCalls != 0 {
				t.Fatalf("createCalls = %d, want 0", store.createCalls)
			}
			if len(store.users) != len(tt.existing) {
				t.Fatalf("users = %d, want %d", len(store.users), len(tt.existing))
			}
		})
	}
}

func TestEnsureBootstrapAdminWrapsCountError(t *testing.T) {
	// Arrange
	ctx := context.Background()
	boom := errors.New("db down")
	store := &fakeStore{countErr: boom}

	// Act
	err := EnsureBootstrapAdmin(ctx, store, formsConfig("admin", "$2a$hash"), testLogger())

	// Assert
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want wrapped %v", err, boom)
	}
}

func TestEnsureBootstrapAdminWrapsCreateError(t *testing.T) {
	// Arrange
	ctx := context.Background()
	boom := errors.New("insert failed")
	store := &fakeStore{createErr: boom}

	// Act
	err := EnsureBootstrapAdmin(ctx, store, formsConfig("admin", "$2a$hash"), testLogger())

	// Assert
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want wrapped %v", err, boom)
	}
}

func TestEnsureBootstrapAdminNilStore(t *testing.T) {
	// Act
	err := EnsureBootstrapAdmin(context.Background(), nil, formsConfig("admin", "$2a$hash"), testLogger())

	// Assert
	if err == nil {
		t.Fatal("nil store: want error")
	}
}

// TestEnsureBootstrapAdminRejectsNonBcryptPassword is the shape-guard regression
// test: a non-empty password that is not a bcrypt hash must fail loudly instead
// of being stored verbatim as if it were a hash.
func TestEnsureBootstrapAdminRejectsNonBcryptPassword(t *testing.T) {
	// Arrange
	ctx := context.Background()
	store := &fakeStore{}
	cfg := formsConfig("admin", "plaintext-password")

	// Act
	err := EnsureBootstrapAdmin(ctx, store, cfg, testLogger())

	// Assert
	if err == nil {
		t.Fatal("non-bcrypt password: want error, got nil")
	}
	if store.createCalls != 0 {
		t.Fatalf("createCalls = %d, want 0 (must not store a non-hash)", store.createCalls)
	}
	if len(store.users) != 0 {
		t.Fatalf("users = %d, want 0", len(store.users))
	}
}

// TestEnsureBootstrapAdminNilLogger proves the nil-logger fallback is safe.
func TestEnsureBootstrapAdminNilLogger(t *testing.T) {
	store := &fakeStore{}
	if err := EnsureBootstrapAdmin(context.Background(), store, formsConfig("admin", "$2a$hash"), nil); err != nil {
		t.Fatalf("nil logger: %v", err)
	}
	if store.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", store.createCalls)
	}
}
