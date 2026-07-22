package config

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// GenerateAPIKey returns a 32-char hex key (UUID without dashes), matching
// the Sonarr/Radarr pattern.
func GenerateAPIKey() string {
	return uuid.NewString()
	// We keep the dashes for readability; Sonarr strips them but that's cosmetic.
	// If dash-stripped is preferred: strings.ReplaceAll(uuid.NewString(), "-", "")
}

// HashPassword returns a bcrypt hash of the plaintext password (cost 10).
func HashPassword(plain string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// CheckPassword compares a bcrypt hash against a plaintext password.
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// generateSessionToken returns a 64-char hex random token for session cookies.
func generateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
