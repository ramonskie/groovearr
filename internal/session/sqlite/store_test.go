package sqlite

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ramonskie/groovearr/internal/session"
	"github.com/ramonskie/groovearr/internal/user"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestDB opens a real SQLite database in a temp dir, mirroring the sibling
// sqlite store tests. No network is involved.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	// Mirror the production connection setup (library/sqlite): WAL, a busy
	// timeout, and a single connection. Without these the reaper and the test
	// goroutine open separate connections and collide with SQLITE_BUSY.
	dsn := filepath.Join(t.TempDir(), "sessions.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestStore(t *testing.T, db *sql.DB) *Store {
	t.Helper()
	s, err := New(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

var baseTime = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func fixtureRow() session.Session {
	return session.Session{
		TokenHash: session.HashToken("tok"),
		UserID:    7,
		Username:  "alice",
		Role:      user.RoleAdmin,
		CreatedAt: baseTime,
		ExpiresAt: baseTime.Add(session.TTL),
	}
}

func TestNewIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	newTestStore(t, db)
	newTestStore(t, db) // must not fail on an existing table
}

func TestCreateGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, newTestDB(t))
	want := fixtureRow()

	if err := s.Create(ctx, want); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, ok, err := s.Get(ctx, want.TokenHash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get did not find the inserted row")
	}
	if got.TokenHash != want.TokenHash || got.UserID != want.UserID ||
		got.Username != want.Username || got.Role != want.Role {
		t.Fatalf("Get identity = %+v, want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("Get times = %v/%v, want %v/%v", got.CreatedAt, got.ExpiresAt, want.CreatedAt, want.ExpiresAt)
	}
}

func TestGetMissingIsNotAnError(t *testing.T) {
	_, ok, err := newTestStore(t, newTestDB(t)).Get(context.Background(), "absent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("Get reported a row for an absent token")
	}
}

func TestTouchReplacesExpiry(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, newTestDB(t))
	row := fixtureRow()
	if err := s.Create(ctx, row); err != nil {
		t.Fatalf("Create: %v", err)
	}

	extended := baseTime.Add(2 * session.TTL)
	if _, err := s.Touch(ctx, row.TokenHash, extended); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _, err := s.Get(ctx, row.TokenHash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.ExpiresAt.Equal(extended) {
		t.Fatalf("expiry = %v, want %v", got.ExpiresAt, extended)
	}
	if !got.CreatedAt.Equal(row.CreatedAt) {
		t.Fatalf("Touch changed created_at: %v", got.CreatedAt)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, newTestDB(t))
	row := fixtureRow()
	if err := s.Create(ctx, row); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Delete(ctx, row.TokenHash); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get(ctx, row.TokenHash); ok {
		t.Fatal("row survived Delete")
	}
	if err := s.Delete(ctx, "absent"); err != nil {
		t.Fatalf("Delete(absent) should be a no-op, got %v", err)
	}
}

func TestDeleteByUserID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, newTestDB(t))
	seeds := []struct {
		userID int64
		token  string
	}{{1, "a"}, {1, "b"}, {2, "c"}}
	for _, seed := range seeds {
		row := fixtureRow()
		row.UserID = seed.userID
		row.TokenHash = session.HashToken(seed.token)
		if err := s.Create(ctx, row); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	if err := s.DeleteByUserID(ctx, 1); err != nil {
		t.Fatalf("DeleteByUserID: %v", err)
	}
	if n := countRows(t, s.db); n != 1 {
		t.Fatalf("rows = %d, want 1 (the other user's)", n)
	}
}

func TestDeleteExpired(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, newTestDB(t))

	live := fixtureRow()
	live.TokenHash = session.HashToken("live")
	live.ExpiresAt = baseTime.Add(time.Hour)
	dead := fixtureRow()
	dead.TokenHash = session.HashToken("dead")
	dead.ExpiresAt = baseTime.Add(-time.Hour)
	for _, row := range []session.Session{live, dead} {
		if err := s.Create(ctx, row); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	n, err := s.DeleteExpired(ctx, baseTime)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}
	if _, ok, _ := s.Get(ctx, live.TokenHash); !ok {
		t.Fatal("DeleteExpired removed a live session")
	}
	if _, ok, _ := s.Get(ctx, dead.TokenHash); ok {
		t.Fatal("DeleteExpired kept an expired session")
	}
}

// TestOnlyHashPersisted proves the raw cookie value never reaches the table.
func TestOnlyHashPersisted(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := newTestStore(t, db)
	svc := session.NewService(store, testLogger())
	t.Cleanup(svc.Shutdown)

	token, _, err := svc.Create(ctx, user.User{ID: 1, Username: "alice", Role: user.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatalf("select token_hash: %v", err)
	}
	if stored == token {
		t.Fatal("raw session token was persisted")
	}
	if stored != session.HashToken(token) {
		t.Fatalf("stored hash = %q, want %q", stored, session.HashToken(token))
	}
}

// TestSessionSurvivesRestart is the regression guard for the whole change: a
// session created before a "restart" (a fresh store + service over the same
// database) must still validate afterwards.
func TestSessionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	beforeStore := newTestStore(t, db)
	before := session.NewService(beforeStore, testLogger())
	token, _, err := before.Create(ctx, user.User{ID: 4, Username: "bob", Role: user.RoleUser})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Simulate a renewal before the restart: the extended expiry must survive
	// too, not just the row.
	renewed := time.Now().Add(60 * 24 * time.Hour).Truncate(time.Second)
	if _, err := beforeStore.Touch(ctx, session.HashToken(token), renewed); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	before.Shutdown()

	after := session.NewService(newTestStore(t, db), testLogger())
	t.Cleanup(after.Shutdown)

	ses, ok, err := after.Validate(ctx, token)
	if err != nil {
		t.Fatalf("Validate after restart: %v", err)
	}
	if !ok {
		t.Fatal("session did not survive a restart")
	}
	if ses.UserID != 4 || ses.Username != "bob" || ses.Role != user.RoleUser {
		t.Fatalf("identity after restart = %+v, want 4/bob/user", ses)
	}
	if !ses.ExpiresAt.Equal(renewed) {
		t.Fatalf("expiry after restart = %v, want the renewed %v", ses.ExpiresAt, renewed)
	}
}

// TestTouchIsMonotonic proves a renewal can never shorten a live window: a
// parallel request that computed an earlier expiry must not overwrite a later
// one.
func TestTouchIsMonotonic(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, newTestDB(t))
	row := fixtureRow()
	row.ExpiresAt = baseTime.Add(2 * session.TTL)
	if err := s.Create(ctx, row); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := s.Touch(ctx, row.TokenHash, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	got, _, err := s.Get(ctx, row.TokenHash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.ExpiresAt.Equal(row.ExpiresAt) {
		t.Fatalf("expiry = %v, want the later %v", got.ExpiresAt, row.ExpiresAt)
	}
}

// TestUnparsableExpiryFailsClosed guards the parse fallback: a corrupt timestamp
// must not extend a session's life.
func TestUnparsableExpiryFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	store := newTestStore(t, db)
	if _, err := db.Exec(
		`INSERT INTO sessions (token_hash, user_id, username, role, created_at, expires_at)
		 VALUES (?, 1, 'alice', 'admin', 'not-a-time', 'not-a-time')`,
		session.HashToken("tok")); err != nil {
		t.Fatalf("insert corrupt row: %v", err)
	}

	svc := session.NewService(store, testLogger())
	t.Cleanup(svc.Shutdown)

	if _, ok, err := svc.Validate(ctx, "tok"); err != nil || ok {
		t.Fatalf("Validate = ok:%v err:%v, want fail-closed rejection", ok, err)
	}
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}
