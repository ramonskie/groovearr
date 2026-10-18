package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/session"
)

// memSessionStore is an in-memory session.Store for the API tests. The
// production store is SQLite (internal/session/sqlite); these tests only need
// the contract, so no database is opened.
type memSessionStore struct {
	mu   sync.Mutex
	rows map[string]session.Session
}

func (m *memSessionStore) Create(_ context.Context, s session.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = make(map[string]session.Session)
	}
	m.rows[s.TokenHash] = s
	return nil
}

func (m *memSessionStore) Get(_ context.Context, tokenHash string) (session.Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[tokenHash]
	return s, ok, nil
}

func (m *memSessionStore) Touch(_ context.Context, tokenHash string, expiresAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.rows[tokenHash]
	if !ok || !expiresAt.After(s.ExpiresAt) {
		return false, nil
	}
	s.ExpiresAt = expiresAt
	m.rows[tokenHash] = s
	return true, nil
}

func (m *memSessionStore) Delete(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, tokenHash)
	return nil
}

func (m *memSessionStore) DeleteByUserID(_ context.Context, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, s := range m.rows {
		if s.UserID == userID {
			delete(m.rows, h)
		}
	}
	return nil
}

func (m *memSessionStore) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for h, s := range m.rows {
		if !s.ExpiresAt.After(before) {
			delete(m.rows, h)
			n++
		}
	}
	return n, nil
}

// newTestSessions returns a session service over a fresh in-memory store. The
// reaper stops with the test.
func newTestSessions(t *testing.T) *session.Service {
	t.Helper()
	svc := session.NewService(&memSessionStore{}, testAPILogger())
	t.Cleanup(svc.Shutdown)
	return svc
}
