// Package session provides persistent, server-side authentication sessions.
//
// A session is an opaque random token issued to the browser as the
// groovearr_sid cookie. The token itself is never stored: the store keeps only
// its SHA-256 hash, so a leaked database or backup cannot be replayed as a
// cookie. Rows survive a process restart — that is the point of the package —
// and a session slides forward on activity within a 30-day idle window, so a
// returning visitor is not asked to log in again.
//
// The package root declares the data type and the persistence contract; the
// SQLite implementation lives in the sqlite subpackage and the expiry/renewal
// policy in Service (service.go). This mirrors the user package layout
// (AGENTS §3).
package session

import (
	"context"
	"time"

	"github.com/ramonskie/groovearr/internal/user"
)

// Session is one authenticated session as persisted.
//
// Identity is denormalized into the row on purpose: every authenticated request
// must resolve username and role without a second lookup, and a
// security-relevant account change retires the rows instead of mutating them
// (see api.invalidateUserSessions). TokenHash is the SHA-256 hex digest of the
// cookie value; the raw token is never persisted.
type Session struct {
	TokenHash string
	UserID    int64
	Username  string
	Role      user.Role
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Store is the persistence contract for sessions.
//
// Get returns (zero Session, false, nil) when no row matches — a missing
// session is not an error (AGENTS §10). Every method takes a context so the
// caller's request deadline applies; implementations must not keep state
// between calls beyond the backing database.
type Store interface {
	// Create inserts s. A duplicate TokenHash is a programming error and
	// surfaces as a database error.
	Create(ctx context.Context, s Session) error
	// Get returns the session for the given token hash, or false when absent.
	Get(ctx context.Context, tokenHash string) (Session, bool, error)
	// Touch extends the expiry of an existing row, never shortening it, and
	// reports whether a row was actually updated (false means it vanished
	// between Validate and Renew — e.g. a logout raced the request). A missing
	// row is not an error: the caller must not re-send a cookie for it.
	Touch(ctx context.Context, tokenHash string, expiresAt time.Time) (bool, error)
	// Delete removes one session (logout).
	Delete(ctx context.Context, tokenHash string) error
	// DeleteByUserID removes every session belonging to a user (disable,
	// password change, role change, deletion).
	DeleteByUserID(ctx context.Context, userID int64) error
	// DeleteExpired removes rows whose expiry is before the given time and
	// returns how many were removed.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}
