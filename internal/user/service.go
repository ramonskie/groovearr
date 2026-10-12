package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ramonskie/groovearr/internal/config"
)

// bootstrapAuthMethod is the only auth method that seeds a first admin.
// "none" and "" mean authentication is disabled (every request is treated as
// admin), so there is no account to create; any other value is not a
// credential-based login and is likewise skipped.
const bootstrapAuthMethod = "forms"

// EnsureBootstrapAdmin promotes the existing single-user config credentials to
// the first admin account on a fresh install.
//
// It seeds exactly one admin when:
//   - auth.method is "forms" (the only credential-based login), and
//   - a non-empty username and password are configured, and
//   - the user store is empty.
//
// It is a no-op in every other case, including each start after the first, so
// it is safe to call unconditionally at startup. cfg is passed by value: the
// caller owns the live config and this function never caches or mutates it.
// The configured password is already bcrypt-hashed by config persistence, so it
// is stored verbatim as the PasswordHash; a shape guard below fails loudly if a
// plaintext value ever reaches here instead.
//
// log is the app logger; nil falls back to slog.Default() so a miswired caller
// degrades to the default logger rather than panicking.
func EnsureBootstrapAdmin(ctx context.Context, store Store, cfg config.Config, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	username := strings.TrimSpace(cfg.Auth.Username)
	if cfg.Auth.Method != bootstrapAuthMethod || username == "" || cfg.Auth.Password == "" {
		return nil
	}
	if store == nil {
		return fmt.Errorf("bootstrap admin: nil store")
	}

	n, err := store.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap admin: count users: %w", err)
	}
	if n > 0 {
		return nil
	}

	// Defensive guard: the stored value must already be a bcrypt hash. bcrypt
	// hashes always start with "$2" ($2a/$2b/$2y). If a plaintext password ever
	// reaches here, refuse to store it rather than silently persisting a
	// credential-shaped value that CheckPassword could never verify.
	if !strings.HasPrefix(cfg.Auth.Password, "$2") {
		return fmt.Errorf("bootstrap admin: configured password for %q is not a bcrypt hash", username)
	}

	if _, err := store.CreateUser(ctx, &User{
		Username:     username,
		PasswordHash: cfg.Auth.Password, // already bcrypt-hashed by config persistence
		Role:         RoleAdmin,
	}); err != nil {
		return fmt.Errorf("bootstrap admin: create %q: %w", username, err)
	}

	log.Info("bootstrap admin seeded", "username", username, "component", "user")
	return nil
}

// Sentinel errors returned by EnsureFormsReady. The API layer maps these to a
// 400 (the operator can act on them); every other error it surfaces as a 500.
var (
	// ErrFirstAdminRequired means forms was being enabled against an empty user
	// store and the request did not carry a first-admin username and a new
	// plaintext password.
	ErrFirstAdminRequired = errors.New("cannot enable forms authentication: set a first admin username and password")
	// ErrWeakFirstAdminPassword means the first-admin password failed the same
	// boundary POST /api/users enforces (see ValidatePassword). The message is
	// built from the shared bounds so it cannot drift from them.
	ErrWeakFirstAdminPassword = fmt.Errorf(
		"cannot enable forms authentication: first admin password must be at least %d characters and at most %d bytes",
		MinPasswordRunes, MaxPasswordBytes,
	)
	// ErrNoEnabledAdmin means enabling forms would leave no account able to
	// authenticate as an admin.
	ErrNoEnabledAdmin = errors.New("cannot enable forms authentication: no enabled admin account")
)

// First-admin password bounds are the shared account boundary (see
// password.go); EnsureFormsReady returns ErrWeakFirstAdminPassword rather than
// the boundary's own error so the API layer can word the refusal in the
// enable-forms context.

// FormsRequest is the credential-relevant outcome of a config save that turns
// forms authentication on. It is decoupled from config.Config so the readiness
// rule is testable without a full config merge.
type FormsRequest struct {
	// Username is the effective first-admin username (the request value, else
	// the existing config value).
	Username string
	// NewPassword is the plaintext password supplied in this request, or "" when
	// the request reused the stored hash. A stored hash may be a credential the
	// operator no longer knows, so it is never seeded as a new account.
	NewPassword string
}

// EnsureFormsReady guarantees that turning on forms authentication leaves a
// usable admin login, returning an actionable error when it would not:
//
//   - an empty user store is seeded with a first admin, but only from a
//     password supplied in this request. A preserved config hash could be one
//     the operator has forgotten, and seeding it would lock them out of the
//     admin-only settings this guard exists to protect (ErrFirstAdminRequired);
//   - a store holding accounts but no enabled admin is refused
//     (ErrNoEnabledAdmin): a store of regular users alone can never reach
//     settings to grant admin;
//   - a store with an enabled admin passes unchanged.
//
// It performs user-store I/O, so callers must invoke it outside the config
// write lock. log may be nil (falls back to slog.Default()).
func EnsureFormsReady(ctx context.Context, store Store, req FormsRequest, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	if store == nil {
		return fmt.Errorf("forms readiness: nil store")
	}

	users, err := store.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("forms readiness: list users: %w", err)
	}

	if len(users) == 0 {
		username := strings.TrimSpace(req.Username)
		if username == "" || req.NewPassword == "" {
			return ErrFirstAdminRequired
		}
		if err := ValidatePassword(req.NewPassword); err != nil {
			return ErrWeakFirstAdminPassword
		}
		hash, err := config.HashPassword(req.NewPassword)
		if err != nil {
			// Defensive backstop: ValidatePassword already rejects the only input
			// bcrypt refuses (>72 bytes), so this is unreachable in practice.
			return ErrWeakFirstAdminPassword
		}
		if _, cerr := store.CreateUser(ctx, &User{
			Username:     username,
			PasswordHash: hash,
			Role:         RoleAdmin,
		}); cerr != nil {
			// A concurrent request may have created that username first. Do not
			// assume the colliding row is a usable admin (it could be a regular
			// user created via POST /api/users): re-read and let the scan below
			// decide, so the switch is refused rather than locking the operator
			// out.
			if !errors.Is(cerr, ErrDuplicateUsername) {
				return fmt.Errorf("forms readiness: seed first admin: %w", cerr)
			}
			users, err = store.ListUsers(ctx)
			if err != nil {
				return fmt.Errorf("forms readiness: verify users: %w", err)
			}
		} else {
			log.Info("forms first admin seeded", "username", username, "component", "user")
			return nil
		}
	}

	for _, u := range users {
		// Only a bcrypt hash can authenticate (config.CheckPassword), so a row
		// with anything else does not count as a usable admin.
		if u.Role == RoleAdmin && !u.Disabled && strings.HasPrefix(u.PasswordHash, "$2") {
			return nil
		}
	}
	return ErrNoEnabledAdmin
}
