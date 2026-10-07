// Package sqlite implements the user.Store interface using SQLite.
//
// It does not own a database: the store wraps a *sql.DB shared with the
// library store (library/sqlite.Store.DB) so the users table lives on the
// same connection and honours the same WAL / foreign_keys settings — the
// same pattern used by internal/metadata/sqlite and internal/tracking/sqlite.
// The store owns its own table and creates it in init().
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/user"
)

// Store implements user.Store backed by a shared SQLite connection.
type Store struct {
	db *sql.DB
}

// New creates the users table (idempotently) and returns a Store backed by db.
// The caller owns the connection; the store never opens or closes its own.
func New(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.init(); err != nil {
		return nil, fmt.Errorf("user sqlite init: %w", err)
	}
	return s, nil
}

// Compile-time guarantee that *Store satisfies the full user.Store contract —
// a missing method fails the build here rather than at wiring time.
var _ user.Store = (*Store)(nil)

// timeLayout is the format produced by SQLite's datetime('now') default, used
// for rows inserted without an explicit timestamp. parseTime tolerates both
// this and RFC3339 so DB-defaulted and store-written rows both parse.
const timeLayout = "2006-01-02 15:04:05"

// scanner is the common surface of *sql.Row and *sql.Rows, letting one scan
// helper serve both the single-row and list queries.
type scanner interface {
	Scan(dest ...any) error
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse(timeLayout, s); err == nil {
		return t
	}
	return time.Time{}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// normalizeUsername trims surrounding whitespace from a username before it is
// written or looked up. Uniqueness is case-insensitive via COLLATE NOCASE.
func normalizeUsername(username string) string {
	return strings.TrimSpace(username)
}

// roleOrDefault collapses an empty role to RoleUser so a caller that omits the
// field persists the documented default rather than an empty string.
func roleOrDefault(r user.Role) user.Role {
	if r == "" {
		return user.RoleUser
	}
	return r
}

func (s *Store) init() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			role          TEXT NOT NULL DEFAULT 'user',
			disabled      INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username COLLATE NOCASE);`)
	if err != nil {
		return fmt.Errorf("create users table: %w", err)
	}
	return nil
}

const userColumns = `id, username, password_hash, role, disabled, created_at, updated_at`

const userSelect = `SELECT ` + userColumns + ` FROM users`

// CreateUser inserts a new account and returns its assigned ID. The username
// is trimmed; an empty role defaults to user. A duplicate username (including
// a case-only variant) is rejected by the unique NOCASE index.
func (s *Store) CreateUser(ctx context.Context, u *user.User) (int64, error) {
	if u == nil {
		return 0, fmt.Errorf("create user: nil user")
	}
	now := nowRFC3339()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, disabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		normalizeUsername(u.Username), u.PasswordHash, roleOrDefault(u.Role),
		boolToInt(u.Disabled), now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("create user %q: %w", u.Username, user.ErrDuplicateUsername)
		}
		return 0, fmt.Errorf("create user %q: %w", u.Username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create user %q: resolve id: %w", u.Username, err)
	}
	return id, nil
}

// GetUser returns the user with the given ID, or (nil, nil) when none exists.
func (s *Store) GetUser(ctx context.Context, id int64) (*user.User, error) {
	row := s.db.QueryRowContext(ctx, userSelect+` WHERE id = ?`, id)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user %d: %w", id, err)
	}
	return u, nil
}

// GetUserByUsername returns the user with the given username using a
// case-insensitive match, or (nil, nil) when none exists.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*user.User, error) {
	row := s.db.QueryRowContext(ctx,
		userSelect+` WHERE username = ? COLLATE NOCASE`, normalizeUsername(username))
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by username %q: %w", username, err)
	}
	return u, nil
}

// ListUsers returns every user ordered by username (case-insensitive), then ID.
func (s *Store) ListUsers(ctx context.Context) ([]user.User, error) {
	rows, err := s.db.QueryContext(ctx, userSelect+` ORDER BY username COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var out []user.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return out, nil
}

// UpdateUser persists the mutable fields (username, password hash, role,
// disabled) for an existing user and refreshes updated_at. The username is
// trimmed; an empty role defaults to user. A rename onto a taken username
// (case-insensitively) is rejected by the unique NOCASE index.
func (s *Store) UpdateUser(ctx context.Context, u *user.User) error {
	if u == nil {
		return fmt.Errorf("update user: nil user")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET username = ?, password_hash = ?, role = ?, disabled = ?, updated_at = ?
		WHERE id = ?`,
		normalizeUsername(u.Username), u.PasswordHash, roleOrDefault(u.Role),
		boolToInt(u.Disabled), nowRFC3339(), u.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("update user %d: %w", u.ID, user.ErrDuplicateUsername)
		}
		return fmt.Errorf("update user %d: %w", u.ID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("update user %d: rows affected: %w", u.ID, err)
	} else if n == 0 {
		return fmt.Errorf("update user %d: %w", u.ID, sql.ErrNoRows)
	}
	return nil
}

// DeleteUser removes the user with the given ID.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete user %d: %w", id, err)
	}
	return nil
}

// DeleteUserGuarded removes a user while enforcing the last-admin invariant.
//
// The existence check, the active-admin count, and the delete all run inside
// one transaction, so a concurrent demotion or delete cannot slip between the
// check and the write. SQLite serializes the write (a second transaction that
// tries to commit against a stale snapshot fails rather than silently leaving
// zero admins), which is why the guard is atomic without a lock in the API.
func (s *Store) DeleteUserGuarded(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete user %d: begin: %w", id, err)
	}
	defer tx.Rollback() // no-op after a successful commit

	target, err := getUserTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("delete user %d: load: %w", id, err)
	}
	if target == nil {
		return fmt.Errorf("delete user %d: %w", id, user.ErrUserNotFound)
	}

	if isActiveAdmin(target) {
		n, err := countActiveAdminsTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("delete user %d: count admins: %w", id, err)
		}
		if n <= 1 {
			return fmt.Errorf("delete user %d: %w", id, user.ErrLastAdmin)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete user %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete user %d: commit: %w", id, err)
	}
	return nil
}

// UpdateUserGuarded applies a delta patch while enforcing the last-admin
// invariant. The existence check, the active-admin count, and the write all
// run in one transaction so a concurrent demotion cannot race past the guard.
//
// Only patch fields that differ from the stored row are written, so a
// concurrent update to another column is never reverted (the stale-write
// race). When nothing differs the row is returned unchanged and updated_at is
// not bumped. A demotion (role != admin) or disable of an active admin is
// refused with ErrLastAdmin when no *other* active admin remains.
func (s *Store) UpdateUserGuarded(ctx context.Context, id int64, patch user.UserPatch) (*user.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("update user %d: begin: %w", id, err)
	}
	defer tx.Rollback() // no-op after a successful commit

	existing, err := getUserTx(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("update user %d: load: %w", id, err)
	}
	if existing == nil {
		return nil, fmt.Errorf("update user %d: %w", id, user.ErrUserNotFound)
	}

	// Build the post-patch row and the set of columns that actually change.
	// Only changed columns are written; the row loaded inside the tx is the
	// authority, so a field the caller did not patch keeps any value a
	// concurrent writer just committed.
	next := *existing
	sets := make([]string, 0, 4)
	args := make([]any, 0, 5)

	if patch.Role != nil {
		next.Role = roleOrDefault(*patch.Role)
		if next.Role != existing.Role {
			sets = append(sets, "role = ?")
			args = append(args, next.Role)
		}
	}
	if patch.Disabled != nil {
		next.Disabled = *patch.Disabled
		if next.Disabled != existing.Disabled {
			sets = append(sets, "disabled = ?")
			args = append(args, boolToInt(next.Disabled))
		}
	}
	if patch.PasswordHash != nil {
		next.PasswordHash = *patch.PasswordHash
		if next.PasswordHash != existing.PasswordHash {
			sets = append(sets, "password_hash = ?")
			args = append(args, next.PasswordHash)
		}
	}

	// No effective change: return the current row untouched so an idempotent
	// PATCH does not rewrite the row or bump updated_at.
	if len(sets) == 0 {
		return existing, nil
	}

	// Guard against the post-patch state, not the request: only a transition
	// away from active-admin is refused, and only when it is the last one.
	if isActiveAdmin(existing) && !isActiveAdmin(&next) {
		n, err := countActiveAdminsTx(ctx, tx)
		if err != nil {
			return nil, fmt.Errorf("update user %d: count admins: %w", id, err)
		}
		if n <= 1 {
			return nil, fmt.Errorf("update user %d: %w", id, user.ErrLastAdmin)
		}
	}

	now := nowRFC3339()
	sets = append(sets, "updated_at = ?")
	args = append(args, now)
	next.UpdatedAt = parseTime(now)
	args = append(args, id)

	res, err := tx.ExecContext(ctx,
		`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("update user %d: %w", id, user.ErrDuplicateUsername)
		}
		return nil, fmt.Errorf("update user %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("update user %d: rows affected: %w", id, err)
	} else if n == 0 {
		return nil, fmt.Errorf("update user %d: %w", id, user.ErrUserNotFound)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("update user %d: commit: %w", id, err)
	}
	return &next, nil
}

// isActiveAdmin reports whether u is an admin account that is not disabled.
func isActiveAdmin(u *user.User) bool {
	return u != nil && u.Role == user.RoleAdmin && !u.Disabled
}

// getUserTx loads a user by ID inside an open transaction, returning
// (nil, nil) when the row is absent (mirroring GetUser's not-found contract).
func getUserTx(ctx context.Context, tx *sql.Tx, id int64) (*user.User, error) {
	row := tx.QueryRowContext(ctx, userSelect+` WHERE id = ?`, id)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// countActiveAdminsTx counts enabled admins inside an open transaction. It is
// the guard read that must share a transaction with the guarded write.
func countActiveAdminsTx(ctx context.Context, tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0`,
		user.RoleAdmin).Scan(&n)
	return n, err
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
// The users table has exactly one unique index (username COLLATE NOCASE), so a
// UNIQUE failure here is a username collision. Drivers surface the condition in
// the message text; matching it keeps the check portable across the
// modernc.org/sqlite versions used here.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CountUsers returns the total number of user accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// scanUser materializes a user row. disabled is stored as INTEGER; an empty
// role (hand-written row) collapses to the RoleUser default.
func scanUser(sc scanner) (*user.User, error) {
	var u user.User
	var disabled int
	var role, createdAt, updatedAt string
	if err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &disabled, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	u.Role = roleFromString(role)
	u.Disabled = disabled != 0
	u.CreatedAt = parseTime(createdAt)
	u.UpdatedAt = parseTime(updatedAt)
	return &u, nil
}

// roleFromString maps a stored role string to a Role, defaulting an empty
// value to RoleUser. It is deliberately permissive on an unknown string so a
// forward-compatible role value round-trips instead of being silently dropped.
func roleFromString(role string) user.Role {
	if role == "" {
		return user.RoleUser
	}
	return user.Role(role)
}
