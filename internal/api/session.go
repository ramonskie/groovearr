package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/user"
)

// session holds an authenticated user session. It carries the identity fields
// that every authenticated request needs (UserID, Username, Role) plus the
// expiry used by the store's validation and reaper.
type session struct {
	UserID    int64
	Username  string
	Role      user.Role
	ExpiresAt time.Time
}

// sessionStore is an in-memory session store with expiry.
type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]session
	done     chan struct{}
}

func newSessionStore() *sessionStore {
	s := &sessionStore{
		sessions: make(map[string]session),
		done:     make(chan struct{}),
	}
	go s.reapLoop()
	return s
}

// Create generates a new session token for the given user and stores the
// identity fields carried by every subsequent request (UserID, Username,
// Role). Returns the token string (for the cookie value), its expiry time, and
// an error if the token could not be generated (crypto/rand failure). The
// caller must not issue a session cookie on error.
func (s *sessionStore) Create(u user.User) (token string, expires time.Time, err error) {
	token, err = newSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires = time.Now().Add(7 * 24 * time.Hour) // 7 days, matching Sonarr
	s.mu.Lock()
	s.sessions[token] = session{
		UserID:    u.ID,
		Username:  u.Username,
		Role:      u.Role,
		ExpiresAt: expires,
	}
	s.mu.Unlock()
	return token, expires, nil
}

// Validate checks a token and returns the full session if it is valid.
// Returns the zero session and false if the token is unknown or expired.
// Expired sessions are revoked on the spot.
func (s *sessionStore) Validate(token string) (session, bool) {
	s.mu.RLock()
	ses, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return session{}, false
	}
	if time.Now().After(ses.ExpiresAt) {
		s.Delete(token)
		return session{}, false
	}
	return ses, true
}

// Delete removes a session token (for logout).
func (s *sessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// DeleteByUserID removes every session belonging to the given user. It backs
// session invalidation when a user is disabled, deleted, or changes password.
func (s *sessionStore) DeleteByUserID(userID int64) {
	s.mu.Lock()
	for token, ses := range s.sessions {
		if ses.UserID == userID {
			delete(s.sessions, token)
		}
	}
	s.mu.Unlock()
}

// Shutdown stops the background reaper goroutine. Safe to call multiple times.
func (s *sessionStore) Shutdown() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
	}
}

// reapLoop periodically removes expired sessions.
func (s *sessionStore) reapLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for token, ses := range s.sessions {
				if now.After(ses.ExpiresAt) {
					delete(s.sessions, token)
				}
			}
			s.mu.Unlock()
		}
	}
}

// newSessionToken returns a 256-bit random hex token. It returns an error
// instead of panicking on crypto/rand failure: panic is reserved for fatal
// startup errors in main() (AGENTS §10), and a runtime entropy failure is a
// request-scoped failure the caller can surface as a 500.
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
