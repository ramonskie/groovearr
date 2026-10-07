// Package user provides the account model for multi-user role support.
//
// It owns the pure data types (User, Role) and the Store persistence
// contract. Concrete implementations live in a subpackage
// (internal/user/sqlite) and wrap failures with fmt.Errorf context per
// AGENTS §10; this package root only declares types and the contract.
//
// Passwords are always stored hashed; the plaintext never reaches this
// package. Hashing and verification reuse config.HashPassword and
// config.CheckPassword.
package user

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors returned by the guard-aware Store methods. They let the API
// layer map a persistence outcome to an HTTP status without asserting a
// concrete store type (AGENTS §3); callers match them with errors.Is.
var (
	// ErrLastAdmin is returned when a delete or update would leave the
	// system with zero active (enabled) admins.
	ErrLastAdmin = errors.New("cannot remove the last active admin")
	// ErrDuplicateUsername is returned when a create or rename collides
	// with an existing username, case-insensitively.
	ErrDuplicateUsername = errors.New("username already exists")
	// ErrUserNotFound is returned by the guard-aware methods when no user
	// with the given ID exists. (Plain lookups keep the (nil, nil)
	// not-found contract.)
	ErrUserNotFound = errors.New("user not found")
)

// Role is the access level granted to a user account. It is the sole
// authorization boundary: admins may manage the settings surface and
// other users, while regular users get the shared library, playlists,
// search, discovery, and download queue.
type Role string

const (
	// RoleAdmin grants access to the admin-only settings surface and
	// user management.
	RoleAdmin Role = "admin"
	// RoleUser grants access to the shared authenticated feature set
	// (library, playlists, search, discover, downloads) without
	// administrative surfaces.
	RoleUser Role = "user"
)

// User is a persisted account record. PasswordHash holds a bcrypt hash
// produced by config.HashPassword; it is never serialized (json:"-") so
// it cannot leak to API clients or logs.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UserPatch carries only the fields a caller intends to change; nil = unchanged.
// It is the input to the guarded update so a handler never has to write back a
// full pre-transaction snapshot, which is what made concurrent updates clobber
// each other's fields.
type UserPatch struct {
	Role         *Role
	Disabled     *bool
	PasswordHash *string
}

// Store is the interface for user-account persistence.
//
// Lookup methods return (nil, nil) when no matching user exists, never a
// "not found" error (AGENTS §10). Implementations must treat usernames
// case-insensitively for uniqueness and lookup.
type Store interface {
	// CreateUser inserts a new user and returns its assigned ID.
	CreateUser(ctx context.Context, u *User) (int64, error)

	// GetUser returns the user with the given ID, or (nil, nil) when no
	// such user exists.
	GetUser(ctx context.Context, id int64) (*User, error)

	// GetUserByUsername returns the user with the given username using a
	// case-insensitive match, or (nil, nil) when no such user exists.
	GetUserByUsername(ctx context.Context, username string) (*User, error)

	// ListUsers returns all users ordered by username.
	ListUsers(ctx context.Context) ([]User, error)

	// UpdateUser persists mutable fields (username, password hash, role,
	// disabled) for an existing user and refreshes updated_at.
	UpdateUser(ctx context.Context, u *User) error

	// DeleteUser removes the user with the given ID.
	DeleteUser(ctx context.Context, id int64) error

	// UpdateUserGuarded applies a delta patch to an existing user while
	// enforcing the last-admin invariant. Only the non-nil patch fields
	// are written (plus updated_at), so a concurrent update cannot revert
	// a field the caller did not intend to touch. If the patch would
	// demote or disable an active admin and no other active admin
	// remains, it makes no change and returns ErrLastAdmin. A missing
	// user returns ErrUserNotFound. An all-nil patch is a no-op: the
	// current row is returned unchanged and updated_at is not bumped.
	//
	// Implementations MUST load the row, apply the patch, run the
	// active-admin check, and write inside a single transaction so a
	// concurrent demotion cannot race past the guard. The updated row is
	// returned so callers do not need a second, racy read-back.
	UpdateUserGuarded(ctx context.Context, id int64, patch UserPatch) (*User, error)

	// DeleteUserGuarded removes the user with the given ID while enforcing
	// the last-admin invariant. If the target is an active admin and it is
	// the last one, it makes no change and returns ErrLastAdmin. A missing
	// user returns ErrUserNotFound. Implementations MUST run the existence
	// check, the active-admin check, and the delete in a single transaction
	// so a concurrent demotion cannot race past the guard.
	DeleteUserGuarded(ctx context.Context, id int64) error

	// CountUsers returns the total number of user accounts. It backs the
	// last-admin guard and first-run bootstrap checks.
	CountUsers(ctx context.Context) (int, error)
}
