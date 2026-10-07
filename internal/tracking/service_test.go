package tracking

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/plugin"
)

// ─── Fakes ────────────────────────────────────────────────────────────

// mockStore is a hand-written in-memory Store.
type mockStore struct {
	artists          map[int64]*domain.TrackedArtist
	byProvider       map[string]int64
	albums           map[int64][]domain.TrackedAlbum
	nextArtistID     int64
	nextAlbumID      int64
	monitorCalls     int
	libraryLinkCalls int
	nameUpdateCalls  int
	touchCalls       int
	searchedIDs      []int64
}

func newMockStore() *mockStore {
	return &mockStore{
		artists:    make(map[int64]*domain.TrackedArtist),
		byProvider: make(map[string]int64),
		albums:     make(map[int64][]domain.TrackedAlbum),
	}
}

func providerKey(providerName, providerArtistID string) string {
	return providerName + "/" + providerArtistID
}

func (m *mockStore) CreateTrackedArtist(_ context.Context, a *domain.TrackedArtist) (int64, error) {
	m.nextArtistID++
	cp := *a
	cp.ID = m.nextArtistID
	m.artists[cp.ID] = &cp
	m.byProvider[providerKey(a.ProviderName, a.ProviderArtistID)] = cp.ID
	return cp.ID, nil
}

func (m *mockStore) GetTrackedArtist(_ context.Context, id int64) (*domain.TrackedArtist, error) {
	a := m.artists[id]
	if a == nil {
		return nil, nil
	}
	cp := *a
	return &cp, nil
}

func (m *mockStore) GetTrackedArtistByProvider(_ context.Context, providerName, providerArtistID string) (*domain.TrackedArtist, error) {
	id, ok := m.byProvider[providerKey(providerName, providerArtistID)]
	if !ok {
		return nil, nil
	}
	return m.GetTrackedArtist(context.Background(), id)
}

func (m *mockStore) ListTrackedArtists(_ context.Context) ([]domain.TrackedArtist, error) {
	out := make([]domain.TrackedArtist, 0, len(m.artists))
	for _, a := range m.artists {
		out = append(out, *a)
	}
	return out, nil
}

func (m *mockStore) UpdateArtistMonitor(_ context.Context, id int64, monitored bool, mode domain.MonitorMode) error {
	m.monitorCalls++
	if a := m.artists[id]; a != nil {
		a.Monitored = monitored
		a.MonitorMode = mode
	}
	return nil
}

func (m *mockStore) DeleteTrackedArtist(_ context.Context, id int64) error {
	delete(m.artists, id)
	delete(m.albums, id)
	return nil
}

// UpsertTrackedAlbum mirrors internal/tracking/sqlite.Store.UpsertTrackedAlbum
// exactly: provider metadata is refreshed, status advances forward-only,
// monitored is never touched on conflict, a nil library link never clears an
// existing one, and first_seen_at survives. Keeping the mock faithful means
// service unit tests exercise the same lifecycle the real store enforces.
func (m *mockStore) UpsertTrackedAlbum(_ context.Context, a *domain.TrackedAlbum) (int64, error) {
	albumType := a.AlbumType
	if albumType == "" {
		albumType = string(domain.AlbumTypeAlbum)
	}
	status := a.Status
	if status == "" {
		status = domain.AlbumStatusWanted
	}

	list := m.albums[a.TrackedArtistID]
	for i := range list {
		if list[i].ProviderAlbumID == a.ProviderAlbumID {
			updated := list[i] // preserves ID, Monitored, FirstSeenAt
			updated.ProviderName = a.ProviderName
			updated.Title = a.Title
			updated.Year = a.Year
			updated.AlbumType = albumType
			updated.Status = advanceStatus(list[i].Status, status)
			if a.LibraryAlbumID != nil {
				updated.LibraryAlbumID = a.LibraryAlbumID
			}
			updated.LastSeenAt = a.LastSeenAt
			list[i] = updated
			m.albums[a.TrackedArtistID] = list
			return updated.ID, nil
		}
	}

	m.nextAlbumID++
	cp := *a
	cp.ID = m.nextAlbumID
	cp.AlbumType = albumType
	cp.Status = status
	m.albums[a.TrackedArtistID] = append(list, cp)
	return cp.ID, nil
}

// advanceStatus is the in-memory twin of the real store's forward-only CASE:
// a stored downloaded or ignored status never regresses, an incoming
// downloaded promotes, and otherwise the stored status wins.
func advanceStatus(existing, incoming domain.AlbumStatus) domain.AlbumStatus {
	if existing == domain.AlbumStatusDownloaded || existing == domain.AlbumStatusIgnored {
		return existing
	}
	if incoming == domain.AlbumStatusDownloaded {
		return domain.AlbumStatusDownloaded
	}
	return existing
}

func (m *mockStore) GetTrackedAlbumByProvider(_ context.Context, artistID int64, providerAlbumID string) (*domain.TrackedAlbum, error) {
	for _, a := range m.albums[artistID] {
		if a.ProviderAlbumID == providerAlbumID {
			cp := a
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockStore) ListTrackedAlbums(_ context.Context, artistID int64) ([]domain.TrackedAlbum, error) {
	list := m.albums[artistID]
	out := make([]domain.TrackedAlbum, len(list))
	copy(out, list)
	return out, nil
}

func (m *mockStore) ListWantedAlbums(_ context.Context) ([]domain.TrackedAlbum, error) {
	var out []domain.TrackedAlbum
	for _, list := range m.albums {
		for _, a := range list {
			if a.Monitored && (a.Status == domain.AlbumStatusWanted || a.Status == domain.AlbumStatusDownloading) {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

func (m *mockStore) UpdateAlbumMonitor(_ context.Context, albumID int64, monitored bool) error {
	for id, list := range m.albums {
		for i := range list {
			if list[i].ID == albumID {
				list[i].Monitored = monitored
				m.albums[id] = list
			}
		}
	}
	return nil
}

func (m *mockStore) UpdateArtistLibraryLink(_ context.Context, artistID int64, libraryArtistID int64) error {
	m.libraryLinkCalls++
	if a := m.artists[artistID]; a != nil {
		link := libraryArtistID
		a.LibraryArtistID = &link
	}
	return nil
}

func (m *mockStore) UpdateArtistName(_ context.Context, artistID int64, name string) error {
	m.nameUpdateCalls++
	if a := m.artists[artistID]; a != nil {
		a.Name = name
	}
	return nil
}

func (m *mockStore) MarkAlbumStatus(_ context.Context, albumID int64, status domain.AlbumStatus) error {
	for id, list := range m.albums {
		for i := range list {
			if list[i].ID == albumID {
				list[i].Status = status
				m.albums[id] = list
			}
		}
	}
	return nil
}

// MarkAlbumSearched mirrors the real store: it stamps last_searched_at on the
// album and records the ID so tests can assert every processed album is marked.
func (m *mockStore) MarkAlbumSearched(_ context.Context, albumID int64) error {
	m.searchedIDs = append(m.searchedIDs, albumID)
	now := time.Now().UTC()
	for id, list := range m.albums {
		for i := range list {
			if list[i].ID == albumID {
				list[i].LastSearchedAt = &now
				m.albums[id] = list
			}
		}
	}
	return nil
}

// GetTrackedAlbum returns the album with the given internal ID, or (nil, nil).
func (m *mockStore) GetTrackedAlbum(_ context.Context, albumID int64) (*domain.TrackedAlbum, error) {
	for _, list := range m.albums {
		for i := range list {
			if list[i].ID == albumID {
				cp := list[i]
				return &cp, nil
			}
		}
	}
	return nil, nil
}

func (m *mockStore) TouchArtistRefreshed(_ context.Context, _ int64) error {
	m.touchCalls++
	return nil
}

// mockLibraryStore embeds library.Store so only the read methods the service
// uses need real behavior; any unexpected call would panic on the nil embed.
type mockLibraryStore struct {
	library.Store
	albumByExt          map[string]*domain.Album
	albumsByArtist      map[int64][]domain.Album
	artistByName        map[string]*domain.Artist
	artistByNameCalls   int
	albumsByArtistCalls int
}

func newMockLibraryStore() *mockLibraryStore {
	return &mockLibraryStore{
		albumByExt:     make(map[string]*domain.Album),
		albumsByArtist: make(map[int64][]domain.Album),
		artistByName:   make(map[string]*domain.Artist),
	}
}

func (m *mockLibraryStore) GetAlbumByExternalID(_ context.Context, service, externalID string) (*domain.Album, error) {
	return m.albumByExt[service+"/"+externalID], nil
}

func (m *mockLibraryStore) GetArtistByName(_ context.Context, name string) (*domain.Artist, error) {
	m.artistByNameCalls++
	return m.artistByName[name], nil
}

func (m *mockLibraryStore) GetAlbumsByArtist(_ context.Context, artistID int64) ([]domain.Album, error) {
	m.albumsByArtistCalls++
	return m.albumsByArtist[artistID], nil
}

// fakeDiscoveryProvider is a scriptable discovery.Provider.
type fakeDiscoveryProvider struct {
	name        string
	albums      []discovery.AlbumResult
	albumsErr   error
	albumTracks []discovery.TrackInfo
	tracksErr   error
	blockAlbums bool
	albumCalls  int
	searchCalls int
	trackCalls  int
}

var _ discovery.Provider = (*fakeDiscoveryProvider)(nil)

func (f *fakeDiscoveryProvider) Name() string        { return f.name }
func (f *fakeDiscoveryProvider) DisplayName() string { return f.name }
func (f *fakeDiscoveryProvider) IsConfigured() bool  { return true }
func (f *fakeDiscoveryProvider) Connected() bool     { return true }
func (f *fakeDiscoveryProvider) CapabilityStatus() map[string]string {
	return map[string]string{"discovery": "connected"}
}
func (f *fakeDiscoveryProvider) CheckConnection(context.Context) error { return nil }
func (f *fakeDiscoveryProvider) SearchArtists(context.Context, string, int) ([]discovery.ArtistSummary, error) {
	f.searchCalls++
	return nil, nil
}
func (f *fakeDiscoveryProvider) GetArtistAlbums(ctx context.Context, _ string, _ int) ([]discovery.AlbumResult, error) {
	f.albumCalls++
	if f.blockAlbums {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.albums, f.albumsErr
}
func (f *fakeDiscoveryProvider) GetAlbumTracks(context.Context, string) ([]discovery.TrackInfo, error) {
	f.trackCalls++
	return f.albumTracks, f.tracksErr
}
func (f *fakeDiscoveryProvider) SearchAlbums(context.Context, string, int) ([]discovery.AlbumResult, error) {
	return nil, nil
}

// fakeQueuer implements DownloadQueuer and ActiveDownloadFinder so a single
// value backs the delegated queue call and the dedup/re-arm record lookup. It
// records the exact delegation arguments and returns a configurable canonical
// result; it deliberately does NOT re-implement the album-first policy (that
// lives in internal/download and is covered by its own tests).
type fakeQueuer struct {
	result   download.AlbumQueueResult
	queueErr error
	records  []download.Record
	// resultByAlbum overrides result for a specific album title, letting tests
	// model the canonical policy's no-op outcome (Queued=0, Errors empty).
	resultByAlbum map[string]download.AlbumQueueResult

	calls    []queuerCall
	retried  []string
	retryErr error
}

// queuerCall captures one delegated QueueAlbumWithFallback invocation.
type queuerCall struct {
	requestedByUserID   int64
	requestedByUsername string
	artist              string
	album               string
	tracks              []download.TrackQueue
	downloadClient      string
	albumSources        []string
}

var (
	_ DownloadQueuer       = (*fakeQueuer)(nil)
	_ ActiveDownloadFinder = (*fakeQueuer)(nil)
)

func (f *fakeQueuer) QueueAlbumWithFallback(_ context.Context, requestedByUserID int64, requestedByUsername, artist, album string, tracks []download.TrackQueue, downloadClient string, albumSources []string) (download.AlbumQueueResult, error) {
	f.calls = append(f.calls, queuerCall{requestedByUserID: requestedByUserID, requestedByUsername: requestedByUsername, artist: artist, album: album, tracks: tracks, downloadClient: downloadClient, albumSources: albumSources})
	if f.queueErr != nil {
		return f.result, f.queueErr
	}
	if r, ok := f.resultByAlbum[album]; ok {
		return r, nil
	}
	return f.result, nil
}

func (f *fakeQueuer) List(_ context.Context) ([]download.Record, error) {
	out := make([]download.Record, len(f.records))
	copy(out, f.records)
	return out, nil
}

func (f *fakeQueuer) Retry(_ context.Context, id string) error {
	if f.retryErr != nil {
		return f.retryErr
	}
	f.retried = append(f.retried, id)
	return nil
}

// fakeRateLimiter records cooldown state.
type fakeRateLimiter struct {
	cooling map[string]bool
	marked  map[string]time.Duration
}

func newFakeRateLimiter() *fakeRateLimiter {
	return &fakeRateLimiter{cooling: map[string]bool{}, marked: map[string]time.Duration{}}
}

func (f *fakeRateLimiter) CoolingDown(name string) bool { return f.cooling[name] }
func (f *fakeRateLimiter) MarkAfter(name string, retryAfter time.Duration) {
	f.marked[name] = retryAfter
}

func newTestService(t *testing.T, store Store, lib library.Store, provider discovery.Provider, rl RateLimiter, cfg config.Config) *Service {
	t.Helper()
	return newTestServiceWith(t, store, lib, provider, rl, nil, cfg)
}

// newTestServiceWith wires the optional download dependencies so tests can
// exercise RefreshArtist/SearchMissing. A nil q leaves both the queuer and the
// active-download finder nil, matching the subtask-04 tests.
func newTestServiceWith(t *testing.T, store Store, lib library.Store, provider discovery.Provider, rl RateLimiter, q *fakeQueuer, cfg config.Config) *Service {
	t.Helper()
	reg := discovery.NewRegistry(plugin.NewRegistry())
	if provider != nil {
		if err := reg.Inner().Register(provider); err != nil {
			t.Fatalf("register provider: %v", err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var queuer DownloadQueuer
	var finder ActiveDownloadFinder
	if q != nil {
		queuer = q
		finder = q
	}
	return NewService(store, reg, lib, rl, queuer, finder, func() config.Config { return cfg }, logger)
}

func boolPtr(b bool) *bool { return &b }

func seedTrackedArtist(t *testing.T, store *mockStore, name string) int64 {
	t.Helper()
	id, err := store.CreateTrackedArtist(context.Background(), &domain.TrackedArtist{
		Name: name, ProviderName: "deezer", ProviderArtistID: "art1",
		Monitored: true, MonitorMode: domain.MonitorModeAll,
	})
	if err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	return id
}

func seedTrackedAlbum(t *testing.T, store *mockStore, artistID int64, providerAlbumID, title string, status domain.AlbumStatus, monitored bool) domain.TrackedAlbum {
	t.Helper()
	album := &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: providerAlbumID, ProviderName: "deezer",
		Title: title, Year: 2001, Monitored: monitored, Status: status,
	}
	id, err := store.UpsertTrackedAlbum(context.Background(), album)
	if err != nil {
		t.Fatalf("seed album: %v", err)
	}
	album.ID = id
	return *album
}

// seedTrackedAlbumYear is seedTrackedAlbum with an explicit release year, for
// tests that assert deterministic batch ordering.
func seedTrackedAlbumYear(t *testing.T, store *mockStore, artistID int64, providerAlbumID, title string, year int, status domain.AlbumStatus, monitored bool) domain.TrackedAlbum {
	t.Helper()
	album := &domain.TrackedAlbum{
		TrackedArtistID: artistID, ProviderAlbumID: providerAlbumID, ProviderName: "deezer",
		Title: title, Year: year, Monitored: monitored, Status: status,
	}
	id, err := store.UpsertTrackedAlbum(context.Background(), album)
	if err != nil {
		t.Fatalf("seed album: %v", err)
	}
	album.ID = id
	return *album
}
