package identity

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/matching"
)

type fakeLibrary struct {
	tracks  map[int64][]domain.Track
	updates map[int64]int64
}

func (f *fakeLibrary) GetTracksByAlbum(_ context.Context, albumID int64) ([]domain.Track, error) {
	return f.tracks[albumID], nil
}

func (f *fakeLibrary) UpdateTrackDuration(_ context.Context, id, durationMs int64) error {
	if f.updates == nil {
		f.updates = make(map[int64]int64)
	}
	f.updates[id] = durationMs
	return nil
}

type fakeCatalog struct {
	albums    []catalog.UnmatchedAlbum
	meta      map[int64]*catalog.AlbumMetadata
	deleted   []int64
	deleteErr error
}

func (f *fakeCatalog) ListUnmatchedAlbums(_ context.Context, _ int) ([]catalog.UnmatchedAlbum, error) {
	return f.albums, nil
}

func (f *fakeCatalog) GetAlbumMetadata(_ context.Context, albumID int64) (*catalog.AlbumMetadata, error) {
	return f.meta[albumID], nil
}

func (f *fakeCatalog) DeleteRefreshByEntity(_ context.Context, _ catalog.EntityType, entityID int64) error {
	f.deleted = append(f.deleted, entityID)
	return f.deleteErr
}

type materializeCall struct {
	albumID         int64
	providerName    string
	providerAlbumID string
	tracks          []discovery.TrackInfo
	partial         bool
}

type fakeMaterializer struct {
	calls []materializeCall
	skip  bool
	err   error
}

func (f *fakeMaterializer) MaterializeResolved(_ context.Context, albumID int64, providerName, providerAlbumID string, tracks []discovery.TrackInfo, partial bool) (bool, error) {
	f.calls = append(f.calls, materializeCall{
		albumID:         albumID,
		providerName:    providerName,
		providerAlbumID: providerAlbumID,
		tracks:          tracks,
		partial:         partial,
	})
	if f.err != nil {
		return false, f.err
	}
	if f.skip {
		return false, nil
	}
	return true, nil
}

type fakeProvider struct {
	discovery.Provider
	name      string
	byID      map[string]*discovery.TrackIdentity
	byISRC    map[string]*discovery.TrackIdentity
	album     map[string][]discovery.TrackInfo
	idCalls   int
	isrcCalls int
	idErr     error
	isrcErr   error
	err       error
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) GetTrackIdentity(_ context.Context, id string) (*discovery.TrackIdentity, error) {
	p.idCalls++
	if p.idErr != nil {
		return nil, p.idErr
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.byID[id], nil
}

func (p *fakeProvider) LookupTrackByISRC(_ context.Context, isrc string) (*discovery.TrackIdentity, error) {
	p.isrcCalls++
	if p.isrcErr != nil {
		return nil, p.isrcErr
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.byISRC[isrc], nil
}

func (p *fakeProvider) GetAlbumTracks(_ context.Context, albumID string) ([]discovery.TrackInfo, error) {
	return p.album[albumID], nil
}

type fakeCooldown struct {
	cooling map[string]bool
	marks   map[string]time.Duration
}

func (c *fakeCooldown) CoolingDown(name string) bool { return c.cooling[name] }
func (c *fakeCooldown) MarkAfter(name string, d time.Duration) {
	if c.marks == nil {
		c.marks = make(map[string]time.Duration)
	}
	c.marks[name] = d
}

func newResolver(lib Library, cat Catalog, mat Materializer, providers []discovery.Provider, cooldown Cooldown, opts Options) *Resolver {
	if opts.ProbeDuration == nil {
		opts.ProbeDuration = func(context.Context, string) (time.Duration, error) {
			return 0, errors.New("unexpected probe")
		}
	}
	return New(lib, cat, mat, func() []discovery.Provider { return providers }, nil, cooldown,
		slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
}

func TestResolveSingleAutoAppliesOneTrack(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {
			ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1",
			Title: "Song", ArtistName: "Artist", AlbumTitle: "Song",
			AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1",
		},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 || res.Parked != 0 {
		t.Fatalf("result = %+v, want 1 resolved", res)
	}
	if len(mat.calls) != 1 {
		t.Fatalf("materialize calls = %d, want 1", len(mat.calls))
	}
	call := mat.calls[0]
	if call.partial {
		t.Error("a true single must not be partial")
	}
	if call.providerAlbumID != "al1" || call.providerName != "spotify" {
		t.Errorf("provider link = %s/%s, want spotify/al1", call.providerName, call.providerAlbumID)
	}
	if len(call.tracks) != 1 || call.tracks[0].ProviderID != "sp1" {
		t.Fatalf("tracks = %+v, want exactly sp1", call.tracks)
	}
	if len(cat.deleted) != 1 || cat.deleted[0] != 1 {
		t.Errorf("queue cleanup = %v, want album 1", cat.deleted)
	}
}

func TestResolveSingleOnLargerReleaseIsPartial(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 4, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {
			ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1",
			Title: "Song", ArtistName: "Artist", AlbumTitle: "Album",
			AlbumTrackCount: 12, TrackNumber: 4, DurationMs: 200000, ISRC: "US1",
		},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 {
		t.Fatalf("result = %+v, want 1 resolved", res)
	}
	if len(mat.calls) != 1 || !mat.calls[0].partial {
		t.Fatalf("materialize = %+v, want one partial call", mat.calls)
	}
	if len(mat.calls[0].tracks) != 1 {
		t.Errorf("partial single materialized %d tracks, want 1", len(mat.calls[0].tracks))
	}
}

func TestResolveWrongISRCStaysParked(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Intro", TrackNumber: 1, Duration: 180000, ISRC: "US1"}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Intro"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "deezer", byISRC: map[string]*discovery.TrackIdentity{
		"US1": {
			ProviderName: "deezer", ProviderTrackID: "dz1", ProviderAlbumID: "al9",
			Title: "Outro", ArtistName: "Other", AlbumTitle: "Other Album",
			AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 180000, ISRC: "US1",
		},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 || res.NoIdentity != 0 {
		t.Fatalf("result = %+v, want parked with a candidate", res)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialized a title-mismatched identity: %+v", mat.calls)
	}
}

func TestResolveArtistMismatchWithoutDurationStaysParked(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, ISRC: "US1"}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "deezer", byISRC: map[string]*discovery.TrackIdentity{
		"US1": {
			ProviderName: "deezer", ProviderTrackID: "dz1", ProviderAlbumID: "al9",
			Title: "Song", ArtistName: "Different Artist", AlbumTitle: "Song",
			AlbumTrackCount: 1, TrackNumber: 1, ISRC: "US1",
		},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want parked", res)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialized an artist-mismatched identity: %+v", mat.calls)
	}
}

func TestResolveNoIdentityStaysParked(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", ISRC: "US1"}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "deezer"}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 || res.NoIdentity != 1 {
		t.Fatalf("result = %+v, want no-identity park", res)
	}
}

func TestResolveMultiTrackRelease(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1, Duration: 100000, ISRC: "US1", ExternalIDs: map[string]string{"spotify": "s1"}},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2, Duration: 110000, ISRC: "US2", ExternalIDs: map[string]string{"spotify": "s2"}},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{
		name: "spotify",
		byID: map[string]*discovery.TrackIdentity{
			"s1": {ProviderName: "spotify", ProviderTrackID: "s1", ProviderAlbumID: "al1", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 1, DurationMs: 100000, ISRC: "US1"},
			"s2": {ProviderName: "spotify", ProviderTrackID: "s2", ProviderAlbumID: "al1", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 2, DurationMs: 110000, ISRC: "US2"},
		},
		album: map[string][]discovery.TrackInfo{
			"al1": {
				{ProviderID: "s1", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 1, DurationMs: 100000},
				{ProviderID: "s2", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 2, DurationMs: 110000},
			},
		},
	}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 {
		t.Fatalf("result = %+v, want 1 resolved", res)
	}
	if len(mat.calls) != 1 {
		t.Fatalf("materialize calls = %d, want 1", len(mat.calls))
	}
	call := mat.calls[0]
	if call.partial || call.providerAlbumID != "al1" || len(call.tracks) != 2 {
		t.Fatalf("materialize = %+v, want full al1 with 2 tracks", call)
	}
}

func TestResolveMultiTrackConflictStaysParked(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1, Duration: 100000, ISRC: "US1", ExternalIDs: map[string]string{"spotify": "s1"}},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2, Duration: 110000, ISRC: "US2", ExternalIDs: map[string]string{"spotify": "s2"}},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"s1": {ProviderName: "spotify", ProviderTrackID: "s1", ProviderAlbumID: "al1", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 1, DurationMs: 100000, ISRC: "US1"},
		"s2": {ProviderName: "spotify", ProviderTrackID: "s2", ProviderAlbumID: "al2", Title: "B", ArtistName: "Artist", AlbumTitle: "Other", AlbumTrackCount: 2, TrackNumber: 2, DurationMs: 110000, ISRC: "US2"},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want parked conflict", res)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialized a conflicting multi-track release: %+v", mat.calls)
	}
}

func TestResolveProbesMissingDuration(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, ISRC: "US1", FilePath: "/music/song.flac",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1", Title: "Song", ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}
	opts := Options{ProbeDuration: func(context.Context, string) (time.Duration, error) { return 200 * time.Second, nil }}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, opts).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 {
		t.Fatalf("result = %+v, want resolved", res)
	}
	if got := lib.updates[10]; got != 200000 {
		t.Fatalf("UpdateTrackDuration = %d, want 200000", got)
	}
}

func TestResolveAllProvidersCooling(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{1: {{ID: 10, AlbumID: 1, Title: "Song", ISRC: "US1"}}}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "deezer"}
	cool := &fakeCooldown{cooling: map[string]bool{"deezer": true}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, cool, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if !res.CoolingDown || res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want cooling-down park", res)
	}
	if len(mat.calls) != 0 {
		t.Fatal("materialized while every provider was cooling down")
	}
}

type plainProvider struct{ discovery.Provider }

func (plainProvider) Name() string { return "plain" }

func TestResolveSkipsAlbumNoLongerUnmatched(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", ISRC: "US1"}},
	}}
	cat := &fakeCatalog{
		albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}},
		meta:   map[int64]*catalog.AlbumMetadata{1: {AlbumID: 1, RefreshStatus: catalog.StatusFresh}},
	}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "deezer"}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Skipped != 1 || res.Resolved != 0 || res.Parked != 0 {
		t.Fatalf("result = %+v, want one skipped", res)
	}
	if p.idCalls != 0 || p.isrcCalls != 0 {
		t.Fatalf("provider calls id=%d isrc=%d, want 0 for a non-unmatched album", p.idCalls, p.isrcCalls)
	}
}

func TestResolveNoIdentityCapability(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{1: {{ID: 10, AlbumID: 1, Title: "Song"}}}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}

	_, err := newResolver(lib, cat, mat, []discovery.Provider{plainProvider{}}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if !errors.Is(err, ErrNoTrackProviders) {
		t.Fatalf("err = %v, want ErrNoTrackProviders", err)
	}
}

func TestResolveSingleSkipsWhenAlreadyMaterialized(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{skip: true}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1", Title: "Song",
			ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Skipped != 1 || res.Resolved != 0 || res.Parked != 0 {
		t.Fatalf("result = %+v, want one skipped", res)
	}
	if len(cat.deleted) != 0 {
		t.Fatalf("queue cleanup = %v, want none for a skipped album", cat.deleted)
	}
}

func TestResolveFallsBackToISRCWhenIDLookupFails(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "deleted"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{
		name:  "spotify",
		idErr: errors.New("track deleted"),
		byISRC: map[string]*discovery.TrackIdentity{
			"US1": {ProviderName: "spotify", ProviderTrackID: "right", ProviderAlbumID: "al1", Title: "Song",
				ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
		},
	}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 {
		t.Fatalf("result = %+v, want resolved via ISRC fallback", res)
	}
	if p.idCalls != 1 || p.isrcCalls != 1 {
		t.Fatalf("calls id=%d isrc=%d, want both attempted", p.idCalls, p.isrcCalls)
	}
	if got := mat.calls[0].tracks[0].ProviderID; got != "right" {
		t.Fatalf("materialized %q, want the ISRC-resolved track", got)
	}
}

func TestResolveCancellationDuringProbeReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, ISRC: "US1", FilePath: "/music/song.flac"}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "deezer"}
	opts := Options{ProbeDuration: func(context.Context, string) (time.Duration, error) {
		cancel()
		return 0, errors.New("probe aborted")
	}}

	_, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, opts).ResolveUnmatched(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialized after cancellation: %+v", mat.calls)
	}
}

func TestResolveKnownDurationConflictStaysParked(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1", Title: "Song",
			ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 100000, ISRC: "US1"},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want parked on a known duration conflict", res)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialized despite a duration conflict: %+v", mat.calls)
	}
}

func TestResolveSingleUnknownTrackCountIsNotPartial(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1", Title: "Song",
			ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 0, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 || len(mat.calls) != 1 {
		t.Fatalf("result = %+v calls=%d, want one resolution", res, len(mat.calls))
	}
	if mat.calls[0].partial {
		t.Fatal("unknown track count must not be treated as a partial release")
	}
}

func TestResolveSingleEmptyProviderAlbumParks(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "", Title: "Song",
			ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want parked without a provider release link", res)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialized without a release link: %+v", mat.calls)
	}
}

func TestResolveQueueCleanupFailureIsNonFatal(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{
		albums:    []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}},
		deleteErr: errors.New("queue locked"),
	}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1", Title: "Song",
			ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 || res.Errors != 0 {
		t.Fatalf("result = %+v, want resolved with a cleanup warning", res)
	}
}

func TestResolveZeroTrackAlbumIsSkipped(t *testing.T) {
	lib := &fakeLibrary{}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "deezer"}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Skipped != 1 || res.Parked != 0 || res.NoIdentity != 0 {
		t.Fatalf("result = %+v, want one skipped", res)
	}
	if p.idCalls != 0 || p.isrcCalls != 0 {
		t.Fatalf("provider calls id=%d isrc=%d, want 0", p.idCalls, p.isrcCalls)
	}
}

func TestAutoEligibleFilterIgnoresHigherNonAutoCandidate(t *testing.T) {
	nonAuto := scoredCandidate{
		identity: &discovery.TrackIdentity{ProviderName: "p", ProviderTrackID: "non", ProviderAlbumID: "a"},
		signals:  matching.CandidateSignals{ISRCExact: true, ArtistMatches: true, DurationKnown: true, DurationWithinTolerance: true},
		score:    100,
	}
	auto := scoredCandidate{
		identity: &discovery.TrackIdentity{ProviderName: "p", ProviderTrackID: "auto", ProviderAlbumID: "b"},
		signals:  matching.CandidateSignals{TitleMatches: true, ArtistMatches: true},
		score:    75,
	}
	eligible := make([]scoredCandidate, 0, 2)
	for _, c := range []scoredCandidate{nonAuto, auto} {
		if autoAllowed(c) {
			eligible = append(eligible, c)
		}
	}
	best := pickBest(eligible)
	if best == nil || best.identity.ProviderTrackID != "auto" {
		t.Fatalf("best = %+v, want the auto-eligible candidate", best)
	}
}

func TestResolveSingleUsesISRCWhenExternalIDIsWrong(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "wrong"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{
		name: "spotify",
		byID: map[string]*discovery.TrackIdentity{
			"wrong": {ProviderName: "spotify", ProviderTrackID: "wrong", ProviderAlbumID: "alx", Title: "Nope", ArtistName: "Nobody", AlbumTitle: "Nope", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 1000, ISRC: "XX1"},
		},
		byISRC: map[string]*discovery.TrackIdentity{
			"US1": {ProviderName: "spotify", ProviderTrackID: "right", ProviderAlbumID: "al1", Title: "Song", ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
		},
	}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 {
		t.Fatalf("result = %+v, want resolved from ISRC fallback", res)
	}
	if got := mat.calls[0].tracks[0].ProviderID; got != "right" {
		t.Fatalf("materialized %q, want the ISRC-verified track", got)
	}
}

func TestResolveDurationUnavailableDisablesProbe(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", ISRC: "US1", FilePath: "/a"},
			{ID: 11, AlbumID: 1, Title: "B", ISRC: "US2", FilePath: "/b"},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "deezer"}
	calls := 0
	opts := Options{ProbeDuration: func(context.Context, string) (time.Duration, error) {
		calls++
		return 0, library.ErrDurationUnavailable
	}}

	_, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, opts).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1 (disabled after unavailable)", calls)
	}
}
