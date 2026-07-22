package api

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// session holds an authenticated user session.
type session struct {
	Username  string
	ExpiresAt time.Time
}

// sessionStore is an in-memory session store with expiry.
type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]session
}

func newSessionStore() *sessionStore {
	s := &sessionStore{sessions: make(map[string]session)}
	go s.reapLoop()
	return s
}

// Create generates a new session token for the given username.
// Returns the token string (for the cookie value) and its expiry time.
func (s *sessionStore) Create(username string) (token string, expires time.Time) {
	token = newSessionToken()
	expires = time.Now().Add(7 * 24 * time.Hour) // 7 days, matching Sonarr
	s.mu.Lock()
	s.sessions[token] = session{Username: username, ExpiresAt: expires}
	s.mu.Unlock()
	return
}

// Validate checks a token and returns the username if the session is valid.
// Returns empty string and false if the token is unknown or expired.
func (s *sessionStore) Validate(token string) (string, bool) {
	s.mu.RLock()
	ses, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(ses.ExpiresAt) {
		s.Delete(token)
		return "", false
	}
	return ses.Username, true
}

// Delete removes a session token (for logout).
func (s *sessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// reapLoop periodically removes expired sessions.
func (s *sessionStore) reapLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
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

func newSessionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
