package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/jobs"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
	"github.com/ramonskie/groovearr/internal/refresh"
)

// ─── Library handlers ────────────────────────────────────────────────

func (s *Server) handleLibraryTracks(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	tracks, err := s.store.SearchTracks(ctx, q, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if tracks == nil {
		tracks = []domain.Track{}
	}

	// Exclude tracks that live under the playlist path — playlist files
	// are managed by buildPlaylistFolder and shouldn't appear in the library view.
	cfg := s.cfg.Get()
	if cfg.Library.PlaylistPath != "" {
		absPlaylist, _ := filepath.Abs(cfg.Library.PlaylistPath)
		tracks = filterByPath(tracks, absPlaylist)
	}

	writeJSON(w, http.StatusOK, tracks)
}

// filterByPath removes tracks whose FilePath starts with excludeRoot.
func filterByPath(tracks []domain.Track, excludeRoot string) []domain.Track {
	out := tracks[:0]
	for _, t := range tracks {
		if t.FilePath == "" || !strings.HasPrefix(t.FilePath, excludeRoot) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) handleLibraryArtists(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	var artists []domain.Artist
	var err error
	if q != "" {
		// SearchArtists has no offset — fetch offset+limit and slice so search
		// results paginate identically to the plain list.
		fetched, err := s.store.SearchArtists(ctx, q, offset+limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		start := offset
		if start > len(fetched) {
			start = len(fetched)
		}
		end := start + limit
		if end > len(fetched) {
			end = len(fetched)
		}
		artists = fetched[start:end]
	} else {
		artists, err = s.store.ListArtists(ctx, offset, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if artists == nil {
		artists = []domain.Artist{}
	}

	// Transform local image paths to API URLs for the frontend. Compilation
	// groupings ("Various Artists") carry no portrait — they show the
	// placeholder avatar instead.
	for i := range artists {
		if library.IsCompilationArtist(artists[i].Name) {
			artists[i].ThumbURL = ""
			continue
		}
		if library.IsLocalArtistThumb(artists[i].ThumbURL) {
			artists[i].ThumbURL = fmt.Sprintf("/api/artist-image/%d", artists[i].ID)
		}
	}

	writeJSON(w, http.StatusOK, artists)
}

func (s *Server) handleLibraryAlbums(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	albums, err := s.store.SearchAlbums(ctx, q, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.Album{}
	}
	writeJSON(w, http.StatusOK, albums)
}

// handleLibraryAlbum returns one library album by ID. Thin per AGENTS §4:
// parse/validate -> store read -> writeJSON. It exists so a client can resolve a
// single album without depending on which page of the (now paginated)
// artist-album listing happens to be loaded — the library page's deep link
// `?artist=X&album=Y` must resolve Y even when it is past page one. An unknown
// or unparsable ID is a 404/400; a store read failure is a 500.
func (s *Server) handleLibraryAlbum(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("albumID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}
	album, err := s.store.GetAlbum(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if album == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}
	writeJSON(w, http.StatusOK, album)
}

// duplicateArtistEntry is one artist inside a duplicate-name group.
type duplicateArtistEntry struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Tracks int    `json:"track_count"`
}

// duplicateGroup is a set of artists whose names differ only by case (the
// common "Acda en de Munnik" / "Acda en De Munnik" split). The first entry is
// the suggested merge target (closest to the MusicBrainz spelling, then most
// tracks).
type duplicateGroup struct {
	Name    string                 `json:"name"`
	Artists []duplicateArtistEntry `json:"artists"`
}

// canonicalArtistName resolves the authoritative spelling for an artist via
// MusicBrainz. Best-effort: failures fall back to the library's own casing.
// Callers bound the context (the scan job runs sequentially against
// MusicBrainz's rate limit; the merge handler wraps a short timeout).
func (s *Server) canonicalArtistName(ctx context.Context, name string) string {
	canonical, _ := s.runners.LookupCanonicalArtist(ctx, name)
	return canonical
}

// noteProviderRateLimit marks a provider as cooling down when the error is a
// rate-limit response. Shared by discover search and album discovery so a
// throttled provider is skipped app-wide for a short window. When the server
// supplied a Retry-After (via metadata.RateLimitError), the cooldown honors it.
func (s *Server) noteProviderRateLimit(providerName string, err error) {
	if err != nil && errors.Is(err, metadata.ErrRateLimited) {
		var rl *metadata.RateLimitError
		var retryAfter time.Duration
		if errors.As(err, &rl) {
			retryAfter = rl.RetryAfter
		}
		s.providerCooldown.MarkAfter(providerName, retryAfter)
		s.log.Warn("provider rate limited, cooling down", "provider", providerName, "error", err, "component", "api")
	}
}

// canonicalMatchScore rates how closely name matches the canonical spelling.
// An exact match wins outright; otherwise each word that already matches the
// canonical casing scores +1, so a mostly-correct variant outranks one that
// capitalizes every particle.
func canonicalMatchScore(name, canonical string) int {
	if canonical == "" {
		return 0
	}
	if name == canonical {
		return len(strings.Fields(name)) + 1
	}
	nw, cw := strings.Fields(name), strings.Fields(canonical)
	n := len(nw)
	if len(cw) < n {
		n = len(cw)
	}
	score := 0
	for i := 0; i < n; i++ {
		if nw[i] == cw[i] {
			score++
		}
	}
	return score
}

// handleLibraryArtistDuplicates lists artists that collide case-insensitively
// from the persisted duplicate scan. Groups not yet scanned are hidden — run
// the "duplicates" job to (re)scan. Each group's first entry is the sensible
// merge target: closest to the canonical provider spelling first, then most
// tracks, then name.
func (s *Server) handleLibraryArtistDuplicates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	dss, ok := s.store.(jobs.DuplicateScanStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"groups": []duplicateGroup{}})
		return
	}
	canonicals, err := dss.ListDuplicateCanonicals(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	byLower := map[string][]domain.Artist{}
	for off := 0; ; off += 200 {
		artists, err := s.store.ListArtists(ctx, off, 200)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if len(artists) == 0 {
			break
		}
		for _, a := range artists {
			// Group with the same normalized key the duplicates job writes to
			// duplicate_scan (spelling variants AND feat-marked rows fold to
			// their identity artist), so the groups surface here exactly as the
			// job grouped them.
			key := library.NormalizeArtistKey(library.IdentityArtistName(a.Name))
			byLower[key] = append(byLower[key], a)
		}
	}

	groups := []duplicateGroup{}
	for key, list := range byLower {
		if len(list) < 2 {
			continue
		}
		canonical, scanned := canonicals[key]
		if !scanned {
			continue // not yet covered by a scan — hide until the next run
		}
		entries := make([]duplicateArtistEntry, 0, len(list))
		for _, a := range list {
			ts, _ := s.store.GetTracksByArtist(ctx, a.ID)
			entries = append(entries, duplicateArtistEntry{ID: a.ID, Name: a.Name, Tracks: len(ts)})
		}
		sort.SliceStable(entries, func(i, j int) bool {
			si, sj := canonicalMatchScore(entries[i].Name, canonical), canonicalMatchScore(entries[j].Name, canonical)
			if si != sj {
				return si > sj
			}
			if entries[i].Tracks != entries[j].Tracks {
				return entries[i].Tracks > entries[j].Tracks
			}
			return entries[i].Name < entries[j].Name
		})
		groups = append(groups, duplicateGroup{Name: key, Artists: entries})
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// handleLibraryArtistMerge folds one artist into another, reassigning its
// albums and tracks. Body: {"remove_id": N}; keep = the path artistID.
func (s *Server) handleLibraryArtistMerge(w http.ResponseWriter, r *http.Request) {
	keepID, err := strconv.ParseInt(r.PathValue("artistID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	var req struct {
		RemoveID int64 `json:"remove_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}
	if req.RemoveID == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("remove_id is required"))
		return
	}

	if err := s.store.MergeArtists(r.Context(), keepID, req.RemoveID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Adopt the canonical provider spelling so the surviving artist keeps
	// the authoritative name, even when the local variant that won was the
	// misspelled one. Only applied when a provider agrees it is the same artist.
	// The canonical comes from the duplicate scan cache when available (so the
	// merge matches what the UI showed); a single live lookup fills the gap for
	// groups merged outside a scan. The scan entry is invalidated on merge.
	renamed := false
	canonical := ""
	keep, err := s.store.GetArtist(r.Context(), keepID)
	if err == nil && keep != nil {
		if dss, ok := s.store.(jobs.DuplicateScanStore); ok {
			key := library.NormalizeArtistKey(library.IdentityArtistName(keep.Name))
			if c, found, derr := dss.GetDuplicateCanonical(r.Context(), key); derr == nil && found {
				canonical = c
			}
			if err := dss.DeleteDuplicateCanonical(r.Context(), key); err != nil {
				s.log.Warn("invalidate duplicate scan entry failed",
					"group", key, "error", err, "component", "api")
			}
		}
		if canonical == "" {
			lookupCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			canonical = s.canonicalArtistName(lookupCtx, keep.Name)
			cancel()
		}
		if canonical != "" && canonical != keep.Name && normalizeKey(canonical) == normalizeKey(keep.Name) {
			if err := s.store.RenameArtist(r.Context(), keepID, canonical); err != nil {
				s.log.Warn("rename merged artist to canonical name failed",
					"artist", keep.Name, "canonical", canonical, "error", err, "component", "api")
			} else {
				renamed = true
			}
		}
	}
	out := map[string]any{"merged": true, "renamed": renamed}
	if renamed {
		out["canonical_name"] = canonical
	}
	// Complete the merge by moving the survivor's tracks into the configured
	// folder layout (merged tracks sit under the removed artist's folders, and
	// a canonical rename may have left the keeper's folder stale). Runs as a
	// background job; skipped when another job is already running.
	if s.jobs != nil {
		if _, err := s.jobs.Start("organize", s.runners.OrganizeArtist(keepID)); err == nil {
			out["organize_started"] = true
		} else if !errors.Is(err, jobs.ErrBusy) {
			s.log.Warn("start merge organize failed", "error", err, "component", "api")
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// sqlDBProvider is satisfied by types that expose a *sql.DB (e.g. *sqlite.Store).
type sqlDBProvider interface {
	DB() *sql.DB
}

// DiscoveryTrackEntry is a track from discovery merged with library download status.
type discoveryTrackEntry struct {
	Title          string `json:"title"`
	TrackNumber    int    `json:"track_number"`
	DurationMs     int64  `json:"duration_ms"`
	Downloaded     bool   `json:"downloaded"`
	LibraryTrackID int64  `json:"library_track_id,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	FileSize       int64  `json:"file_size,omitempty"`
	Bitrate        int    `json:"bitrate,omitempty"`
	Format         string `json:"format,omitempty"`
}

// albumDiscoveryResponse is the JSON payload for the discovery endpoint.
// metadata_pending is true when the album's provider tracklist has not been
// materialized yet (a cold read); the response then carries the local library
// fallback. The pre-existing fields keep their shape so the UI reads them
// unchanged.
//
// metadata_status and metadata_terminal are additive: they carry the album's
// persisted catalog RefreshStatus and whether it is terminal (unmatched/failed)
// so the UI can present an accurate parked state instead of an indefinite
// "fetching" spinner. metadata_pending remains true for a terminal album (its
// provider tracklist is still unmaterialized) but metadata_terminal lets the UI
// tell the two apart.
type albumDiscoveryResponse struct {
	Provider         string                `json:"provider"`
	ProviderAlbumID  string                `json:"provider_album_id"`
	Tracks           []discoveryTrackEntry `json:"tracks"`
	MetadataPending  bool                  `json:"metadata_pending"`
	MetadataStatus   catalog.RefreshStatus `json:"metadata_status"`
	MetadataTerminal bool                  `json:"metadata_terminal"`
}

// catalogReader is the subset of catalog.Store the album read path needs:
// materialized album tracklists and the album provider link. Declared in the
// api package so this layer depends only on the catalog capabilities it uses
// (AGENTS §3) and never on the refresh queue it does not touch — which lets the
// queue land independently.
type catalogReader interface {
	catalog.AlbumTrackStore
	catalog.AlbumMetadataStore
}

// artistCatalogReader is the subset of catalog.Store the discover artist read
// path needs: the durable artist overview and discography. It is kept separate
// from catalogReader so each handler depends only on the catalog capabilities it
// uses (AGENTS §3).
type artistCatalogReader interface {
	catalog.ArtistMetadataStore
	catalog.ArtistDiscographyStore
}

// catalogDeps is the full catalog surface the server holds: the album read
// path's reader plus the artist read path's reader. It is the constructor's
// single catalog dependency (wired in cmd/groovearr/app.go) while each handler
// keeps its own narrow slice.
type catalogDeps interface {
	catalogReader
	artistCatalogReader
}

// handleLibraryAlbumDiscovery returns an album's expected tracklist merged with
// live local download status. A warm album is a pure catalog + library read.
// The first open of an unmaterialized (cold/pending) album performs one
// bounded, deduplicated synchronous provider fetch (Option A) when a cold
// fetcher is wired; on success the fresh rows are served, otherwise the request
// falls back to the local tracks with metadata_pending. Albums already matched
// (fresh, even with zero rows) or parked terminal (unmatched/failed before their
// retry window) are never re-fetched on open — see buildAlbumDiscovery.
func (s *Server) handleLibraryAlbumDiscovery(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()

	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if album == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	writeJSON(w, http.StatusOK, s.buildAlbumDiscovery(ctx, albumID, artist.ID))
}

// coldFetchBudget bounds how long the first open of an unmaterialized album
// waits on its one-time provider fetch before falling back to local tracks. The
// fetcher enforces its own hard ceiling; this outer bound keeps the HTTP request
// from ever outliving the UI's cold-open spinner.
const coldFetchBudget = 20 * time.Second

// albumColdFetcher is the narrow slice of the refresh engine's cold fetcher the
// album read path needs: one bounded, deduplicated cold materialization attempt.
// FetchMatched reports whether a provider album was resolved and materialized.
// It is declared here (not as *refresh.ColdFetcher) so the api package depends
// on behavior, not construction (AGENTS §3). Wired in cmd/groovearr/app.go via
// Server.SetAlbumColdFetcher — mirrors SetAlbumRefresher.
type albumColdFetcher interface {
	FetchMatched(ctx context.Context, albumID int64) (bool, error)
}

// SetAlbumColdFetcher injects the cold-read fetcher after construction. Pass nil
// to leave the cold path on its local-tracks fallback (the pre-subtask-10
// behavior).
func (s *Server) SetAlbumColdFetcher(f albumColdFetcher) {
	s.albumColdFetch = f
}

// buildAlbumDiscovery assembles the album-detail response from the materialized
// catalog tracklist merged with the live local library tracks. Every branch but
// the one-time Option-A cold fetch is a pure catalog + library read; the request
// never waits on a provider for an album that is already matched.
//
// Read policy:
//
//   - warm (materialized rows exist): pure SQLite read + scoped merge.
//   - fresh matched-empty (StatusFresh with zero rows): warm, served as an empty
//     materialized list; the album was matched and simply has no provider tracks.
//   - stale (materialized metadata older than the soft TTL): serve the rows
//     immediately and best-effort enqueue a low-priority refresh behind the
//     response; no provider call in the request.
//   - terminal (unmatched/failed) before its long retry elapses: do not
//     re-search and do not re-enqueue; serve local tracks with metadata_pending
//     (the parked queue row already carries the retry due_at).
//   - pending / no metadata row: Option-A synchronous cold fetch (one bounded,
//     deduplicated provider call when a fetcher is wired); on a timeout, error,
//     no-match, or no fetcher, fall back to local tracks with metadata_pending.
func (s *Server) buildAlbumDiscovery(ctx context.Context, albumID, artistID int64) *albumDiscoveryResponse {
	materialized := s.materializedAlbumTracks(ctx, albumID)
	meta := s.albumMetadata(ctx, albumID)

	// Only an unmaterialized album that is not already definitively matched may
	// trigger the synchronous cold fetch.
	if len(materialized) == 0 && !isMaterializedStatus(meta) {
		if s.terminalRetryParked(meta) {
			// Terminal and not yet due: re-searching now would defeat the state
			// that keeps the queue from churning, and re-enqueueing here would
			// overwrite the parked row's 90-day due_at with now. Serve local
			// tracks and leave the existing queue row untouched.
			return s.localPendingResponse(ctx, albumID, meta)
		}
		if s.albumColdFetch != nil {
			// Option A cold read: one synchronous, deduplicated provider fetch
			// that materializes the album. The fetch details live in
			// internal/refresh; this handler only names the narrow interface
			// above and bounds the wait.
			fetchCtx, cancel := context.WithTimeout(ctx, coldFetchBudget)
			matched, err := s.albumColdFetch.FetchMatched(fetchCtx, albumID)
			cancel()
			if err != nil {
				s.log.Warn("cold album fetch failed", "album_id", albumID, "error", err, "component", "api")
			}
			if err == nil && matched {
				materialized = s.materializedAlbumTracks(ctx, albumID)
				meta = s.albumMetadata(ctx, albumID)
			}
		}
	}

	// Tier-1 lazy staleness: a matched album whose metadata is past the soft TTL
	// is served as-is and refreshed behind the response. No provider call here.
	if isMaterializedStatus(meta) && s.albumMetadataStale(meta) {
		s.enqueueStaleRefresh(ctx, albumID)
	}

	providerName, providerAlbumID := providerLink(meta)

	if len(materialized) == 0 && !isMaterializedStatus(meta) {
		// Still cold (no provider match, a timeout/error, no fetcher wired, or a
		// pending row): serve the local library tracks as a best-effort fallback
		// and tell the UI the provider tracklist is not materialized yet.
		return s.localPendingResponse(ctx, albumID, meta)
	}

	albumLocal, artistLocal := s.albumScopeTracks(ctx, albumID, artistID)
	merged := catalog.MergeTracksScoped(materialized, albumLocal, artistLocal)
	entries := make([]discoveryTrackEntry, 0, len(merged))
	for i := range merged {
		entries = append(entries, mergedTrackEntry(&merged[i]))
	}
	status, terminal := metadataState(meta)
	return &albumDiscoveryResponse{
		Provider:         providerName,
		ProviderAlbumID:  providerAlbumID,
		Tracks:           entries,
		MetadataPending:  false,
		MetadataStatus:   status,
		MetadataTerminal: terminal,
	}
}

// isMaterializedStatus reports whether the album has a definitive provider link
// (fresh or stale), regardless of how many rows it materialized. A matched album
// with zero rows is warm, not cold, and must not be re-fetched on every open.
func isMaterializedStatus(meta *catalog.AlbumMetadata) bool {
	return meta != nil && (meta.RefreshStatus == catalog.StatusFresh || meta.RefreshStatus == catalog.StatusStale)
}

// localPendingResponse serves the album's local library tracks with
// metadata_pending=true: the fallback for an album with no materialized provider
// tracklist. metadata_terminal is true when the album is parked terminal
// (unmatched/failed) so the UI can show an accurate parked state; a nil or
// pending metadata row is not terminal.
func (s *Server) localPendingResponse(ctx context.Context, albumID int64, meta *catalog.AlbumMetadata) *albumDiscoveryResponse {
	providerName, providerAlbumID := providerLink(meta)
	libTracks, err := s.store.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		s.log.Warn("get tracks by album failed", "album_id", albumID, "error", err, "component", "api")
	}
	entries := make([]discoveryTrackEntry, 0, len(libTracks))
	for i := range libTracks {
		entries = append(entries, localTrackEntry(&libTracks[i]))
	}
	status, terminal := metadataState(meta)
	return &albumDiscoveryResponse{
		Provider:         providerName,
		ProviderAlbumID:  providerAlbumID,
		Tracks:           entries,
		MetadataPending:  true,
		MetadataStatus:   status,
		MetadataTerminal: terminal,
	}
}

// metadataState projects a persisted catalog metadata row onto the discovery
// response: the raw RefreshStatus and whether it is terminal (unmatched/failed).
// A nil row (never materialized) has no status and is not terminal — it is cold
// and still materializing, which metadata_pending already conveys.
func metadataState(meta *catalog.AlbumMetadata) (catalog.RefreshStatus, bool) {
	if meta == nil {
		return "", false
	}
	return meta.RefreshStatus, meta.RefreshStatus.IsTerminal()
}

// providerLink projects the persisted provider link from a metadata row. A nil
// row (never materialized) yields empty strings, which is the normal cold state.
func providerLink(meta *catalog.AlbumMetadata) (providerName, providerAlbumID string) {
	if meta == nil {
		return "", ""
	}
	return meta.ProviderName, meta.ProviderAlbumID
}

// terminalRetryParked reports whether a metadata row is a terminal
// (unmatched/failed) album still inside its long retry window. Such a row must
// neither be re-fetched nor re-enqueued: the parked queue row already carries
// its 90-day due_at, and re-arming it at now would let the engine re-search the
// provider within one interval. A nil row is not parked.
func (s *Server) terminalRetryParked(meta *catalog.AlbumMetadata) bool {
	return meta != nil && meta.RefreshStatus.IsTerminal() && !s.terminalRetryElapsed(meta)
}

// terminalRetryElapsed reports whether a terminal (unmatched/failed) album is due
// for another provider attempt. It uses the same long interval the refresh queue
// uses for unmatched rows, measured against the metadata's refresh/update time so
// opening the album inside the window does not re-search the provider.
func (s *Server) terminalRetryElapsed(meta *catalog.AlbumMetadata) bool {
	t := meta.LastRefreshedAt
	if t.IsZero() {
		t = meta.UpdatedAt
	}
	if t.IsZero() {
		return true // no timestamp: treat as due rather than park forever
	}
	return time.Since(t) >= catalog.UnmatchedRetry
}

// albumStaleThreshold returns the soft TTL after which a warm album read is
// stale. It reads the injected live seam when present (app.go passes
// refresh.Service.AlbumStaleAfter, so it tracks hot-reloaded config), otherwise
// falls back to the engine default.
func (s *Server) albumStaleThreshold() time.Duration {
	if s.albumStaleAfter != nil {
		if d := s.albumStaleAfter(); d > 0 {
			return d
		}
	}
	return refresh.DefaultAlbumStaleAfter
}

// albumMetadataStale reports whether a materialized album's metadata is older
// than the soft TTL. The refresh time is preferred; a row with no refresh time
// falls back to its update time. A row with neither is treated as not stale.
func (s *Server) albumMetadataStale(meta *catalog.AlbumMetadata) bool {
	t := meta.LastRefreshedAt
	if t.IsZero() {
		t = meta.UpdatedAt
	}
	if t.IsZero() {
		return false
	}
	return time.Since(t) > s.albumStaleThreshold()
}

// enqueueStaleRefresh best-effort queues a low-priority background refresh behind
// the read response. The enqueue is a single short queue write — never a provider
// call and never a goroutine (AGENTS §2/§4) — so it does not block the read. A
// missing engine or a queue-write error is logged and never fails the request.
func (s *Server) enqueueStaleRefresh(ctx context.Context, albumID int64) {
	s.enqueueRefresh(ctx, albumID, catalog.ReasonStale)
}

// enqueueRefresh is the shared best-effort low-priority enqueue behind a read.
func (s *Server) enqueueRefresh(ctx context.Context, albumID int64, reason string) {
	if s.albumRefresh == nil {
		return
	}
	if err := s.albumRefresh.EnqueueAlbum(ctx, albumID, reason, catalog.PriorityLow); err != nil {
		s.log.Warn("enqueue refresh failed", "album_id", albumID, "reason", reason, "error", err, "component", "api")
	}
}

// materializedAlbumTracks reads the album's materialized expected rows from the
// catalog. A nil catalog (bare test server) or a read error yields no rows so a
// warm/cold decision never fails the request; the error is logged.
func (s *Server) materializedAlbumTracks(ctx context.Context, albumID int64) []catalog.AlbumTrack {
	if s.catalog == nil {
		return nil
	}
	rows, err := s.catalog.GetAlbumTracks(ctx, albumID)
	if err != nil {
		s.log.Warn("catalog album tracks read failed", "album_id", albumID, "error", err, "component", "api")
		return nil
	}
	return rows
}

// albumMetadata reads the album's metadata row (provider link + refresh
// bookkeeping). A nil catalog (bare test server) or a read error yields nil, so
// an absent row is indistinguishable from an unreadable one — both mean "not
// materialized".
func (s *Server) albumMetadata(ctx context.Context, albumID int64) *catalog.AlbumMetadata {
	if s.catalog == nil {
		return nil
	}
	meta, err := s.catalog.GetAlbumMetadata(ctx, albumID)
	if err != nil {
		s.log.Warn("catalog album metadata read failed", "album_id", albumID, "error", err, "component", "api")
		return nil
	}
	return meta
}

// albumScopeTracks returns the album's own local tracks and the artist's wider
// pool separately. The scoped merge uses the album set for (disc,track) and title
// matching (so a track from another album cannot satisfy this album's position)
// and the artist pool for globally unique ISRC matching (M9).
func (s *Server) albumScopeTracks(ctx context.Context, albumID, artistID int64) (albumLocal, artistLocal []domain.Track) {
	albumLocal, err := s.store.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		s.log.Warn("get tracks by album failed", "album_id", albumID, "error", err, "component", "api")
	}
	artistLocal, err = s.store.GetTracksByArtist(ctx, artistID)
	if err != nil {
		s.log.Warn("get tracks by artist failed", "artist_id", artistID, "error", err, "component", "api")
	}
	return albumLocal, artistLocal
}

// localTrackEntry maps a local library track to a downloaded discovery entry.
func localTrackEntry(t *domain.Track) discoveryTrackEntry {
	return discoveryTrackEntry{
		Title:          t.Title,
		TrackNumber:    t.TrackNumber,
		DurationMs:     t.Duration,
		Downloaded:     true,
		LibraryTrackID: t.ID,
		FilePath:       t.FilePath,
		FileSize:       t.FileSize,
		Bitrate:        t.Bitrate,
		Format:         formatFromPath(t.FilePath),
	}
}

// mergedTrackEntry maps one merged catalog/local row to a discovery entry.
func mergedTrackEntry(m *catalog.MergedTrack) discoveryTrackEntry {
	entry := discoveryTrackEntry{
		Title:       m.Title,
		TrackNumber: m.TrackNumber,
		DurationMs:  m.DurationMs,
		Downloaded:  m.Downloaded,
	}
	if m.Local != nil {
		entry.LibraryTrackID = m.Local.ID
		entry.FilePath = m.Local.FilePath
		entry.FileSize = m.Local.FileSize
		entry.Bitrate = m.Local.Bitrate
		entry.Format = formatFromPath(m.Local.FilePath)
	}
	return entry
}

// albumRefresher is the narrow slice of the catalog refresh engine the API
// needs for a manual metadata refresh: enqueue one album for background
// materialization. Declared here (not as the concrete *refresh.Service) so the
// api package depends on behavior, not construction (AGENTS §3). The engine is
// wired in cmd/groovearr/app.go via Server.SetAlbumRefresher.
type albumRefresher interface {
	EnqueueAlbum(ctx context.Context, albumID int64, reason string, priority int) error
}

// errRefreshUnavailable is returned (503) when no refresh engine is wired.
// app.go always wires one; this is the defensive guard for a Server built
// without it (e.g. in tests), so the endpoint degrades cleanly.
var errRefreshUnavailable = errors.New("refresh service not available")

// handleLibraryAlbumRefresh enqueues a high-priority metadata refresh for one
// album and returns 202 Accepted. It is thin by contract (AGENTS §4):
// parse/validate -> service -> writeJSON, with no goroutine and no direct store
// mutation. The refresh engine owns the background provider work, so the request
// never waits on a provider here; the queue is recorded and the response
// returns immediately.
//
// The album must exist locally before it is queued: refreshing an unknown id
// would enqueue a permanent terminal no-match row and churn the queue, so a
// missing album is a 404. The route is rate limited with the same shared
// "download" bucket as the sibling library-album actions (see handlers.go).
func (s *Server) handleLibraryAlbumRefresh(w http.ResponseWriter, r *http.Request) {
	albumID, err := strconv.ParseInt(r.PathValue("albumID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}
	// Validate the album exists before enqueueing. A nil store is only possible
	// on a bare test server; production always wires the library store.
	if s.store != nil {
		album, err := s.store.GetAlbum(r.Context(), albumID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("get album: %w", err))
			return
		}
		if album == nil {
			writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
			return
		}
	}
	if s.albumRefresh == nil {
		writeError(w, http.StatusServiceUnavailable, errRefreshUnavailable)
		return
	}
	if err := s.albumRefresh.EnqueueAlbum(r.Context(), albumID, catalog.ReasonManual, catalog.PriorityHigh); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

// handleLibraryAlbumDownloadMissing queues all undownloaded tracks for an album
// as pending downloads. Source resolution happens in the background via the
// monitor's resolvePendingSources — same pattern as playlist download-missing.
func (s *Server) handleLibraryAlbumDownloadMissing(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()

	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get album: %w", err))
		return
	}
	if album == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get artist: %w", err))
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	// Expected tracklist for the album, read from the materialized catalog merged
	// with local tracks — no provider call except the one-time Option-A cold
	// fetch buildAlbumDiscovery performs for a still-unmaterialized album.
	discovery, pending, err := getLibraryAlbumDiscoveryData(ctx, s, albumID, artist.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to load discovery data: %w", err))
		return
	}
	if pending {
		// The album has no materialized provider tracklist yet (cold, or a
		// terminal no-match awaiting its retry). A silent {"queued":0} would read
		// as "nothing missing"; queue a best-effort refresh and return a
		// distinguishable 202 so the UI can say the download will retry.
		//
		// A terminal album still inside its retry window is the one exception:
		// its parked queue row already carries the 90-day due_at, so enqueueing
		// here would overwrite that with now and let the engine re-search within
		// one interval. Every other pending case (never materialized, or terminal
		// with the retry elapsed) is genuinely due and is enqueued at now.
		parked := s.terminalRetryParked(s.albumMetadata(ctx, albumID))
		if !parked {
			s.enqueueRefresh(ctx, albumID, catalog.ReasonCold)
		}
		message := "album metadata is not materialized yet; a refresh was queued and the download will retry"
		if parked {
			message = "album metadata is not materialized yet; a refresh is already scheduled for its retry window and the download will retry"
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"queued":  0,
			"pending": true,
			"message": message,
		})
		return
	}

	queued := 0
	var errors []string
	for _, dt := range discovery {
		if dt.Downloaded {
			continue
		}
		_, err := s.downloadSvc.QueuePending(ctx, requesterID(ctx), requesterUsername(ctx), download.Meta{
			Artist:      artist.Name,
			Album:       album.Title,
			Title:       dt.Title,
			TrackNumber: dt.TrackNumber,
		})
		if err != nil {
			s.log.Warn("queue pending failed", "artist", artist.Name, "title", dt.Title, "error", err, "component", "api")
			errors = append(errors, fmt.Sprintf("%s: %v", dt.Title, err))
			continue
		}
		queued++
	}

	resp := map[string]any{"queued": queued}
	if len(errors) > 0 {
		resp["errors"] = errors
	}
	writeJSON(w, http.StatusOK, resp)
}

// discoveryTrackData is the internal representation used by handleLibraryAlbumDownloadMissing.
type discoveryTrackData struct {
	Title       string
	TrackNumber int
	Downloaded  bool
}

// getLibraryAlbumDiscoveryData returns the expected-track list for an album for
// the download-missing path, reusing buildAlbumDiscovery so there is a single
// catalog read + scoped merge path. It reads only the materialized catalog and
// the local library — no discovery provider is called beyond the one-time
// Option-A cold fetch buildAlbumDiscovery performs. A still-unmaterialized album
// (cold, or a terminal no-match) yields pending=true and no data, so the caller
// can return a distinguishable "will retry" response instead of a silent
// {"queued":0}.
func getLibraryAlbumDiscoveryData(ctx context.Context, s *Server, albumID, artistID int64) (data []discoveryTrackData, pending bool, err error) {
	resp := s.buildAlbumDiscovery(ctx, albumID, artistID)
	if resp.MetadataPending {
		return nil, true, nil
	}
	out := make([]discoveryTrackData, 0, len(resp.Tracks))
	for i := range resp.Tracks {
		out = append(out, discoveryTrackData{
			Title:       resp.Tracks[i].Title,
			TrackNumber: resp.Tracks[i].TrackNumber,
			Downloaded:  resp.Tracks[i].Downloaded,
		})
	}
	return out, false, nil
}

func (s *Server) handleLibraryArtist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	artist, err := s.store.GetArtist(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}
	// Compilation groupings show the placeholder avatar, never a portrait.
	if library.IsCompilationArtist(artist.Name) {
		artist.ThumbURL = ""
	} else if library.IsLocalArtistThumb(artist.ThumbURL) {
		artist.ThumbURL = fmt.Sprintf("/api/artist-image/%d", artist.ID)
	}
	writeJSON(w, http.StatusOK, artist)
}

func (s *Server) handleLibraryArtistAlbums(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	_, offset, limit := parsePagination(r)
	albums, err := s.store.GetAlbumsByArtist(r.Context(), id, offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.Album{}
	}
	writeJSON(w, http.StatusOK, albums)
}

func (s *Server) handleLibraryArtistTracks(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	tracks, err := s.store.GetTracksByArtist(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if tracks == nil {
		tracks = []domain.Track{}
	}
	writeJSON(w, http.StatusOK, tracks)
}

// handleCoverArt serves the cover.jpg image for an album.
func (s *Server) handleCoverArt(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()
	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	// Try to find the album directory from existing tracks.
	cfg := s.cfg.Get()
	var albumDir string
	tracks, _ := s.store.GetTracksByAlbum(ctx, albumID)
	if len(tracks) > 0 && tracks[0].FilePath != "" {
		albumDir = library.AlbumDirFromTrack(tracks[0].FilePath)
	} else {
		// Fall back to constructing the path from the folder template.
		resolver := library.NewPathResolver(cfg.Library.FolderTemplate)
		resolved := resolver.Resolve(library.ResolveArgs{
			Artist:    artist.Name,
			Album:     album.Title,
			Year:      album.Year,
			TrackNum:  1,
			Title:     "dummy",
			Ext:       "mp3",
			AlbumType: "Album",
		})
		if resolved == "" {
			writeError(w, http.StatusNotFound, fmt.Errorf("cannot resolve album path"))
			return
		}
		albumDir = filepath.Join(cfg.Library.LibraryPath, resolved)
	}

	// Defense-in-depth: ensure resolved path stays within library root.
	if cleanDir, cleanRoot := filepath.Clean(albumDir), filepath.Clean(cfg.Library.LibraryPath); !strings.HasPrefix(cleanDir, cleanRoot+string(os.PathSeparator)) && cleanDir != cleanRoot {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := serveLibraryImage(w, r, albumDir, coverImageNames); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Errorf("cover not found"))
			return
		}
		s.log.Error("cover serve failed", "album_id", albumID, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, err)
	}
}

// coverImageNames and artistImageNames are the local image filenames the
// library serves for albums and artists respectively. Both lists are shared
// with the scanner so extraction and serving agree on what counts as artwork.
var coverImageNames = library.CoverCandidates

var artistImageNames = library.ArtistImageNames

// serveLibraryImage opens the first existing file from candidates in dir and
// streams it to the client with caching headers. Returns os.ErrNotExist when
// none of the candidates exist.
func serveLibraryImage(w http.ResponseWriter, r *http.Request, dir string, candidates []string) error {
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		f, err := os.Open(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
		http.ServeContent(w, r, name, fi.ModTime(), f)
		f.Close()
		return nil
	}
	return os.ErrNotExist
}

// handleArtistImage serves the artist.jpg image from the artist's library directory.
func (s *Server) handleArtistImage(w http.ResponseWriter, r *http.Request) {
	artistIDStr := r.PathValue("artistID")
	artistID, err := strconv.ParseInt(artistIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}

	ctx := r.Context()
	artist, err := s.store.GetArtist(ctx, artistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	cfg := s.cfg.Get()
	var artistDir string

	// Try to find the artist directory from existing tracks.
	tracks, _ := s.store.GetTracksByArtist(ctx, artistID)
	if len(tracks) > 0 && tracks[0].FilePath != "" {
		// Tracks live at {artist}/{album}/track.ext (or {artist}/{album}/Disc N/track.ext).
		dir := library.ArtistDirFromTrack(tracks[0].FilePath)
		// Flat layout ({root}/{Artist}/track.ext) has no album level, so
		// ArtistDirFromTrack resolves to the library root. Resolve to the
		// track's own folder instead, mirroring the scanner — otherwise every
		// flat-layout artist would serve an artist.jpg dropped in the root.
		if filepath.Clean(dir) == filepath.Clean(cfg.Library.LibraryPath) {
			dir = library.AlbumDirFromTrack(tracks[0].FilePath)
		}
		// Only serve from the artist's own folder. A shared compilation
		// grouping (e.g. "Various Artists") would leak one artist's image onto
		// every artist whose albums live there. A folder that merely differs
		// in name from the stored artist is still served — it's the track's
		// real home.
		if !library.IsCompilationDir(dir) {
			artistDir = dir
		}
	}
	if artistDir == "" {
		// Fallback: construct from library root + artist name.
		artistDir = filepath.Join(cfg.Library.LibraryPath, artist.Name)
	}

	// Defense-in-depth: ensure resolved path stays within library root.
	if cleanDir, cleanRoot := filepath.Clean(artistDir), filepath.Clean(cfg.Library.LibraryPath); !strings.HasPrefix(cleanDir, cleanRoot+string(os.PathSeparator)) && cleanDir != cleanRoot {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := serveLibraryImage(w, r, artistDir, artistImageNames); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Errorf("artist image not found"))
			return
		}
		s.log.Error("artist image serve failed", "artist_id", artistID, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, err)
	}
}
