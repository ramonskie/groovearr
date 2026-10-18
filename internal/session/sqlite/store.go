// Package sqlite implements session.Store on the application's shared SQLite
// connection.
//
// It does not own a database: New wraps the *sql.DB owned by the library store
// (library/sqlite.Store.DB), the same pattern used by internal/user/sqlite and
// internal/metadata/sqlite. The sessions table is created idempotently and the
// schema is additive only (AGENTS §5).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/session"
	"github.com/ramonskie/groovearr/internal/user"
)

// Store implements session.Store backed by a shared SQLite connection.
type Store struct {
	db *sql.DB
}

// Compile-time guarantee that *Store satisfies the contract — a missing method
// fails the build here rather than at wiring time.
var _ session.Store = (*Store)(nil)

// New creates the sessions table (idempotently) and returns a Store backed by
// db. The caller owns the connection; the store never opens or closes its own.
func New(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.init(); err != nil {
		return nil, fmt.Errorf("session sqlite init: %w", err)
	}
	return s, nil
}

const schema = `
	CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL,
		username   TEXT NOT NULL,
		role       TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);`

// init creates the table and indexes. Both statements are idempotent so an
// existing database upgrades in place.
func (s *Store) init() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("create sessions table: %w", err)
	}
	return nil
}

const sessionColumns = `token_hash, user_id, username, role, created_at, expires_at`

// Create inserts a session row.
func (s *Store) Create(ctx context.Context, ses session.Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (`+sessionColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
		ses.TokenHash, ses.UserID, ses.Username, string(ses.Role),
		formatTime(ses.CreatedAt), formatTime(ses.ExpiresAt))
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// Get returns the session for tokenHash, or (zero, false, nil) when absent.
func (s *Store) Get(ctx context.Context, tokenHash string) (session.Session, bool, error) {
	var (
		ses                   session.Session
		role, created, expiry string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&ses.TokenHash, &ses.UserID, &ses.Username, &role, &created, &expiry)
	if err == sql.ErrNoRows {
		return session.Session{}, false, nil
	}
	if err != nil {
		return session.Session{}, false, fmt.Errorf("select session: %w", err)
	}
	ses.Role = user.Role(role)
	ses.CreatedAt = parseTime(created)
	ses.ExpiresAt = parseTime(expiry)
	return ses, true, nil
}

// Touch extends an existing row's expiry and reports whether it updated
// anything. It never shortens a row: the UPDATE only applies when the stored
// expiry is earlier than the new one, so parallel requests that all pass the
// renewal threshold cannot race each other into a shorter window. A missing row
// returns (false, nil); the caller must not re-send a cookie for it.
func (s *Store) Touch(ctx context.Context, tokenHash string, expiresAt time.Time) (bool, error) {
	ts := formatTime(expiresAt)
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE token_hash = ? AND expires_at < ?`,
		ts, tokenHash, ts)
	if err != nil {
		return false, fmt.Errorf("touch session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("touch session: rows affected: %w", err)
	}
	return n > 0, nil
}

// Delete removes one row. Missing rows are not an error (logout is idempotent).
func (s *Store) Delete(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteByUserID removes every session of a user.
func (s *Store) DeleteByUserID(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete sessions for user %d: %w", userID, err)
	}
	return nil
}

// DeleteExpired removes rows expiring at or before the given time and reports
// how many were removed. The boundary matches Validate (a session with
// now >= ExpiresAt is expired), and the fixed-width text format keeps the
// lexicographic comparison correct.
func (s *Store) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: rows affected: %w", err)
	}
	return n, nil
}

// formatTime renders a timestamp in UTC at second precision. A fixed-width
// layout keeps the lexicographic comparison DeleteExpired performs on the text
// column correct.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// parseTime decodes a stored timestamp. It accepts this store's RFC3339 output
// and SQLite's default `datetime('now')` layout, so a hand-written row still
// parses instead of being read as the zero time. Ordering is only guaranteed
// for rows this store writes (fixed-width UTC RFC3339, second precision); a
// row in the SQLite layout can mis-order in the SQL comparisons, which fails
// closed (the row is treated as expired/prunable). An unparsable value becomes
// the zero time — also fail closed.
func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	return time.Time{}
}
