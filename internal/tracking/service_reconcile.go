package tracking

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
)

// AddArtistOptions controls how a newly tracked artist is monitored. The zero
// value is the sensible default: monitored, full discography, and auto-refresh
// following the tracking.refresh_mins setting.
type AddArtistOptions struct {
	// Monitored toggles acquisition for the artist. nil defaults to true.
	Monitored *bool
	// MonitorMode selects the monitoring scope. Empty defaults to
	// domain.MonitorModeAll.
	MonitorMode domain.MonitorMode
	// AutoRefresh enables the periodic discography refresh loop. nil falls
	// back to whether tracking.refresh_mins enables the loop.
	AutoRefresh *bool
	// SearchOnAdd queues the artist's missing (wanted) albums once, right
	// after the initial reconcile. It mirrors Lidarr's "Start Search for
	// Missing Albums" checkbox and defaults to false: a normal add only builds
	// the wanted list, leaving acquisition to the periodic refresh/search job.
	// The post-add search is best-effort; a failure never fails the add.
	SearchOnAdd bool
}

// addArtistTimeout bounds the synchronous discography fetch AddArtist performs
// so a hung provider cannot tie up an HTTP request indefinitely. It is a var so
// tests can shorten it.
var addArtistTimeout = 30 * time.Second

// AddArtist starts tracking an artist. It is idempotent: when a row already
// exists for the provider pair, that row is reconciled in place (monitor
// settings updated, discography refreshed) instead of being duplicated.
//
// The provider must already be registered with the discovery registry; a
// cooling-down provider is skipped without any provider call. The discography
// fetch is bounded by addArtistTimeout so the synchronous call cannot hang.
func (s *Service) AddArtist(ctx context.Context, providerName, providerArtistID, name string, opts AddArtistOptions) (*domain.TrackedArtist, error) {
	if providerName == "" || providerArtistID == "" {
		return nil, errors.New("tracking: provider name and artist ID are required")
	}
	if s.discoveryReg == nil {
		return nil, errors.New("tracking: discovery registry not available")
	}
	provider := s.discoveryReg.Get(providerName)
	if provider == nil {
		return nil, fmt.Errorf("%w: %q", ErrProviderNotRegistered, providerName)
	}
	if s.coolingDown(providerName) {
		return nil, fmt.Errorf("%w: %q", ErrProviderCoolingDown, providerName)
	}

	mode := opts.mode()
	monitored := opts.monitored(mode)

	// Fetch the discography before writing anything: a bounded-fetch failure
	// must leave no orphan artist row behind, so the only side effect of a
	// failed add is a provider call. Re-adding an existing artist still
	// reconciles it because upsertArtist updates the row in place.
	fetchCtx, cancel := context.WithTimeout(ctx, addArtistTimeout)
	defer cancel()
	albums, err := s.fetchArtistAlbums(fetchCtx, provider, providerName, providerArtistID)
	if err != nil {
		return nil, fmt.Errorf("add artist %s/%s: %w", providerName, providerArtistID, err)
	}

	artist, err := s.upsertArtist(ctx, providerName, providerArtistID, name, monitored, mode, opts)
	if err != nil {
		return nil, err
	}
	if err := s.ReconcileAlbums(ctx, artist.ID, albums); err != nil {
		return nil, err
	}

	// Re-read after reconcile so the caller's response reflects the persisted
	// row rather than the stale pre-reconcile snapshot.
	resolved, err := s.resolveArtist(ctx, artist.ID, artist)
	if err != nil {
		return nil, err
	}
	if opts.SearchOnAdd {
		s.searchOnAdd(ctx, artist.ID)
	}
	return resolved, nil
}

// resolveArtist re-reads a tracked artist after reconcile so a caller's
// response reflects the persisted row (ReconcileAlbums resolved the library
// link on its own copy). It falls back to the pre-reconcile snapshot when the
// row has since vanished, preserving AddArtist's prior behavior.
func (s *Service) resolveArtist(ctx context.Context, id int64, fallback *domain.TrackedArtist) (*domain.TrackedArtist, error) {
	resolved, err := s.store.GetTrackedArtist(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get tracked artist %d: %w", id, err)
	}
	if resolved != nil {
		return resolved, nil
	}
	return fallback, nil
}

// searchOnAdd runs the opt-in post-add SearchMissing. The add already
// succeeded, so a search failure is logged and swallowed rather than failing
// the whole operation.
func (s *Service) searchOnAdd(ctx context.Context, artistID int64) {
	if _, err := s.SearchMissing(ctx, artistID); err != nil {
		s.log.Warn("add artist search-on-add failed", "artist_id", artistID, "error", err, "component", "tracking")
	}
}

// upsertArtist finds the row for the provider pair and refreshes its
// provider-owned name and monitor settings, or creates a new tracked artist.
// AutoRefresh is only applied at creation: the store offers no standalone
// updater for it.
func (s *Service) upsertArtist(ctx context.Context, providerName, providerArtistID, name string, monitored bool, mode domain.MonitorMode, opts AddArtistOptions) (*domain.TrackedArtist, error) {
	existing, err := s.store.GetTrackedArtistByProvider(ctx, providerName, providerArtistID)
	if err != nil {
		return nil, fmt.Errorf("get tracked artist %s/%s: %w", providerName, providerArtistID, err)
	}
	if existing != nil {
		if existing.Monitored != monitored || existing.MonitorMode != mode {
			if err := s.store.UpdateArtistMonitor(ctx, existing.ID, monitored, mode); err != nil {
				return nil, fmt.Errorf("update artist monitor %d: %w", existing.ID, err)
			}
			existing.Monitored = monitored
			existing.MonitorMode = mode
		}
		// The provider is authoritative for the display name: re-adding a
		// provider pair with a corrected name (e.g. a disambiguation suffix)
		// must persist it instead of keeping the stale first-seen name.
		if existing.Name != name {
			if err := s.store.UpdateArtistName(ctx, existing.ID, name); err != nil {
				return nil, fmt.Errorf("update artist name %d: %w", existing.ID, err)
			}
			existing.Name = name
		}
		return existing, nil
	}

	artist := &domain.TrackedArtist{
		Name:             name,
		ProviderName:     providerName,
		ProviderArtistID: providerArtistID,
		Monitored:        monitored,
		MonitorMode:      mode,
		AutoRefresh:      opts.autoRefresh(s.cfg()),
	}
	id, err := s.store.CreateTrackedArtist(ctx, artist)
	if err != nil {
		return nil, fmt.Errorf("create tracked artist %s/%s: %w", providerName, providerArtistID, err)
	}
	artist.ID = id
	created, err := s.store.GetTrackedArtist(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get tracked artist %d: %w", id, err)
	}
	if created != nil {
		return created, nil
	}
	return artist, nil
}

// ReconcileAlbums upserts every discovered release and links it to the local
// library. Existing rows keep their status and monitored flag unless a fresh
// library match promotes them to downloaded. Library records are never
// mutated. The library artist and its album candidates are resolved once for
// the whole pass rather than per release.
func (s *Service) ReconcileAlbums(ctx context.Context, artistID int64, albums []discovery.AlbumResult) error {
	artist, err := s.store.GetTrackedArtist(ctx, artistID)
	if err != nil {
		return fmt.Errorf("get tracked artist %d: %w", artistID, err)
	}
	if artist == nil {
		return fmt.Errorf("tracking: tracked artist %d not found", artistID)
	}

	lib, err := s.resolveLibraryMatches(ctx, artist)
	if err != nil {
		return err
	}

	nowYear := time.Now().UTC().Year()
	for _, result := range albums {
		if err := s.reconcileAlbum(ctx, artist, result, lib, nowYear); err != nil {
			return err
		}
	}
	if err := s.store.TouchArtistRefreshed(ctx, artistID); err != nil {
		return fmt.Errorf("touch artist refreshed %d: %w", artistID, err)
	}
	return nil
}

// libraryMatches is the per-reconcile library snapshot: the resolved local
// artist (0 when unmatched) and its album candidates. It is computed once per
// ReconcileAlbums so a discography does not trigger one artist lookup and one
// album list per release (N+1), and is never cached across calls.
type libraryMatches struct {
	artistID   int64
	candidates []domain.Album
}

// resolveLibraryMatches resolves the tracked artist's local counterpart and its
// album candidates once. When the link is discovered by name it is persisted
// (only when the stored link is nil or different) so later reconciles skip the
// name lookup and the linkage survives independent of a successful match.
func (s *Service) resolveLibraryMatches(ctx context.Context, artist *domain.TrackedArtist) (*libraryMatches, error) {
	libArtistID, err := s.libraryArtistID(ctx, artist)
	if err != nil {
		return nil, err
	}
	if libArtistID == 0 {
		return &libraryMatches{}, nil
	}
	if artist.LibraryArtistID == nil || *artist.LibraryArtistID != libArtistID {
		if err := s.store.UpdateArtistLibraryLink(ctx, artist.ID, libArtistID); err != nil {
			return nil, fmt.Errorf("update artist library link %d: %w", artist.ID, err)
		}
		linked := libArtistID
		artist.LibraryArtistID = &linked
	}
	candidates, err := s.libStore.GetAlbumsByArtist(ctx, libArtistID)
	if err != nil {
		return nil, fmt.Errorf("list library albums for artist %d: %w", libArtistID, err)
	}
	return &libraryMatches{artistID: libArtistID, candidates: candidates}, nil
}

// reconcileAlbum upserts a single discovered release, preserving the existing
// row's acquisition state where one is already recorded.
func (s *Service) reconcileAlbum(ctx context.Context, artist *domain.TrackedArtist, result discovery.AlbumResult, lib *libraryMatches, nowYear int) error {
	existing, err := s.store.GetTrackedAlbumByProvider(ctx, artist.ID, result.ProviderID)
	if err != nil {
		return fmt.Errorf("get tracked album %q: %w", result.ProviderID, err)
	}

	libID := libraryLink(existing)
	if libID == nil {
		libID, err = s.matchLibraryAlbum(ctx, artist, result, lib)
		if err != nil {
			return err
		}
	}

	album := &domain.TrackedAlbum{
		TrackedArtistID: artist.ID,
		ProviderAlbumID: result.ProviderID,
		ProviderName:    result.ProviderName,
		Title:           result.Title,
		Year:            result.Year,
		AlbumType:       result.Type,
		Monitored:       resolveMonitored(artist, existing, result.Year, nowYear),
		Status:          resolveStatus(existing, libID != nil),
		LibraryAlbumID:  libID,
	}
	if _, err := s.store.UpsertTrackedAlbum(ctx, album); err != nil {
		return fmt.Errorf("upsert tracked album %q: %w", result.ProviderID, err)
	}
	return nil
}

// matchLibraryAlbum links a discovered album to an existing library album,
// matching external ID first and falling back to a normalized
// artist|title|year comparison against the candidates resolved once per
// reconcile. It only reads from the library.
func (s *Service) matchLibraryAlbum(ctx context.Context, artist *domain.TrackedArtist, result discovery.AlbumResult, lib *libraryMatches) (*int64, error) {
	if result.ProviderID != "" {
		album, err := s.libStore.GetAlbumByExternalID(ctx, result.ProviderName, result.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("match album external id %s/%s: %w", result.ProviderName, result.ProviderID, err)
		}
		if album != nil {
			return &album.ID, nil
		}
	}

	if lib == nil || lib.artistID == 0 {
		return nil, nil
	}

	discArtist := result.ArtistName
	if discArtist == "" {
		discArtist = artist.Name
	}
	return matchCandidate(lib.candidates, artist.Name, discArtist, result.Title, result.Year), nil
}

// matchCandidate scans library album candidates for the first whose normalized
// artist|title|year equals the discovered release, returning its ID or nil.
// Both reconcile and the post-import link share this one matcher so a provider
// match and an import match can never drift apart.
func matchCandidate(candidates []domain.Album, libArtist, discArtist, discTitle string, discYear int) *int64 {
	for i := range candidates {
		if albumMatches(libArtist, candidates[i].Title, candidates[i].Year, discArtist, discTitle, discYear) {
			id := candidates[i].ID
			return &id
		}
	}
	return nil
}

// LinkImportedAlbum reconciles a completed import back onto tracking. It finds
// tracked albums for artistName|albumTitle (normalized artist+title via the
// same albumMatches matcher reconcile uses) and promotes every match that is
// not already downloaded or ignored to downloaded, carrying the resolved
// library link. It is the contract the post-import hook calls; it does not wire
// the import chain itself. No matching tracked album is a no-op (nil error), so
// an import of an untracked release is silent.
func (s *Service) LinkImportedAlbum(ctx context.Context, artistName, albumTitle string) error {
	if normalizeAlbumText(artistName) == "" || normalizeAlbumText(albumTitle) == "" {
		return nil
	}
	artists, err := s.store.ListTrackedArtists(ctx)
	if err != nil {
		return fmt.Errorf("list tracked artists: %w", err)
	}
	for i := range artists {
		if normalizeAlbumText(artists[i].Name) != normalizeAlbumText(artistName) {
			continue
		}
		if err := s.linkImportedForArtist(ctx, &artists[i], artistName, albumTitle); err != nil {
			return err
		}
	}
	return nil
}

// linkImportedForArtist promotes the matching albums owned by one tracked
// artist. The library link is resolved once per artist, not per album.
func (s *Service) linkImportedForArtist(ctx context.Context, artist *domain.TrackedArtist, artistName, albumTitle string) error {
	albums, err := s.store.ListTrackedAlbums(ctx, artist.ID)
	if err != nil {
		return fmt.Errorf("list tracked albums %d: %w", artist.ID, err)
	}
	link, err := s.resolveImportedLibraryLink(ctx, artistName, albumTitle)
	if err != nil {
		return err
	}
	for _, album := range albums {
		if album.Status == domain.AlbumStatusDownloaded || album.Status == domain.AlbumStatusIgnored {
			continue
		}
		if !albumMatches(artistName, albumTitle, 0, artist.Name, album.Title, album.Year) {
			continue
		}
		if err := s.promoteImported(ctx, artist.ID, album, link); err != nil {
			return err
		}
	}
	return nil
}

// resolveImportedLibraryLink resolves the local album ID for an imported
// artist|album by matching the library artist's candidates with the shared
// matcher. It returns nil when the library side cannot be resolved — the import
// hook is not required to have linked the artist, and a missing link must not
// block the promotion.
func (s *Service) resolveImportedLibraryLink(ctx context.Context, artistName, albumTitle string) (*int64, error) {
	if s.libStore == nil {
		return nil, nil
	}
	libArtist, err := s.libStore.GetArtistByName(ctx, artistName)
	if err != nil {
		return nil, fmt.Errorf("lookup library artist %q: %w", artistName, err)
	}
	if libArtist == nil {
		return nil, nil
	}
	candidates, err := s.libStore.GetAlbumsByArtist(ctx, libArtist.ID)
	if err != nil {
		return nil, fmt.Errorf("list library albums for artist %d: %w", libArtist.ID, err)
	}
	return matchCandidate(candidates, artistName, artistName, albumTitle, 0), nil
}

// promoteImported forwards a matching tracked album through UpsertTrackedAlbum
// with status downloaded and the resolved library link, so the store's
// forward-only CASE does the promotion and preserves the existing monitored
// flag. A nil link still promotes; the link is a best-effort enrichment.
func (s *Service) promoteImported(ctx context.Context, artistID int64, album domain.TrackedAlbum, link *int64) error {
	promoted := album
	promoted.TrackedArtistID = artistID
	promoted.Status = domain.AlbumStatusDownloaded
	if link != nil {
		promoted.LibraryAlbumID = link
	}
	if _, err := s.store.UpsertTrackedAlbum(ctx, &promoted); err != nil {
		return fmt.Errorf("link imported album %d: %w", album.ID, err)
	}
	return nil
}

// libraryArtistID resolves the library artist for a tracked artist, preferring
// the stored link and falling back to an exact name lookup.
func (s *Service) libraryArtistID(ctx context.Context, artist *domain.TrackedArtist) (int64, error) {
	if artist.LibraryArtistID != nil && *artist.LibraryArtistID != 0 {
		return *artist.LibraryArtistID, nil
	}
	if artist.Name == "" {
		return 0, nil
	}
	libArtist, err := s.libStore.GetArtistByName(ctx, artist.Name)
	if err != nil {
		return 0, fmt.Errorf("lookup library artist %q: %w", artist.Name, err)
	}
	if libArtist == nil {
		return 0, nil
	}
	return libArtist.ID, nil
}

// ListTrackedArtists returns every tracked artist. It delegates to the store
// and is the listing half of the jobs.TrackedRefresher contract consumed by
// the refresh-tracked job.
func (s *Service) ListTrackedArtists(ctx context.Context) ([]domain.TrackedArtist, error) {
	artists, err := s.store.ListTrackedArtists(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tracked artists: %w", err)
	}
	return artists, nil
}

// ListWanted returns the artist's monitored albums still waiting to be
// acquired (status wanted).
func (s *Service) ListWanted(ctx context.Context, artistID int64) ([]domain.TrackedAlbum, error) {
	albums, err := s.store.ListTrackedAlbums(ctx, artistID)
	if err != nil {
		return nil, fmt.Errorf("list tracked albums %d: %w", artistID, err)
	}
	wanted := make([]domain.TrackedAlbum, 0, len(albums))
	for _, album := range albums {
		if album.Monitored && album.Status == domain.AlbumStatusWanted {
			wanted = append(wanted, album)
		}
	}
	return wanted, nil
}

// monitored resolves the artist-level monitored flag. MonitorModeNone always
// wins: mode "none" means the artist is tracked without acquisition.
func (o AddArtistOptions) monitored(mode domain.MonitorMode) bool {
	if mode == domain.MonitorModeNone {
		return false
	}
	if o.Monitored == nil {
		return true
	}
	return *o.Monitored
}

func (o AddArtistOptions) mode() domain.MonitorMode {
	if o.MonitorMode == "" {
		return domain.MonitorModeAll
	}
	return o.MonitorMode
}

func (o AddArtistOptions) autoRefresh(cfg config.Config) bool {
	if o.AutoRefresh != nil {
		return *o.AutoRefresh
	}
	return cfg.Tracking.RefreshMins != nil && *cfg.Tracking.RefreshMins > 0
}

func libraryLink(a *domain.TrackedAlbum) *int64 {
	if a == nil {
		return nil
	}
	return a.LibraryAlbumID
}

// resolveMonitored picks the monitored flag for a reconciled album. An
// existing row keeps its current flag, so a user toggling a single album off
// is not overridden by the next refresh (the store also omits monitored from
// its conflict update). A brand-new row is derived from the artist-level
// desiredMonitored rule.
func resolveMonitored(artist *domain.TrackedArtist, existing *domain.TrackedAlbum, year, nowYear int) bool {
	if existing != nil {
		return existing.Monitored
	}
	return desiredMonitored(artist.Monitored, artist.MonitorMode, year, nowYear)
}

// desiredMonitored is the artist-aware, state-free monitoring rule:
//   - an unmonitored artist, or mode none, monitors nothing (the artist is
//     tracked without acquisition);
//   - mode future monitors only releases dated after nowYear, so the
//     back-catalogue surfaced at add time stays unmonitored while genuinely
//     new releases are picked up;
//   - all (or unset) monitors every release.
func desiredMonitored(artistMonitored bool, mode domain.MonitorMode, year, nowYear int) bool {
	if !artistMonitored || mode == domain.MonitorModeNone {
		return false
	}
	if mode == domain.MonitorModeFuture {
		return year > nowYear
	}
	return true
}

// resolveStatus picks the acquisition status for a reconciled album: a library
// match promotes a wanted/downloading row to downloaded; an existing row keeps
// its status; a brand-new row starts wanted. The store's upsert CASE enforces
// the same precedence, and it wins for the terminal states: a stored ignored
// (or downloaded) never regresses to the incoming value, so an existing ignored
// album stays ignored even when a library match would otherwise promote it.
func resolveStatus(existing *domain.TrackedAlbum, matched bool) domain.AlbumStatus {
	if matched {
		return domain.AlbumStatusDownloaded
	}
	if existing != nil {
		return existing.Status
	}
	return domain.AlbumStatusWanted
}

// albumMatches reports whether a library album is the same release as a
// discovered result, comparing normalized artist and title and treating a year
// as authoritative only when both sides carry one.
func albumMatches(libArtist, libTitle string, libYear int, discArtist, discTitle string, discYear int) bool {
	if normalizeAlbumText(libTitle) != normalizeAlbumText(discTitle) {
		return false
	}
	if discArtist != "" && normalizeAlbumText(libArtist) != normalizeAlbumText(discArtist) {
		return false
	}
	if libYear != 0 && discYear != 0 && libYear != discYear {
		return false
	}
	return true
}

// normalizeAlbumText lowercases and collapses every run of non-alphanumeric
// characters into a single space, so punctuation and case differences between
// a provider title and a library title do not break matching.
func normalizeAlbumText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			pendingSpace = false
			continue
		}
		pendingSpace = true
	}
	return b.String()
}
