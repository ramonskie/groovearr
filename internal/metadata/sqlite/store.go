// Package sqlite persists metadata rate-limit state (cooldowns + events) so a
// long server-requested backoff survives a restart and is observable.
package sqlite

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/ramonskie/groovearr/internal/metadata"
)

// Store persists provider cooldowns and rate-limit events in SQLite. It
// implements metadata.Store. Safe for concurrent use (SQLite serializes
// writes; all methods are single-statement).
type Store struct {
	db  *sql.DB
	log *slog.Logger
}

// NewStore creates the cooldown/event tables and returns a Store backed by db.
func NewStore(db *sql.DB, logger *slog.Logger) (*Store, error) {
	s := &Store{db: db, log: logger}
	if err := s.init(); err != nil {
		return nil, err
	}
	return s, nil
}

// rateLimitEventRetention bounds the durable rate-limit event log: on startup
// (and every Store construction) all but the newest rows are pruned.
const rateLimitEventRetention = 500

func (s *Store) init() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS provider_cooldowns (
			provider  TEXT PRIMARY KEY,
			until_ts  INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS rate_limit_events (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			ts               INTEGER NOT NULL,
			provider         TEXT NOT NULL,
			duration_sec     INTEGER NOT NULL,
			source           TEXT NOT NULL DEFAULT '',
			retry_after_sec  INTEGER NOT NULL DEFAULT 0
		);`); err != nil {
		return err
	}
	return s.prune()
}

// prune removes expired cooldowns and caps the event log. Called on init so a
// long-running install stays bounded.
func (s *Store) prune() error {
	_, err := s.db.Exec(`
		DELETE FROM provider_cooldowns WHERE until_ts <= ?;
		DELETE FROM rate_limit_events
		 WHERE id NOT IN (SELECT id FROM rate_limit_events ORDER BY id DESC LIMIT ?);`,
		time.Now().Unix(), rateLimitEventRetention)
	return err
}

// SetCooldown upserts the cooldown expiry for a provider.
func (s *Store) SetCooldown(provider string, until time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO provider_cooldowns (provider, until_ts) VALUES (?, ?)
		 ON CONFLICT(provider) DO UPDATE SET until_ts = excluded.until_ts`,
		provider, until.Unix())
	if err != nil && s.log != nil {
		s.log.Warn("persist cooldown failed", "provider", provider, "error", err, "component", "metadata_rate_store")
	}
	return err
}

// DeleteCooldown removes a provider's cooldown row (manual clear).
func (s *Store) DeleteCooldown(provider string) error {
	_, err := s.db.Exec(`DELETE FROM provider_cooldowns WHERE provider = ?`, provider)
	if err != nil && s.log != nil {
		s.log.Warn("delete cooldown failed", "provider", provider, "error", err, "component", "metadata_rate_store")
	}
	return err
}

// LoadCooldowns returns all persisted cooldowns (including expired ones —
// callers filter by time).
func (s *Store) LoadCooldowns() ([]metadata.CooldownEntry, error) {
	rows, err := s.db.Query(`SELECT provider, until_ts FROM provider_cooldowns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []metadata.CooldownEntry
	for rows.Next() {
		var p string
		var ts int64
		if err := rows.Scan(&p, &ts); err != nil {
			return nil, err
		}
		out = append(out, metadata.CooldownEntry{Provider: p, Until: time.Unix(ts, 0)})
	}
	return out, rows.Err()
}

// LogRateLimitEvent records a cooldown application for observability.
func (s *Store) LogRateLimitEvent(provider string, duration time.Duration, source string, retryAfter time.Duration) error {
	_, err := s.db.Exec(
		`INSERT INTO rate_limit_events (ts, provider, duration_sec, source, retry_after_sec)
		 VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), provider, int64(duration.Seconds()), source, int64(retryAfter.Seconds()))
	if err != nil && s.log != nil {
		s.log.Warn("log rate-limit event failed", "provider", provider, "error", err, "component", "metadata_rate_store")
	}
	return err
}

// LoadRateLimitEvents returns the most recent limit events in chronological
// order. limit ≤ 0 defaults to 50.
func (s *Store) LoadRateLimitEvents(limit int) ([]metadata.RateLimitEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT ts, provider, duration_sec, source, retry_after_sec
		 FROM rate_limit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rev []metadata.RateLimitEvent
	for rows.Next() {
		var e metadata.RateLimitEvent
		var ts, dur, ra int64
		if err := rows.Scan(&ts, &e.Provider, &dur, &e.Source, &ra); err != nil {
			return nil, err
		}
		e.TS = time.Unix(ts, 0)
		e.Duration = time.Duration(dur) * time.Second
		e.RetryAfter = time.Duration(ra) * time.Second
		rev = append(rev, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to chronological order (oldest of the window first).
	out := make([]metadata.RateLimitEvent, len(rev))
	for i, e := range rev {
		out[len(rev)-1-i] = e
	}
	return out, nil
}
