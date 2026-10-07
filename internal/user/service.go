package user

import (
	"context"
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
