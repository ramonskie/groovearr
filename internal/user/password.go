package user

import (
	"fmt"
	"unicode/utf8"
)

// Account-password boundary. It lives in the user domain so every path that
// accepts a password — POST /api/users, PATCH password changes, and the
// first-admin seed when Forms is enabled — enforces exactly one rule.
//
// The minimum is counted in runes (characters the person typed); the maximum in
// bytes, because bcrypt.GenerateFromPassword rejects input longer than 72 bytes.
const (
	// MinPasswordRunes is the shortest accepted password, in runes.
	MinPasswordRunes = 8
	// MaxPasswordBytes is bcrypt's hard input limit, in bytes.
	MaxPasswordBytes = 72
)

// ValidatePassword enforces the account-password boundary. The error message is
// the stable, user-facing wire value; callers surface it as a 400.
func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordRunes {
		return fmt.Errorf("password must be at least %d characters", MinPasswordRunes)
	}
	if len(pw) > MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
	}
	return nil
}
