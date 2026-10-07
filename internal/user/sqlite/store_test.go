package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ramonskie/groovearr/internal/user"
)

// newTestStore opens a real SQLite database in a temp dir and wraps it with the
// user store, mirroring the sibling sqlite store tests. No network is involved.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := New(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestCreateGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Arrange + Act
	id, err := s.CreateUser(ctx, &user.User{
		Username:     "alice",
		PasswordHash: "$2a$hash",
		Role:         user.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateUser returned zero id")
	}

	// Assert
	byID, err := s.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if byID == nil {
		t.Fatal("GetUser returned nil for existing user")
	}
	if byID.Username != "alice" || byID.PasswordHash != "$2a$hash" || byID.Role != user.RoleAdmin || byID.Disabled {
		t.Fatalf("GetUser = %+v; want alice/admin/hash/not-disabled", byID)
	}
	if byID.CreatedAt.IsZero() || byID.UpdatedAt.IsZero() {
		t.Errorf("timestamps not populated: created=%v updated=%v", byID.CreatedAt, byID.UpdatedAt)
	}
}

func TestGetUserByUsernameCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.CreateUser(ctx, &user.User{Username: "Alice", PasswordHash: "h", Role: user.RoleUser})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	for _, q := range []string{"Alice", "alice", "ALICE", "aLiCe"} {
		got, err := s.GetUserByUsername(ctx, q)
		if err != nil {
			t.Fatalf("GetUserByUsername(%q): %v", q, err)
		}
		if got == nil || got.ID != id {
			t.Fatalf("GetUserByUsername(%q) = %+v; want id %d", q, got, id)
		}
	}
}

func TestListUsersOrderedByUsername(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Arrange: insert out of order, mixed case.
	for _, name := range []string{"carol", "Alice", "bob"} {
		if _, err := s.CreateUser(ctx, &user.User{Username: name, PasswordHash: "h"}); err != nil {
			t.Fatalf("CreateUser(%q): %v", name, err)
		}
	}

	// Act
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}

	// Assert: case-insensitive alphabetical.
	if len(users) != 3 {
		t.Fatalf("len = %d, want 3", len(users))
	}
	got := []string{users[0].Username, users[1].Username, users[2].Username}
	want := []string{"Alice", "bob", "carol"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestUpdateUser(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.CreateUser(ctx, &user.User{Username: "bob", PasswordHash: "old", Role: user.RoleUser})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	created, _ := s.GetUser(ctx, id)

	// Act
	err = s.UpdateUser(ctx, &user.User{
		ID:           id,
		Username:     "robert",
		PasswordHash: "new",
		Role:         user.RoleAdmin,
		Disabled:     true,
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	// Assert
	got, err := s.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "robert" || got.PasswordHash != "new" || got.Role != user.RoleAdmin || !got.Disabled {
		t.Fatalf("updated = %+v; want robert/new/admin/disabled", got)
	}
	if !got.UpdatedAt.After(created.UpdatedAt) && !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want >= created %v", got.UpdatedAt, created.UpdatedAt)
	}
}

func TestUpdateUserMissingReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Act
	err := s.UpdateUser(ctx, &user.User{ID: 9999, Username: "ghost", PasswordHash: "h"})

	// Assert
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateUser missing = %v; want sql.ErrNoRows", err)
	}
}

func TestDeleteUser(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.CreateUser(ctx, &user.User{Username: "temp", PasswordHash: "h"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Act
	if err := s.DeleteUser(ctx, id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// Assert
	got, err := s.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("GetUser after delete = %+v; want nil", got)
	}
	if n, _ := s.CountUsers(ctx); n != 0 {
		t.Fatalf("CountUsers = %d, want 0", n)
	}
}

func TestCountUsers(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if n, err := s.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("CountUsers empty = (%d, %v); want (0, nil)", n, err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.CreateUser(ctx, &user.User{Username: name, PasswordHash: "h"}); err != nil {
			t.Fatalf("CreateUser(%q): %v", name, err)
		}
	}
	if n, err := s.CountUsers(ctx); err != nil || n != 3 {
		t.Fatalf("CountUsers = (%d, %v); want (3, nil)", n, err)
	}
}

func TestCreateUserDuplicateUsernameRejected(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		initial string
		dup     string
	}{
		{name: "exact", initial: "admin", dup: "admin"},
		{name: "case variant upper", initial: "admin", dup: "Admin"},
		{name: "case variant lower from upper", initial: "Admin", dup: "admin"},
		{name: "case variant mixed", initial: "Admin", dup: "aDmIn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.CreateUser(ctx, &user.User{Username: tt.initial, PasswordHash: "h"}); err != nil {
				t.Fatalf("CreateUser(%q): %v", tt.initial, err)
			}

			// Act
			_, err := s.CreateUser(ctx, &user.User{Username: tt.dup, PasswordHash: "h"})

			// Assert
			if err == nil {
				t.Fatalf("CreateUser(%q) succeeded; want duplicate error", tt.dup)
			}
			if !errors.Is(err, user.ErrDuplicateUsername) {
				t.Fatalf("CreateUser(%q) err = %v; want ErrDuplicateUsername", tt.dup, err)
			}
			if n, _ := s.CountUsers(ctx); n != 1 {
				t.Fatalf("CountUsers = %d after rejected duplicate, want 1", n)
			}
		})
	}
}

func TestUsernameTrimmedOnWrite(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Act
	id, err := s.CreateUser(ctx, &user.User{Username: "   alice   ", PasswordHash: "h"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Assert: stored without surrounding whitespace, and lookups trim too.
	got, err := s.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("stored username = %q, want %q", got.Username, "alice")
	}
	if found, err := s.GetUserByUsername(ctx, "  ALICE  "); err != nil || found == nil {
		t.Fatalf("GetUserByUsername trimmed = (%+v, %v); want match", found, err)
	}
}

func TestRoleDefaultsToUser(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.CreateUser(ctx, &user.User{Username: "plain", PasswordHash: "h"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := s.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Role != user.RoleUser {
		t.Fatalf("Role = %q, want %q", got.Role, user.RoleUser)
	}
}

func TestLookupsReturnNilWhenNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Act + Assert: not-found is (nil, nil), never an error.
	byID, err := s.GetUser(ctx, 4242)
	if byID != nil || err != nil {
		t.Fatalf("GetUser missing = (%+v, %v); want (nil, nil)", byID, err)
	}
	byName, err := s.GetUserByUsername(ctx, "ghost")
	if byName != nil || err != nil {
		t.Fatalf("GetUserByUsername missing = (%+v, %v); want (nil, nil)", byName, err)
	}
}

func TestNewIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Arrange: first store creates the table and a row.
	s1, err := New(db)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := s1.CreateUser(ctx, &user.User{Username: "keep", PasswordHash: "h"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Act: constructing again must not fail on the existing table or drop data.
	s2, err := New(db)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}

	// Assert
	if n, err := s2.CountUsers(ctx); err != nil || n != 1 {
		t.Fatalf("CountUsers after re-init = (%d, %v); want (1, nil)", n, err)
	}
}

// createAdmin inserts an enabled admin and returns its ID.
func createAdmin(t *testing.T, s *Store, name string, disabled bool) int64 {
	t.Helper()
	id, err := s.CreateUser(context.Background(), &user.User{
		Username: name, PasswordHash: "h", Role: user.RoleAdmin, Disabled: disabled,
	})
	if err != nil {
		t.Fatalf("CreateUser admin %q: %v", name, err)
	}
	return id
}

func TestDeleteUserGuarded(t *testing.T) {
	ctx := context.Background()

	t.Run("removes a non-last admin", func(t *testing.T) {
		s := newTestStore(t)
		keep := createAdmin(t, s, "root", false)
		remove := createAdmin(t, s, "second", false)

		if err := s.DeleteUserGuarded(ctx, remove); err != nil {
			t.Fatalf("DeleteUserGuarded: %v", err)
		}
		if u, _ := s.GetUser(ctx, keep); u == nil {
			t.Error("unrelated admin was removed")
		}
		if u, _ := s.GetUser(ctx, remove); u != nil {
			t.Error("target admin not removed")
		}
	})

	t.Run("last active admin is refused", func(t *testing.T) {
		s := newTestStore(t)
		root := createAdmin(t, s, "root", false)

		err := s.DeleteUserGuarded(ctx, root)
		if !errors.Is(err, user.ErrLastAdmin) {
			t.Fatalf("err = %v, want ErrLastAdmin", err)
		}
		if u, _ := s.GetUser(ctx, root); u == nil {
			t.Error("last admin was deleted despite guard")
		}
	})

	t.Run("disabled admin does not count as active and is removable", func(t *testing.T) {
		s := newTestStore(t)
		root := createAdmin(t, s, "root", true)

		if err := s.DeleteUserGuarded(ctx, root); err != nil {
			t.Fatalf("DeleteUserGuarded disabled admin: %v", err)
		}
	})

	t.Run("regular user is removable", func(t *testing.T) {
		s := newTestStore(t)
		createAdmin(t, s, "root", false)
		id, err := s.CreateUser(ctx, &user.User{Username: "bob", PasswordHash: "h", Role: user.RoleUser})
		if err != nil {
			t.Fatalf("CreateUser bob: %v", err)
		}

		if err := s.DeleteUserGuarded(ctx, id); err != nil {
			t.Fatalf("DeleteUserGuarded user: %v", err)
		}
	})

	t.Run("missing user returns ErrUserNotFound", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.DeleteUserGuarded(ctx, 4242); !errors.Is(err, user.ErrUserNotFound) {
			t.Fatalf("err = %v, want ErrUserNotFound", err)
		}
	})
}

func TestUpdateUserGuarded(t *testing.T) {
	ctx := context.Background()

	t.Run("role-only patch preserves password hash", func(t *testing.T) {
		s := newTestStore(t)
		root := createAdmin(t, s, "root", false)
		createAdmin(t, s, "second", false) // another active admin, so the demote is allowed

		role := user.RoleUser
		updated, err := s.UpdateUserGuarded(ctx, root, user.UserPatch{Role: &role})
		if err != nil {
			t.Fatalf("UpdateUserGuarded: %v", err)
		}
		if updated.Role != user.RoleUser {
			t.Errorf("returned role = %q, want user", updated.Role)
		}
		if updated.PasswordHash != "h" {
			t.Errorf("returned password hash = %q, want unchanged %q", updated.PasswordHash, "h")
		}
		got, _ := s.GetUser(ctx, root)
		if got == nil || got.Role != user.RoleUser {
			t.Fatalf("stored role = %+v, want user", got)
		}
		if got.PasswordHash != "h" {
			t.Errorf("stored password hash = %q; role-only patch must not touch it", got.PasswordHash)
		}
	})

	t.Run("password-only patch preserves role", func(t *testing.T) {
		s := newTestStore(t)
		root := createAdmin(t, s, "root", false)

		hash := "new-hash"
		updated, err := s.UpdateUserGuarded(ctx, root, user.UserPatch{PasswordHash: &hash})
		if err != nil {
			t.Fatalf("UpdateUserGuarded: %v", err)
		}
		if updated.PasswordHash != "new-hash" {
			t.Errorf("returned password hash = %q, want new-hash", updated.PasswordHash)
		}
		if updated.Role != user.RoleAdmin {
			t.Errorf("returned role = %q; password-only patch must not touch it", updated.Role)
		}
		got, _ := s.GetUser(ctx, root)
		if got == nil || got.Role != user.RoleAdmin || got.PasswordHash != "new-hash" {
			t.Fatalf("stored = %+v; want admin/new-hash", got)
		}
	})

	t.Run("partial patch does not revert a concurrently changed column", func(t *testing.T) {
		// Simulates the stale-write race: the caller read the row when the hash
		// was "h", another writer changes the hash, then the caller patches only
		// the role. The hash must survive because it was never part of the patch.
		s := newTestStore(t)
		root := createAdmin(t, s, "root", false)
		createAdmin(t, s, "second", false)

		if err := s.UpdateUser(ctx, &user.User{
			ID: root, Username: "root", PasswordHash: "changed", Role: user.RoleAdmin,
		}); err != nil {
			t.Fatalf("concurrent password update: %v", err)
		}

		role := user.RoleUser
		if _, err := s.UpdateUserGuarded(ctx, root, user.UserPatch{Role: &role}); err != nil {
			t.Fatalf("UpdateUserGuarded: %v", err)
		}
		got, _ := s.GetUser(ctx, root)
		if got == nil || got.PasswordHash != "changed" {
			t.Errorf("password hash = %+v; role-only patch reverted the concurrent change", got)
		}
	})

	t.Run("nil patch is a no-op and does not bump updated_at", func(t *testing.T) {
		s := newTestStore(t)
		root := createAdmin(t, s, "root", false)

		// Pin updated_at to a known past instant so a write is detectable.
		const pinned = "2000-01-01T00:00:00Z"
		if _, err := s.db.ExecContext(ctx, `UPDATE users SET updated_at = ? WHERE id = ?`, pinned, root); err != nil {
			t.Fatalf("pin updated_at: %v", err)
		}
		before, _ := s.GetUser(ctx, root)

		updated, err := s.UpdateUserGuarded(ctx, root, user.UserPatch{})
		if err != nil {
			t.Fatalf("UpdateUserGuarded: %v", err)
		}
		if updated.ID != root || updated.Username != "root" || updated.Role != user.RoleAdmin {
			t.Errorf("returned = %+v; want the unchanged row", updated)
		}
		if !updated.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("returned updated_at = %v, want unchanged %v", updated.UpdatedAt, before.UpdatedAt)
		}
		got, _ := s.GetUser(ctx, root)
		if !got.UpdatedAt.Equal(parseTime(pinned)) {
			t.Errorf("stored updated_at = %v; a no-op patch must not write", got.UpdatedAt)
		}
	})

	t.Run("demote last active admin is refused", func(t *testing.T) {
		s := newTestStore(t)
		root := createAdmin(t, s, "root", false)

		role := user.RoleUser
		_, err := s.UpdateUserGuarded(ctx, root, user.UserPatch{Role: &role})
		if !errors.Is(err, user.ErrLastAdmin) {
			t.Fatalf("err = %v, want ErrLastAdmin", err)
		}
		got, _ := s.GetUser(ctx, root)
		if got == nil || got.Role != user.RoleAdmin {
			t.Errorf("last admin was demoted despite guard: %+v", got)
		}
	})

	t.Run("disable last active admin is refused", func(t *testing.T) {
		s := newTestStore(t)
		root := createAdmin(t, s, "root", false)

		disabled := true
		_, err := s.UpdateUserGuarded(ctx, root, user.UserPatch{Disabled: &disabled})
		if !errors.Is(err, user.ErrLastAdmin) {
			t.Fatalf("err = %v, want ErrLastAdmin", err)
		}
		got, _ := s.GetUser(ctx, root)
		if got == nil || got.Disabled {
			t.Errorf("last admin was disabled despite guard: %+v", got)
		}
	})

	t.Run("demote admin allowed when another active admin remains", func(t *testing.T) {
		s := newTestStore(t)
		createAdmin(t, s, "root", false)
		second := createAdmin(t, s, "second", false)

		role := user.RoleUser
		updated, err := s.UpdateUserGuarded(ctx, second, user.UserPatch{Role: &role})
		if err != nil {
			t.Fatalf("UpdateUserGuarded: %v", err)
		}
		if updated.Role != user.RoleUser {
			t.Errorf("role = %+v, want user", updated)
		}
		got, _ := s.GetUser(ctx, second)
		if got == nil || got.Role != user.RoleUser {
			t.Errorf("role = %+v, want user", got)
		}
	})

	t.Run("regular user update is not guarded", func(t *testing.T) {
		s := newTestStore(t)
		createAdmin(t, s, "root", false)
		id, _ := s.CreateUser(ctx, &user.User{Username: "bob", PasswordHash: "old", Role: user.RoleUser})

		hash := "new"
		disabled := true
		if _, err := s.UpdateUserGuarded(ctx, id, user.UserPatch{PasswordHash: &hash, Disabled: &disabled}); err != nil {
			t.Fatalf("UpdateUserGuarded regular user: %v", err)
		}
		got, _ := s.GetUser(ctx, id)
		if got == nil || got.PasswordHash != "new" || !got.Disabled {
			t.Errorf("regular user update not applied: %+v", got)
		}
	})

	t.Run("missing user returns ErrUserNotFound", func(t *testing.T) {
		s := newTestStore(t)
		role := user.RoleUser
		updated, err := s.UpdateUserGuarded(ctx, 4242, user.UserPatch{Role: &role})
		if !errors.Is(err, user.ErrUserNotFound) {
			t.Fatalf("err = %v, want ErrUserNotFound", err)
		}
		if updated != nil {
			t.Errorf("updated = %+v, want nil", updated)
		}
	})
}

// newConcurrentTestStore opens a WAL SQLite database whose transactions begin
// with BEGIN IMMEDIATE (via the driver's _txlock parameter). Concurrent guarded
// writes therefore serialize at BEGIN — the loser waits on busy_timeout, then
// reads a fresh snapshot — instead of surfacing SQLITE_BUSY / BUSY_SNAPSHOT.
// That keeps the application-level last-admin race deterministic to assert
// without weakening the invariant under test (the store's transactional guard
// still does the work).
func newConcurrentTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "users.db") +
		"?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := New(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

// countActiveAdmins reads the invariant directly from the database.
func countActiveAdmins(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0`, user.RoleAdmin).Scan(&n); err != nil {
		t.Fatalf("count active admins: %v", err)
	}
	return n
}

// runConcurrentGuardedOp releases two goroutines together and returns their
// outcomes in target order.
func runConcurrentGuardedOp(op func(id int64) error, targets []int64) []error {
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, id := range targets {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			<-start
			errs[i] = op(id)
		}(i, id)
	}
	close(start)
	wg.Wait()
	return errs
}

// assertOneAdminOutcome asserts the last-admin invariant for two racing guarded
// operations on two active admins: exactly one succeeds, exactly one is refused
// with ErrLastAdmin, no other error escapes, and at least one active admin
// survives. The winner is deliberately not asserted — either may lose.
func assertOneAdminOutcome(t *testing.T, s *Store, targets []int64, errs []error) {
	t.Helper()
	var succeeded, lastAdmin int
	for i, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, user.ErrLastAdmin):
			lastAdmin++
		case errors.Is(err, user.ErrUserNotFound):
			// The other operation may have removed/demoted the row first; still
			// a benign, contract-defined outcome.
		default:
			t.Errorf("unexpected error for target %d: %v", targets[i], err)
		}
	}
	if succeeded != 1 {
		t.Errorf("successful ops = %d, want exactly 1 (errs=%v)", succeeded, errs)
	}
	if lastAdmin != 1 {
		t.Errorf("ErrLastAdmin ops = %d, want exactly 1 (errs=%v)", lastAdmin, errs)
	}
	if n := countActiveAdmins(t, s); n < 1 {
		t.Fatalf("active admins after concurrent ops = %d, want >= 1", n)
	}
}

// TestConcurrentGuardedDeleteKeepsOneAdmin is the R3-M3 regression: two active
// admins, two concurrent guarded deletes. The last-admin invariant must hold —
// one delete lands, the other is refused with ErrLastAdmin — and no other error
// may escape.
func TestConcurrentGuardedDeleteKeepsOneAdmin(t *testing.T) {
	ctx := context.Background()
	s := newConcurrentTestStore(t)
	a := createAdmin(t, s, "root", false)
	b := createAdmin(t, s, "second", false)
	targets := []int64{a, b}

	errs := runConcurrentGuardedOp(func(id int64) error {
		return s.DeleteUserGuarded(ctx, id)
	}, targets)
	assertOneAdminOutcome(t, s, targets, errs)
}

// TestConcurrentGuardedDemoteKeepsOneAdmin is the R3-M3 demote variant: two
// active admins demoted concurrently via UpdateUserGuarded. Exactly one
// demotion lands; the other is refused with ErrLastAdmin; at least one admin
// remains.
func TestConcurrentGuardedDemoteKeepsOneAdmin(t *testing.T) {
	ctx := context.Background()
	s := newConcurrentTestStore(t)
	a := createAdmin(t, s, "root", false)
	b := createAdmin(t, s, "second", false)
	targets := []int64{a, b}

	errs := runConcurrentGuardedOp(func(id int64) error {
		role := user.RoleUser
		_, err := s.UpdateUserGuarded(ctx, id, user.UserPatch{Role: &role})
		return err
	}, targets)
	assertOneAdminOutcome(t, s, targets, errs)
}
