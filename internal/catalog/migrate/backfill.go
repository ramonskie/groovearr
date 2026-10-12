// Package migrate performs the one-time, best-effort backfill of the legacy
// JSON-blob metadata caches (album_discovery_cache, artist_overview_cache) into
// the durable catalog tables (album_tracks, album_metadata, artist_metadata,
// artist_discography) and enqueues any album that is still unmaterialized so the
// refresh engine fills it later.
//
// Design constraints (AGENTS.md):
//
//   - The legacy tables are read directly here because they were never put
//     behind a store interface. They are left in place — the schema is
//     additive-only and existing databases upgrade without data loss.
//   - Writes go through catalog.Store, never raw SQL, so replace/upsert
//     semantics (and the queue's (entity_type, entity_id) uniqueness) are the
//     single source of truth.
//   - Best-effort: a malformed or unresolvable row is logged and skipped; it
//     never fails the whole run.
//   - Idempotent + resumable: ReplaceAlbumTracks / ReplaceArtistDiscography are
//     delete-then-insert, the metadata upserts conflict-resolve, and the queue
//     upserts on (entity_type, entity_id). Re-running is a no-op.
//   - Short transactions: each store write is its own short transaction; there
//     is never one giant transaction. Legacy rows are read fully into memory
//     first (closing the cursor) because the shared SQLite connection is
//     SetMaxOpenConns(1) — holding a cursor open while writing would deadlock.
//
// SCHEDULED FOR REMOVAL. This package exists only to migrate databases created
// before the catalog existed. Once the first release carrying internal/catalog
// has shipped and existing installs have upgraded, delete this package and its
// wiring in cmd/groovearr/app.go (search "migrate.Start" / the backfill Runner).
// The legacy tables themselves stay — the schema is additive-only and they are
// never dropped. The only thing that goes away is the blob *decoder*.
//
// Tracked in .opencode/context/project-intelligence/living-notes.md
// ("Remove legacy cache backfill"). Do not remove before an upgrade window has
// passed, or the upgrade stops being lossless.
package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/strutil"
)

// Legacy table names. They are read-only here and are never dropped or altered.
const (
	tableAlbumDiscoveryCache  = "album_discovery_cache"
	tableArtistOverviewCache  = "artist_overview_cache"
	libraryProviderName       = "library"
	legacyTimeLayoutSQLiteNow = "2006-01-02 15:04:05"
)

// Result reports what a backfill pass decoded and enqueued. It is returned so
// the caller can log a concise one-line summary; a zero value means the legacy
// tables were empty and nothing was done.
type Result struct {
	// AlbumsDecoded counts album cache rows successfully materialized.
	AlbumsDecoded int
	// TracksDecoded counts album_tracks rows written.
	TracksDecoded int
	// ArtistsDecoded counts artist cache rows successfully materialized.
	ArtistsDecoded int
	// ReleasesDecoded counts artist_discography rows written.
	ReleasesDecoded int
	// Enqueued counts remainder albums added/re-armed in the refresh queue.
	Enqueued int
	// Skipped counts rows (or individual tracks) dropped as malformed or
	// unresolvable. Best-effort: skipped rows are logged, never fatal.
	Skipped int
}

// Enabled is the live gate that reports whether catalog materialization work
// (the one-time backfill plus its remainder enqueue) may run. It is read at the
// start of every pass so a config hot-reload is honored without caching config
// (AGENTS §9). A nil gate means enabled.
type Enabled func() bool

// Backfill decodes the legacy JSON-blob caches into the durable catalog and
// enqueues unmaterialized albums. It takes the shared *sql.DB to READ the legacy
// tables and the catalog.Store to WRITE the catalog.
//
// enabled, when non-nil, is consulted first: a disabled catalog skips the whole
// pass and returns a zero Result without touching the database.
//
// It is safe to call repeatedly: every write is replace/upsert semantics. A
// malformed row is logged and skipped. When both legacy tables are empty the
// call returns a zero Result immediately without touching the catalog, so a
// fresh install pays no cost. A nil store or db is a no-op.
func Backfill(ctx context.Context, db *sql.DB, store catalog.Store, log *slog.Logger, enabled Enabled) (Result, error) {
	var res Result
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if db == nil || store == nil {
		return res, nil
	}
	if enabled != nil && !enabled() {
		log.Debug("catalog backfill: disabled by config, skipping",
			"component", "catalog-migrate")
		return res, nil
	}

	albumRows, err := loadAlbumCacheRows(ctx, db)
	if err != nil {
		return res, err
	}
	artistRows, err := loadArtistCacheRows(ctx, db)
	if err != nil {
		return res, err
	}

	// Nothing to migrate — skip the remainder enqueue too. This is the guard
	// that keeps startup cheap on databases that never used the old caches.
	if len(albumRows) == 0 && len(artistRows) == 0 {
		log.Debug("catalog backfill: legacy cache tables empty, nothing to do",
			"component", "catalog-migrate")
		return res, nil
	}

	now := time.Now().UTC()
	b := &backfiller{ctx: ctx, store: store, log: log, now: now, res: &res}

	for _, row := range albumRows {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		b.processAlbum(row)
	}

	if len(artistRows) > 0 {
		index, err := b.localArtistIndex(ctx, db)
		if err != nil {
			return res, err
		}
		for _, row := range artistRows {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			b.processArtist(row, index)
		}
	}

	enqueued, err := b.enqueueRemainder(ctx, db)
	if err != nil {
		return res, err
	}
	res.Enqueued = enqueued

	return res, nil
}

// backfiller carries the per-run dependencies so the per-row helpers stay pure
// functions of their inputs plus this state.
type backfiller struct {
	ctx   context.Context
	store catalog.Store
	log   *slog.Logger
	now   time.Time
	res   *Result
}

// processAlbum decodes one album_discovery_cache row and materializes it. A
// malformed blob is skipped (logged) without aborting the run.
func (b *backfiller) processAlbum(row albumCacheRow) {
	var raw []legacyTrack
	if err := json.Unmarshal([]byte(row.tracksJSON), &raw); err != nil {
		b.log.Warn("catalog backfill: skip album with malformed tracks_json",
			"album_id", row.albumID, "error", err, "component", "catalog-migrate")
		b.res.Skipped++
		return
	}

	// A legacy row is either a provider tracklist (provider_name = real
	// provider) or the post-import "library" rewrite. Origin follows the stored
	// provider_name.
	origin := catalog.OriginProvider
	providerName := row.providerName
	if providerName == "" || strings.EqualFold(providerName, libraryProviderName) {
		origin = catalog.OriginLibrary
		providerName = ""
	}

	// Rebuild the rows, normalizing disc/track numbers so a provider that omits
	// them cannot violate UNIQUE(album_id, disc_number, track_number) and abort
	// the whole album. A within-album key collision drops the later duplicate.
	tracks := make([]catalog.AlbumTrack, 0, len(raw))
	seen := make(map[[2]int]struct{}, len(raw))
	for i, t := range raw {
		disc := t.DiscNumber
		if disc <= 0 {
			disc = 1
		}
		num := t.TrackNumber
		if num <= 0 {
			num = i + 1
		}
		key := [2]int{disc, num}
		if _, dup := seen[key]; dup {
			b.log.Warn("catalog backfill: drop duplicate track key in album",
				"album_id", row.albumID, "disc", disc, "track", num, "component", "catalog-migrate")
			b.res.Skipped++
			continue
		}
		seen[key] = struct{}{}

		providerTrackID := ""
		if origin == catalog.OriginProvider {
			providerTrackID = t.ProviderID
		}
		tracks = append(tracks, catalog.AlbumTrack{
			AlbumID:         row.albumID,
			DiscNumber:      disc,
			TrackNumber:     num,
			Title:           t.Title,
			ArtistName:      t.ArtistName,
			DurationMs:      t.DurationMs,
			ISRC:            t.ISRC,
			ProviderName:    providerName,
			ProviderTrackID: providerTrackID,
			Origin:          origin,
			CreatedAt:       row.cachedAt,
			UpdatedAt:       row.cachedAt,
		})
	}

	if err := b.store.ReplaceAlbumTracks(b.ctx, row.albumID, tracks); err != nil {
		b.log.Warn("catalog backfill: replace album tracks failed",
			"album_id", row.albumID, "error", err, "component", "catalog-migrate")
		b.res.Skipped++
		return
	}

	metaProvider := providerName
	metaAlbumID := row.providerAlbumID
	if origin == catalog.OriginLibrary {
		// No provider link for a library-derived row; leave the columns empty so
		// the read path never mistakes the sentinel for a real provider.
		metaProvider = ""
		metaAlbumID = ""
	}
	m := catalog.AlbumMetadata{
		AlbumID:         row.albumID,
		ProviderName:    metaProvider,
		ProviderAlbumID: metaAlbumID,
		RefreshStatus:   catalog.StatusFresh,
		LastRefreshedAt: row.cachedAt,
		UpdatedAt:       b.now,
	}
	if err := b.store.UpsertAlbumMetadata(b.ctx, m); err != nil {
		b.log.Warn("catalog backfill: upsert album metadata failed",
			"album_id", row.albumID, "error", err, "component", "catalog-migrate")
		b.res.Skipped++
		return
	}

	b.res.AlbumsDecoded++
	b.res.TracksDecoded += len(tracks)
}

// processArtist decodes one artist_overview_cache row and materializes it,
// resolving the local artist by normalized name. An unresolved artist (no local
// row) or malformed blob is skipped best-effort.
func (b *backfiller) processArtist(row artistCacheRow, index map[string]int64) {
	artistID, ok := index[strutil.NormalizeName(row.artistName)]
	if !ok {
		artistID, ok = index[strings.TrimSpace(row.normalizedName)]
	}
	if !ok || artistID <= 0 {
		b.log.Debug("catalog backfill: skip artist with no local match",
			"artist_name", row.artistName, "normalized_name", row.normalizedName,
			"component", "catalog-migrate")
		b.res.Skipped++
		return
	}

	// A corrupt genres blob degrades to no genres rather than dropping the whole
	// overview — the rest of the row is still useful.
	genres := decodeGenres(row.genresJSON)
	if strings.TrimSpace(row.genresJSON) != "" && strings.TrimSpace(row.genresJSON) != "null" && genres == nil {
		b.log.Warn("catalog backfill: artist genres_json malformed, keeping other fields",
			"normalized_name", row.normalizedName, "component", "catalog-migrate")
		b.res.Skipped++
	}

	m := catalog.ArtistMetadata{
		ArtistID:         artistID,
		ProviderName:     row.providerName,
		ProviderArtistID: row.providerArtistID,
		ImageURL:         row.imageURL,
		Genres:           genres,
		RefreshStatus:    catalog.StatusFresh,
		LastRefreshedAt:  row.cachedAt,
		UpdatedAt:        b.now,
	}
	if err := b.store.UpsertArtistMetadata(b.ctx, m); err != nil {
		b.log.Warn("catalog backfill: upsert artist metadata failed",
			"artist_id", artistID, "error", err, "component", "catalog-migrate")
		b.res.Skipped++
		return
	}

	releases, malformed := decodeDiscography(row.discographyJSON, artistID, row.providerName, row.cachedAt)
	if malformed {
		b.log.Warn("catalog backfill: artist discography_json malformed, keeping overview",
			"normalized_name", row.normalizedName, "component", "catalog-migrate")
		b.res.Skipped++
	} else if len(releases) > 0 {
		if err := b.store.ReplaceArtistDiscography(b.ctx, artistID, releases); err != nil {
			b.log.Warn("catalog backfill: replace discography failed",
				"artist_id", artistID, "error", err, "component", "catalog-migrate")
			b.res.Skipped++
		} else {
			b.res.ReleasesDecoded += len(releases)
		}
	}

	b.res.ArtistsDecoded++
}

// enqueueRemainder queues every album that still has no materialized tracklist
// and no fresh/stale metadata row, so the engine fills it later at low
// priority. It is a no-op for albums already materialized.
//
// The album ids are read fully and the cursor closed before any queue write
// because the shared connection is SetMaxOpenConns(1).
func (b *backfiller) enqueueRemainder(ctx context.Context, db *sql.DB) (int, error) {
	ids, err := unmaterializedAlbumIDs(ctx, db, b.now)
	if err != nil {
		return 0, err
	}
	enqueued := 0
	for _, albumID := range ids {
		if err := ctx.Err(); err != nil {
			return enqueued, err
		}
		_, err := b.store.EnqueueRefresh(ctx, catalog.RefreshItem{
			EntityType: catalog.EntityAlbum,
			EntityID:   albumID,
			Reason:     catalog.ReasonBulk,
			Priority:   catalog.PriorityLow,
			DueAt:      b.now,
			CreatedAt:  b.now,
		})
		if err != nil {
			b.log.Warn("catalog backfill: enqueue remainder album failed",
				"album_id", albumID, "error", err, "component", "catalog-migrate")
			b.res.Skipped++
			continue
		}
		enqueued++
	}
	return enqueued, nil
}

// localArtistIndex builds a normalized-name -> id map from the local artists
// table. Reading it once avoids a per-row query while still allowing a
// case/accent/punctuation-insensitive match.
func (b *backfiller) localArtistIndex(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM artists`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	index := make(map[string]int64)
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		key := strutil.NormalizeName(name)
		if key == "" {
			continue
		}
		if _, exists := index[key]; !exists {
			index[key] = id
		}
	}
	return index, rows.Err()
}

// ─── Legacy reads ────────────────────────────────────────────────────

type albumCacheRow struct {
	albumID         int64
	providerName    string
	providerAlbumID string
	tracksJSON      string
	cachedAt        time.Time
}

type artistCacheRow struct {
	normalizedName   string
	artistName       string
	providerName     string
	providerArtistID string
	imageURL         string
	genresJSON       string
	topTracksJSON    sql.NullString
	discographyJSON  string
	cachedAt         time.Time
}

func loadAlbumCacheRows(ctx context.Context, db *sql.DB) ([]albumCacheRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT album_id, provider_name, provider_album_id, tracks_json, cached_at
		FROM `+tableAlbumDiscoveryCache)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []albumCacheRow{}
	for rows.Next() {
		var r albumCacheRow
		var cachedAt string
		if err := rows.Scan(&r.albumID, &r.providerName, &r.providerAlbumID, &r.tracksJSON, &cachedAt); err != nil {
			return nil, err
		}
		r.cachedAt = parseLegacyTime(cachedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadArtistCacheRows(ctx context.Context, db *sql.DB) ([]artistCacheRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT normalized_name, artist_name, provider_name, provider_artist_id,
		image_url, genres_json, top_tracks_json, discography_json, cached_at
		FROM `+tableArtistOverviewCache)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []artistCacheRow{}
	for rows.Next() {
		var r artistCacheRow
		var cachedAt string
		if err := rows.Scan(&r.normalizedName, &r.artistName, &r.providerName, &r.providerArtistID,
			&r.imageURL, &r.genresJSON, &r.topTracksJSON, &r.discographyJSON, &cachedAt); err != nil {
			return nil, err
		}
		r.cachedAt = parseLegacyTime(cachedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// unmaterializedAlbumIDs returns the ids of albums with no materialized
// tracklist and no successful (fresh/stale) metadata row. Albums parked as
// terminal 'unmatched'/'failed' are excluded while their long retry has not yet
// elapsed (catalog.UnmatchedRetry), so a boot-time remainder enqueue cannot
// re-arm a queue row the engine deliberately parked and defeat the drain.
//
// now is the reference clock for the retry window; the cutoff is compared against
// album_metadata.updated_at, which is stamped when the terminal status is
// recorded. Times are RFC3339 UTC text, so the lexicographic comparison is
// chronological.
func unmaterializedAlbumIDs(ctx context.Context, db *sql.DB, now time.Time) ([]int64, error) {
	unmatchedCutoff := now.Add(-catalog.UnmatchedRetry).UTC().Format(time.RFC3339)
	rows, err := db.QueryContext(ctx, `
		SELECT al.id
		FROM albums al
		WHERE NOT EXISTS (SELECT 1 FROM album_tracks t WHERE t.album_id = al.id)
		  AND NOT EXISTS (
			SELECT 1 FROM album_metadata am
			WHERE am.album_id = al.id
			  AND ( am.refresh_status IN ('fresh','stale')
			     OR ( am.refresh_status IN ('unmatched','failed') AND am.updated_at > ? ) )
		  )
		ORDER BY al.id`, unmatchedCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ─── Decoders ────────────────────────────────────────────────────────

// legacyTrack is the union of the two shapes ever written to tracks_json: a
// provider tracklist (provider_id/artist_name/album_title/title/track_number/
// disc_number/duration_ms/isrc) and the post-import library rewrite
// (title/track_number/duration_ms). Unknown/absent fields decode to zero.
type legacyTrack struct {
	ProviderID  string `json:"provider_id"`
	ArtistName  string `json:"artist_name"`
	AlbumTitle  string `json:"album_title"`
	Title       string `json:"title"`
	TrackNumber int    `json:"track_number"`
	DiscNumber  int    `json:"disc_number"`
	DurationMs  int64  `json:"duration_ms"`
	ISRC        string `json:"isrc"`
}

// legacyRelease is a defensive decoder for a release-list discography blob. The
// historical blob is map[string]int (release-type counts only), which carries no
// per-release detail; this path exists so an array-shaped blob (or a future
// producer) materializes cleanly instead of being dropped.
type legacyRelease struct {
	ProviderAlbumID string `json:"provider_album_id"`
	ProviderID      string `json:"provider_id"`
	Title           string `json:"title"`
	Year            int    `json:"year"`
	Type            string `json:"type"`
	CoverURL        string `json:"cover_url"`
	Cover           string `json:"cover"`
	TrackCount      int    `json:"track_count"`
}

func decodeGenres(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var genres []string
	if err := json.Unmarshal([]byte(raw), &genres); err != nil {
		return nil
	}
	return genres
}

// decodeDiscography accepts both legacy shapes. It returns (releases, false) for
// a valid blob (an array of releases, or the common map[string]int counts which
// yields no rows), and (nil, true) for a blob that is neither — so the caller
// can log it without failing the run.
func decodeDiscography(raw string, artistID int64, providerName string, seenAt time.Time) ([]catalog.ArtistDiscographyRelease, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, false
	}

	var list []legacyRelease
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		releases := make([]catalog.ArtistDiscographyRelease, 0, len(list))
		for _, r := range list {
			providerAlbumID := r.ProviderAlbumID
			if providerAlbumID == "" {
				providerAlbumID = r.ProviderID
			}
			cover := r.CoverURL
			if cover == "" {
				cover = r.Cover
			}
			releases = append(releases, catalog.ArtistDiscographyRelease{
				ArtistID:        artistID,
				ProviderName:    providerName,
				ProviderAlbumID: providerAlbumID,
				Title:           r.Title,
				Year:            r.Year,
				Type:            r.Type,
				CoverURL:        cover,
				TrackCount:      r.TrackCount,
				LastSeenAt:      seenAt,
			})
		}
		return releases, false
	}

	var counts map[string]int
	if err := json.Unmarshal([]byte(raw), &counts); err == nil {
		// Release-type counts only: nothing to materialize. Valid, not malformed.
		return nil, false
	}

	return nil, true
}

// parseLegacyTime parses cached_at in either the RFC3339 form the discover
// handler writes or the SQLite datetime('now') form used by the column default.
// Unparseable input yields the zero time, which the catalog stores as NULL.
func parseLegacyTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, legacyTimeLayoutSQLiteNow} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ─── Lifecycle ───────────────────────────────────────────────────────

// Runner owns the one-time backfill goroutine so its lifecycle is explicit and
// tied to the server background context, mirroring the refresh engine: Start
// returns immediately (startup never blocks on the decode) and Stop cancels and
// waits for the goroutine to exit so it cannot outlive the store it writes
// through.
type Runner struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// Start launches Backfill once in its own goroutine and returns a Runner. A nil
// db or store yields an already-stopped Runner. The goroutine is tied to ctx:
// cancelling ctx aborts between per-album transactions, so a partial pass simply
// resumes on the next boot (the run is idempotent). errors are logged and never
// propagated — backfill is best-effort startup housekeeping.
func Start(ctx context.Context, db *sql.DB, store catalog.Store, log *slog.Logger, enabled Enabled) *Runner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Runner{}
	if db == nil || store == nil {
		return r
	}
	r.startWork(ctx, db, store, log, enabled)
	return r
}

// startWork configures the runner and launches the backfill goroutine. The
// cancel/done pair is published under r.mu so a concurrent Stop can never
// observe a half-written pair (Start/Stop is race-clean).
func (r *Runner) startWork(ctx context.Context, db *sql.DB, store catalog.Store, log *slog.Logger, enabled Enabled) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.mu.Lock()
	r.cancel = cancel
	r.done = done
	r.mu.Unlock()

	go func() {
		defer close(done)
		res, err := Backfill(runCtx, db, store, log, enabled)
		if err != nil {
			if runCtx.Err() == nil {
				log.Warn("catalog backfill failed", "error", err, "component", "catalog-migrate")
			}
			return
		}
		if res == (Result{}) {
			return
		}
		log.Info("catalog backfill complete",
			"albums", res.AlbumsDecoded, "tracks", res.TracksDecoded,
			"artists", res.ArtistsDecoded, "releases", res.ReleasesDecoded,
			"enqueued", res.Enqueued, "skipped", res.Skipped,
			"component", "catalog-migrate")
	}()
}

// Stop cancels the backfill and blocks until its goroutine exits. Safe to call
// on a Runner that was never started or already stopped.
func (r *Runner) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.done = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
