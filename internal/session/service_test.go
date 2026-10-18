package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/user"
)

// testClock is a fixed reference time; tests shift it explicitly.
var testClock = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// fakeStore is an in-memory Store that records the calls made against it and can
// fail any operation with a configured error.
type fakeStore struct {
	rows  map[string]Session
	calls []string
	touch map[string]time.Time
	err   error // returned by every method when set
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make(map[string]Session), touch: make(map[string]time.Time)}
}

func (f *fakeStore) record(op string) error {
	f.calls = append(f.calls, op)
	return f.err
}

func (f *fakeStore) Create(_ context.Context, s Session) error {
	if err := f.record("create"); err != nil {
		return err
	}
	f.rows[s.TokenHash] = s
	return nil
}

func (f *fakeStore) Get(_ context.Context, tokenHash string) (Session, bool, error) {
	if err := f.record("get"); err != nil {
		return Session{}, false, err
	}
	s, ok := f.rows[tokenHash]
	return s, ok, nil
}

func (f *fakeStore) Touch(_ context.Context, tokenHash string, expiresAt time.Time) (bool, error) {
	if err := f.record("touch"); err != nil {
		return false, err
	}
	f.touch[tokenHash] = expiresAt
	s, ok := f.rows[tokenHash]
	if !ok {
		return false, nil
	}
	s.ExpiresAt = expiresAt
	f.rows[tokenHash] = s
	return true, nil
}

func (f *fakeStore) Delete(_ context.Context, tokenHash string) error {
	if err := f.record("delete"); err != nil {
		return err
	}
	delete(f.rows, tokenHash)
	return nil
}

func (f *fakeStore) DeleteByUserID(_ context.Context, userID int64) error {
	if err := f.record("delete_by_user"); err != nil {
		return err
	}
	for h, s := range f.rows {
		if s.UserID == userID {
			delete(f.rows, h)
		}
	}
	return nil
}

func (f *fakeStore) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	if err := f.record("delete_expired"); err != nil {
		return 0, err
	}
	var n int64
	for h, s := range f.rows {
		if !s.ExpiresAt.After(before) {
			delete(f.rows, h)
			n++
		}
	}
	return n, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestService builds a Service with a fixed clock. It deliberately does not
// start the reaper goroutine: the policy tests are deterministic and Prune is
// exercised directly.
func newTestService(store Store) *Service {
	return &Service{store: store, log: testLogger(), now: func() time.Time { return testClock }, done: make(chan struct{})}
}

func TestCreate(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	token, expires, err := svc.Create(context.Background(), user.User{ID: 7, Username: "alice", Role: user.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(token) != 64 {
		t.Fatalf("token len = %d, want 64 hex chars", len(token))
	}
	if want := testClock.Add(TTL); !expires.Equal(want) {
		t.Fatalf("expires = %v, want %v", expires, want)
	}
	if len(store.rows) != 1 {
		t.Fatalf("stored rows = %d, want 1", len(store.rows))
	}
	row, ok := store.rows[HashToken(token)]
	if !ok {
		t.Fatal("row is not keyed by the token hash")
	}
	if _, rawStored := store.rows[token]; rawStored {
		t.Fatal("raw token was stored; only the hash may be persisted")
	}
	if row.UserID != 7 || row.Username != "alice" || row.Role != user.RoleAdmin {
		t.Fatalf("stored identity = %+v, want 7/alice/admin", row)
	}
	if !row.CreatedAt.Equal(testClock) {
		t.Fatalf("created_at = %v, want %v", row.CreatedAt, testClock)
	}
}

func TestCreateStoreErrorIssuesNoSession(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("db down")
	svc := newTestService(store)

	if _, _, err := svc.Create(context.Background(), user.User{ID: 1}); err == nil {
		t.Fatal("Create returned nil error when the store failed")
	}
}

func TestValidate(t *testing.T) {
	valid := Session{
		TokenHash: HashToken("tok"),
		UserID:    3,
		Username:  "bob",
		Role:      user.RoleUser,
		CreatedAt: testClock.Add(-time.Hour),
		ExpiresAt: testClock.Add(time.Hour),
	}

	tests := []struct {
		name      string
		seed      *Session
		token     string
		storeErr  bool
		wantOK    bool
		wantErr   bool
		wantCalls []string
	}{
		{
			name:      "empty token is not authenticated without touching the store",
			token:     "",
			wantOK:    false,
			wantCalls: []string{},
		},
		{
			name:      "unknown token is not authenticated",
			token:     "nope",
			wantOK:    false,
			wantCalls: []string{"get"},
		},
		{
			name:      "valid session resolves the stored identity",
			seed:      &valid,
			token:     "tok",
			wantOK:    true,
			wantCalls: []string{"get"},
		},
		{
			name:      "expired session is rejected and its row dropped",
			seed:      &Session{TokenHash: HashToken("old"), UserID: 4, ExpiresAt: testClock},
			token:     "old",
			wantOK:    false,
			wantCalls: []string{"get", "delete"},
		},
		{
			name:      "store failure is an error, never an identity (fail closed)",
			token:     "tok",
			storeErr:  true,
			wantOK:    false,
			wantErr:   true,
			wantCalls: []string{"get"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			if tt.seed != nil {
				store.rows[tt.seed.TokenHash] = *tt.seed
			}
			if tt.storeErr {
				store.err = errors.New("db down")
			}
			svc := newTestService(store)

			ses, ok, err := svc.Validate(context.Background(), tt.token)

			if tt.wantErr && err == nil {
				t.Fatal("Validate returned nil error on store failure")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && (ses.UserID != valid.UserID || ses.Username != valid.Username || ses.Role != valid.Role) {
				t.Fatalf("identity = %+v, want %+v", ses, valid)
			}
			if !equalStrings(store.calls, tt.wantCalls) {
				t.Fatalf("store calls = %v, want %v", store.calls, tt.wantCalls)
			}
		})
	}
}

func TestNeedsRenewalBoundary(t *testing.T) {
	svc := newTestService(newFakeStore())

	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{name: "fresh session does not renew", expiresAt: testClock.Add(TTL), want: false},
		{name: "more than half left does not renew", expiresAt: testClock.Add(RenewAfter + time.Second), want: false},
		{name: "exactly half left does not renew", expiresAt: testClock.Add(RenewAfter), want: false},
		{name: "past half renews", expiresAt: testClock.Add(RenewAfter - time.Second), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := svc.NeedsRenewal(Session{ExpiresAt: tt.expiresAt}); got != tt.want {
				t.Fatalf("NeedsRenewal(%v) = %v, want %v", tt.expiresAt, got, tt.want)
			}
		})
	}
}

func TestRenewExtendsToFullWindow(t *testing.T) {
	store := newFakeStore()
	store.rows[HashToken("tok")] = Session{TokenHash: HashToken("tok"), ExpiresAt: testClock}
	svc := newTestService(store)

	expires, renewed, err := svc.Renew(context.Background(), "tok")
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if !renewed {
		t.Fatal("Renew reported no update for an existing row")
	}
	if want := testClock.Add(TTL); !expires.Equal(want) {
		t.Fatalf("expires = %v, want %v", expires, want)
	}
	if got := store.touch[HashToken("tok")]; !got.Equal(expires) {
		t.Fatalf("Touch expiry = %v, want %v", got, expires)
	}
}

// TestRenewMissingRowReportsNotRenewed keeps a vanished session from being
// re-cookie'd: the caller must not re-send a token whose row is gone.
func TestRenewMissingRowReportsNotRenewed(t *testing.T) {
	svc := newTestService(newFakeStore())

	if _, renewed, err := svc.Renew(context.Background(), "gone"); err != nil || renewed {
		t.Fatalf("Renew = renewed:%v err:%v, want false/nil", renewed, err)
	}
}

func TestRenewStoreError(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("db down")
	svc := newTestService(store)

	if _, _, err := svc.Renew(context.Background(), "tok"); err == nil {
		t.Fatal("Renew returned nil error when the store failed")
	}
}

func TestDelete(t *testing.T) {
	store := newFakeStore()
	store.rows[HashToken("tok")] = Session{TokenHash: HashToken("tok")}
	svc := newTestService(store)

	if err := svc.Delete(context.Background(), "tok"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(store.rows) != 0 {
		t.Fatal("Delete left the row in place")
	}
	if err := svc.Delete(context.Background(), ""); err != nil {
		t.Fatalf("Delete(empty) should be a no-op, got %v", err)
	}
}

func TestDeleteByUserID(t *testing.T) {
	store := newFakeStore()
	store.rows["a"] = Session{TokenHash: "a", UserID: 1}
	store.rows["b"] = Session{TokenHash: "b", UserID: 1}
	store.rows["c"] = Session{TokenHash: "c", UserID: 2}
	svc := newTestService(store)

	if err := svc.DeleteByUserID(context.Background(), 1); err != nil {
		t.Fatalf("DeleteByUserID: %v", err)
	}
	if len(store.rows) != 1 || store.rows["c"].UserID != 2 {
		t.Fatalf("rows after delete = %+v, want only user 2's", store.rows)
	}
}

func TestPruneRemovesExpiredRows(t *testing.T) {
	store := newFakeStore()
	store.rows["live"] = Session{TokenHash: "live", ExpiresAt: testClock.Add(time.Hour)}
	store.rows["dead"] = Session{TokenHash: "dead", ExpiresAt: testClock.Add(-time.Hour)}
	svc := newTestService(store)

	n, err := svc.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	if _, ok := store.rows["live"]; !ok {
		t.Fatal("Prune removed a live session")
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	svc := newTestService(newFakeStore())
	svc.Shutdown()
	svc.Shutdown()
}

func TestHashToken(t *testing.T) {
	got := HashToken("token")
	if got != HashToken("token") {
		t.Fatal("HashToken is not deterministic")
	}
	if got == "token" {
		t.Fatal("HashToken returned the raw token")
	}
	if len(got) != 64 {
		t.Fatalf("HashToken len = %d, want 64", len(got))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
