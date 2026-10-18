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
	"github.com/ramonskie/groovearr/internal/metadata"
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

func (f *fakeCatalog) GetUnmatchedAlbum(_ context.Context, albumID int64) (*catalog.UnmatchedAlbum, error) {
	for i := range f.albums {
		if f.albums[i].AlbumID == albumID {
			return &f.albums[i], nil
		}
	}
	return nil, nil
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
	name                string
	byID                map[string]*discovery.TrackIdentity
	byISRC              map[string]*discovery.TrackIdentity
	album               map[string][]discovery.TrackInfo
	idCalls             int
	isrcCalls           int
	albumCalls          int
	idErr               error
	isrcErr             error
	albumErr            error
	err                 error
	identityUnavailable bool
	reviewOnly          bool
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) IdentityAvailable() bool  { return !p.identityUnavailable }
func (p *fakeProvider) IdentityReviewOnly() bool { return p.reviewOnly }

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
	p.albumCalls++
	if p.albumErr != nil {
		return nil, p.albumErr
	}
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

func TestResolveMultiTrackSmallerReleaseStaysParked(t *testing.T) {
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
			"al1": {{ProviderID: "s1", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 1, DurationMs: 100000}},
		},
	}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want parked smaller release", res)
	}
	if p.albumCalls != 1 {
		t.Fatalf("album calls = %d, want 1", p.albumCalls)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialized a smaller release: %+v", mat.calls)
	}
}

func TestResolveMultiTrackDuplicateProviderTrackStaysParked(t *testing.T) {
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
			"s1": {ProviderName: "spotify", ProviderTrackID: "same", ProviderAlbumID: "al1", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 1, DurationMs: 100000, ISRC: "US1"},
			"s2": {ProviderName: "spotify", ProviderTrackID: "same", ProviderAlbumID: "al1", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 2, DurationMs: 110000, ISRC: "US2"},
		},
		album: map[string][]discovery.TrackInfo{
			"al1": {
				{ProviderID: "same", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 1, DurationMs: 100000},
				{ProviderID: "other", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 2, DurationMs: 110000},
			},
		},
	}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want parked duplicate mapping", res)
	}
	if p.albumCalls != 0 {
		t.Fatalf("album calls = %d, want 0 for a duplicate mapping", p.albumCalls)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialized a duplicate mapping: %+v", mat.calls)
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

func TestResolveMarksRateLimitedProvider(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", ISRC: "US1", ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	cool := &fakeCooldown{}
	retry := 2 * time.Minute
	p := &fakeProvider{name: "spotify", idErr: metadata.NewRateLimitError("spotify", retry, "")}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, cool, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want a parked album", res)
	}
	if got := cool.marks["spotify"]; got != retry {
		t.Fatalf("MarkAfter duration = %v, want %v", got, retry)
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

func TestResolveSingleEquivalentTieUsesProviderOrder(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Anywhere You Go (DJ Antoine vs Mad Mark 2K12 radio edit)", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1", "deezer": "dz1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "DJ Antoine", Title: "Anywhere You Go"}}}
	mat := &fakeMaterializer{}
	spotify := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "sp-album",
			Title: "Anywhere You Go - DJ Antoine vs Mad Mark 2K12 Radio Edit", ArtistName: "DJ Antoine",
			AlbumTitle: "Anywhere You Go", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}
	deezer := &fakeProvider{name: "deezer", byID: map[string]*discovery.TrackIdentity{
		"dz1": {ProviderName: "deezer", ProviderTrackID: "dz1", ProviderAlbumID: "dz-album",
			Title: "Anywhere You Go (DJ Antoine vs Mad Mark 2K12 Radio Edit)", ArtistName: "DJ Antoine",
			AlbumTitle: "Anywhere You Go", AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}

	r := newResolver(lib, cat, mat, []discovery.Provider{spotify, deezer}, nil, Options{})
	// Deliberately reverse registration order: the live provider_order, not
	// slice order, must break the tie.
	r.order = metadata.NewProviderOrder(func() []string { return []string{"deezer", "spotify"} })
	res, err := r.ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 {
		t.Fatalf("result = %+v, want resolved", res)
	}
	if len(mat.calls) != 1 || mat.calls[0].providerName != "deezer" {
		t.Fatalf("materialize = %+v, want the configured earlier provider", mat.calls)
	}
	if got := mat.calls[0].tracks[0].Title; got != "Anywhere You Go (DJ Antoine vs Mad Mark 2K12 Radio Edit)" {
		t.Fatalf("title = %q, want the configured provider-order title", got)
	}
}

func TestResolveMultiTrackEquivalentTieUsesProviderOrder(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1, Duration: 100000, ISRC: "US1", ExternalIDs: map[string]string{"spotify": "s1", "deezer": "d1"}},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2, Duration: 110000, ISRC: "US2", ExternalIDs: map[string]string{"spotify": "s2", "deezer": "d2"}},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	spotify := &fakeProvider{
		name: "spotify",
		byID: map[string]*discovery.TrackIdentity{
			"s1": {ProviderName: "spotify", ProviderTrackID: "s1", ProviderAlbumID: "sp-album", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 1, DurationMs: 100000, ISRC: "US1"},
			"s2": {ProviderName: "spotify", ProviderTrackID: "s2", ProviderAlbumID: "sp-album", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 2, DurationMs: 110000, ISRC: "US2"},
		},
		album: map[string][]discovery.TrackInfo{
			"sp-album": {
				{ProviderID: "s1", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 1, DurationMs: 100000},
				{ProviderID: "s2", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 2, DurationMs: 110000},
			},
		},
	}
	deezer := &fakeProvider{
		name: "deezer",
		byID: map[string]*discovery.TrackIdentity{
			"d1": {ProviderName: "deezer", ProviderTrackID: "d1", ProviderAlbumID: "dz-album", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 1, DurationMs: 100000, ISRC: "US1"},
			"d2": {ProviderName: "deezer", ProviderTrackID: "d2", ProviderAlbumID: "dz-album", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 2, DurationMs: 110000, ISRC: "US2"},
		},
		album: map[string][]discovery.TrackInfo{
			"dz-album": {
				{ProviderID: "d1", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 1, DurationMs: 100000},
				{ProviderID: "d2", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", TrackNumber: 2, DurationMs: 110000},
			},
		},
	}

	r := newResolver(lib, cat, mat, []discovery.Provider{spotify, deezer}, nil, Options{})
	r.order = metadata.NewProviderOrder(func() []string { return []string{"deezer", "spotify"} })
	res, err := r.ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 1 {
		t.Fatalf("result = %+v, want resolved", res)
	}
	if len(mat.calls) != 1 || mat.calls[0].providerName != "deezer" || mat.calls[0].providerAlbumID != "dz-album" {
		t.Fatalf("materialize = %+v, want the configured earlier provider release", mat.calls)
	}
}

func TestResolveMultiTrackNonEquivalentTieStaysParked(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1, Duration: 100000, ExternalIDs: map[string]string{"spotify": "s1", "deezer": "d1"}},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2, Duration: 110000, ExternalIDs: map[string]string{"spotify": "s2", "deezer": "d2"}},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	// No ISRCs on either side; the 4s difference is inside the scoring
	// tolerance but outside the 2s tie tolerance, so this is a genuine
	// non-equivalent exact-score tie. The album maps make the test fail loudly
	// if the resolver wrongly treats the groups as equivalent and fetches.
	spotify := &fakeProvider{
		name: "spotify",
		byID: map[string]*discovery.TrackIdentity{
			"s1": {ProviderName: "spotify", ProviderTrackID: "s1", ProviderAlbumID: "sp-album", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 1, DurationMs: 104000},
			"s2": {ProviderName: "spotify", ProviderTrackID: "s2", ProviderAlbumID: "sp-album", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 2, DurationMs: 114000},
		},
		album: map[string][]discovery.TrackInfo{
			"sp-album": {{ProviderID: "s1", Title: "A"}, {ProviderID: "s2", Title: "B"}},
		},
	}
	deezer := &fakeProvider{
		name: "deezer",
		byID: map[string]*discovery.TrackIdentity{
			"d1": {ProviderName: "deezer", ProviderTrackID: "d1", ProviderAlbumID: "dz-album", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 1, DurationMs: 100000},
			"d2": {ProviderName: "deezer", ProviderTrackID: "d2", ProviderAlbumID: "dz-album", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 2, DurationMs: 110000},
		},
		album: map[string][]discovery.TrackInfo{
			"dz-album": {{ProviderID: "d1", Title: "A"}, {ProviderID: "d2", Title: "B"}},
		},
	}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{spotify, deezer}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want parked", res)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialize = %+v, want no guess", mat.calls)
	}
	if spotify.albumCalls != 0 || deezer.albumCalls != 0 {
		t.Fatalf("album fetches spotify=%d deezer=%d, want 0 (parked at equivalence)", spotify.albumCalls, deezer.albumCalls)
	}
}

func TestResolveMultiTrackDifferentKnownCountsStaysParked(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1, Duration: 100000, ExternalIDs: map[string]string{"spotify": "s1", "deezer": "d1"}},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2, Duration: 110000, ExternalIDs: map[string]string{"spotify": "s2", "deezer": "d2"}},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	spotify := &fakeProvider{
		name: "spotify",
		byID: map[string]*discovery.TrackIdentity{
			"s1": {ProviderName: "spotify", ProviderTrackID: "s1", ProviderAlbumID: "sp-album", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 1, DurationMs: 100000, ISRC: "US1"},
			"s2": {ProviderName: "spotify", ProviderTrackID: "s2", ProviderAlbumID: "sp-album", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 2, TrackNumber: 2, DurationMs: 110000, ISRC: "US2"},
		},
		album: map[string][]discovery.TrackInfo{
			"sp-album": {{ProviderID: "s1", Title: "A"}, {ProviderID: "s2", Title: "B"}},
		},
	}
	deezer := &fakeProvider{
		name: "deezer",
		byID: map[string]*discovery.TrackIdentity{
			"d1": {ProviderName: "deezer", ProviderTrackID: "d1", ProviderAlbumID: "dz-album", Title: "A", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 3, TrackNumber: 1, DurationMs: 100000, ISRC: "US1"},
			"d2": {ProviderName: "deezer", ProviderTrackID: "d2", ProviderAlbumID: "dz-album", Title: "B", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 3, TrackNumber: 2, DurationMs: 110000, ISRC: "US2"},
		},
		album: map[string][]discovery.TrackInfo{
			"dz-album": {{ProviderID: "d1", Title: "A"}, {ProviderID: "d2", Title: "B"}, {ProviderID: "d3", Title: "Bonus"}},
		},
	}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{spotify, deezer}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if err != nil {
		t.Fatalf("ResolveUnmatched: %v", err)
	}
	if res.Resolved != 0 || res.Parked != 1 {
		t.Fatalf("result = %+v, want parked", res)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialize = %+v, want no guess", mat.calls)
	}
	if spotify.albumCalls != 0 || deezer.albumCalls != 0 {
		t.Fatalf("album fetches spotify=%d deezer=%d, want 0 (different release counts)", spotify.albumCalls, deezer.albumCalls)
	}
}

func TestReviewAlbumSingleReturnsCandidates(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1",
			ExternalIDs: map[string]string{"spotify": "sp1"}}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1",
			Title: "Song", ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 1,
			TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}

	got, err := newResolver(lib, cat, &fakeMaterializer{}, []discovery.Provider{p}, nil, Options{}).
		ReviewAlbum(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReviewAlbum: %v", err)
	}
	if got.AlbumID != 1 || len(got.Tracks) != 1 || len(got.Tracks[0].Candidates) != 1 {
		t.Fatalf("review = %+v, want one track and one candidate", got)
	}
	c := got.Tracks[0].Candidates[0]
	if c.Provider != "spotify" || c.ProviderTrackID != "sp1" || !c.Auto || c.Score < 75 {
		t.Fatalf("candidate = %+v, want an auto Spotify candidate", c)
	}
}

func TestReviewAlbumMultiTrackSkipsIdentityCandidates(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1, Duration: 100000},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2, Duration: 110000},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	p := &fakeProvider{name: "spotify"}

	got, err := newResolver(lib, cat, &fakeMaterializer{}, []discovery.Provider{p}, nil, Options{}).
		ReviewAlbum(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReviewAlbum: %v", err)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(got.Tracks))
	}
	for _, tr := range got.Tracks {
		if len(tr.Candidates) != 0 {
			t.Fatalf("multi-track review candidate = %+v, want none", tr.Candidates)
		}
	}
	if p.idCalls != 0 || p.isrcCalls != 0 {
		t.Fatalf("provider calls id=%d isrc=%d, want 0", p.idCalls, p.isrcCalls)
	}
}

func TestApplyTrackMatchMaterializesPartial(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1",
			Title: "Song", ArtistName: "Artist", AlbumTitle: "Album", AlbumTrackCount: 12,
			TrackNumber: 4, DurationMs: 200000, ISRC: "US1"},
	}}

	applied, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ApplyTrackMatch(context.Background(), 1, 10, "spotify", "sp1")
	if err != nil {
		t.Fatalf("ApplyTrackMatch: %v", err)
	}
	if !applied || len(mat.calls) != 1 || !mat.calls[0].partial {
		t.Fatalf("applied=%v calls=%+v, want one partial call", applied, mat.calls)
	}
	if len(mat.calls[0].tracks) != 1 || mat.calls[0].tracks[0].ProviderID != "sp1" {
		t.Fatalf("tracks = %+v, want sp1", mat.calls[0].tracks)
	}
}

func TestApplyTrackMatchUnknownTrackCountNotPartial(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", byID: map[string]*discovery.TrackIdentity{
		"sp1": {ProviderName: "spotify", ProviderTrackID: "sp1", ProviderAlbumID: "al1",
			Title: "Song", ArtistName: "Artist", AlbumTitle: "Song", AlbumTrackCount: 0,
			TrackNumber: 1, DurationMs: 200000, ISRC: "US1"},
	}}

	applied, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ApplyTrackMatch(context.Background(), 1, 10, "spotify", "sp1")
	if err != nil {
		t.Fatalf("ApplyTrackMatch: %v", err)
	}
	if !applied || len(mat.calls) != 1 || mat.calls[0].partial {
		t.Fatalf("applied=%v calls=%+v, want one non-partial call", applied, mat.calls)
	}
	if len(mat.calls[0].tracks) != 1 || mat.calls[0].tracks[0].ProviderID != "sp1" {
		t.Fatalf("tracks = %+v, want sp1", mat.calls[0].tracks)
	}
}

func TestApplyTrackMatchRejectsMultiTrackAlbum(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	p := &fakeProvider{name: "spotify"}
	mat := &fakeMaterializer{}

	_, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ApplyTrackMatch(context.Background(), 1, 10, "spotify", "sp1")
	if !errors.Is(err, ErrReleaseMatchRequired) {
		t.Fatalf("err = %v, want ErrReleaseMatchRequired", err)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialize = %+v, want none", mat.calls)
	}
}

func TestApplyReleaseMatchMaterializesFullRelease(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", album: map[string][]discovery.TrackInfo{
		"al1": {
			{ProviderID: "s1", Title: "A", AlbumTitle: "Album", TrackNumber: 1},
			{ProviderID: "s2", Title: "B", AlbumTitle: "Album", TrackNumber: 2},
		},
	}}

	n, applied, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ApplyReleaseMatch(context.Background(), 1, "spotify", "al1")
	if err != nil {
		t.Fatalf("ApplyReleaseMatch: %v", err)
	}
	if !applied || n != 2 || len(mat.calls) != 1 || mat.calls[0].partial {
		t.Fatalf("n=%d applied=%v calls=%+v, want full release", n, applied, mat.calls)
	}
}

func TestApplyReleaseMatchRejectsSingleTrackExpansion(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", album: map[string][]discovery.TrackInfo{
		"al1": {
			{ProviderID: "s1", Title: "A", TrackNumber: 1},
			{ProviderID: "s2", Title: "B", TrackNumber: 2},
		},
	}}

	_, _, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ApplyReleaseMatch(context.Background(), 1, "spotify", "al1")
	if !errors.Is(err, ErrTrackMatchRequired) {
		t.Fatalf("err = %v, want ErrTrackMatchRequired", err)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialize = %+v, want none", mat.calls)
	}
}

func TestReviewAlbumResolvedAlbumRejected(t *testing.T) {
	cat := &fakeCatalog{}
	_, err := newResolver(&fakeLibrary{}, cat, &fakeMaterializer{}, nil, nil, Options{}).
		ReviewAlbum(context.Background(), 1)
	if !errors.Is(err, ErrAlbumNotUnmatched) {
		t.Fatalf("err = %v, want ErrAlbumNotUnmatched", err)
	}
}

func TestEquivalentRecordingsSafety(t *testing.T) {
	mk := func(isrc, title, artist string, ms int64) *discovery.TrackIdentity {
		return &discovery.TrackIdentity{ISRC: isrc, Title: title, ArtistName: artist, DurationMs: ms}
	}
	cases := []struct {
		name string
		a, b *discovery.TrackIdentity
		want bool
	}{
		{"same ISRC wins despite title spelling", mk("US1", "Song - Radio Edit", "Artist", 200000), mk("US1", "Song (Radio Edit)", "Artist", 500000), true},
		{"hyphenated ISRC matches normalized", mk("US-RC1-76-07839", "Song", "Artist", 200000), mk("USRC17607839", "Song", "Artist", 200000), true},
		{"different non-empty ISRC is not equivalent", mk("US1", "Song", "Artist", 200000), mk("US2", "Song", "Artist", 200000), false},
		{"one missing ISRC needs known duration", mk("", "Song", "Artist", 200000), mk("US2", "Song", "Artist", 201000), true},
		{"one missing ISRC with unknown duration parks", mk("", "Song", "Artist", 0), mk("US2", "Song", "Artist", 200000), false},
		{"duration outside tie tolerance parks", mk("", "Song", "Artist", 200000), mk("US2", "Song", "Artist", 204000), false},
		{"different title parks", mk("US1", "Song A", "Artist", 200000), mk("US2", "Song B", "Artist", 200000), false},
		{"different artist parks", mk("", "Song", "Artist", 200000), mk("US2", "Song", "Some Other Band", 200000), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := equivalentRecordings(tc.a, tc.b); got != tc.want {
				t.Fatalf("equivalentRecordings = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyReleaseMatchRejectsShrinkingRelease(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "spotify", album: map[string][]discovery.TrackInfo{
		"al1": {{ProviderID: "s1", Title: "A", TrackNumber: 1}},
	}}

	_, _, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ApplyReleaseMatch(context.Background(), 1, "spotify", "al1")
	if !errors.Is(err, ErrReleaseTooSmall) {
		t.Fatalf("err = %v, want ErrReleaseTooSmall", err)
	}
	if len(mat.calls) != 0 {
		t.Fatalf("materialize = %+v, want none", mat.calls)
	}
}

type fakeReleaseOnlyProvider struct {
	discovery.Provider
	name  string
	album map[string][]discovery.TrackInfo
}

func (p *fakeReleaseOnlyProvider) Name() string { return p.name }
func (p *fakeReleaseOnlyProvider) GetAlbumTracks(_ context.Context, albumID string) ([]discovery.TrackInfo, error) {
	return p.album[albumID], nil
}

func TestApplyReleaseMatchUsesBaseDiscoveryProvider(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	mat := &fakeMaterializer{}
	// This provider deliberately does NOT implement TrackIdentityProvider.
	p := &fakeReleaseOnlyProvider{name: "musicbrainz", album: map[string][]discovery.TrackInfo{
		"mb1": {
			{ProviderID: "m1", Title: "A", TrackNumber: 1},
			{ProviderID: "m2", Title: "B", TrackNumber: 2},
		},
	}}

	n, applied, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ApplyReleaseMatch(context.Background(), 1, "musicbrainz", "mb1")
	if err != nil {
		t.Fatalf("ApplyReleaseMatch: %v", err)
	}
	if !applied || n != 2 || len(mat.calls) != 1 {
		t.Fatalf("n=%d applied=%v calls=%+v, want full release", n, applied, mat.calls)
	}
}

func TestReviewAlbumAllIdentityProvidersCooling(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	p := &fakeProvider{name: "spotify"}
	cool := &fakeCooldown{cooling: map[string]bool{"spotify": true}}

	review, err := newResolver(lib, cat, &fakeMaterializer{}, []discovery.Provider{p}, cool, Options{}).
		ReviewAlbum(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReviewAlbum: %v", err)
	}
	if len(review.IdentityProviders) != 0 {
		t.Fatalf("identity providers = %v, want none while cooling", review.IdentityProviders)
	}
	if len(review.Tracks) != 1 || len(review.Tracks[0].Candidates) != 0 {
		t.Fatalf("review = %+v, want local track without candidates", review)
	}
	if p.idCalls != 0 || p.isrcCalls != 0 {
		t.Fatalf("provider calls id=%d isrc=%d, want 0", p.idCalls, p.isrcCalls)
	}
}

func TestApplyReleaseMatchProviderNotFound(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {
			{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1},
			{ID: 11, AlbumID: 1, Title: "B", TrackNumber: 2},
		},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	p := &fakeProvider{name: "deezer", albumErr: discovery.ErrNotFound}

	_, _, err := newResolver(lib, cat, &fakeMaterializer{}, []discovery.Provider{p}, nil, Options{}).
		ApplyReleaseMatch(context.Background(), 1, "deezer", "missing")
	if !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("err = %v, want ErrCandidateNotFound", err)
	}
}

func TestApplyTrackMatchProviderNotFound(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "A", TrackNumber: 1}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Album"}}}
	p := &fakeProvider{name: "spotify", idErr: discovery.ErrNotFound}

	_, err := newResolver(lib, cat, &fakeMaterializer{}, []discovery.Provider{p}, nil, Options{}).
		ApplyTrackMatch(context.Background(), 1, 10, "spotify", "missing")
	if !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("err = %v, want ErrCandidateNotFound", err)
	}
}

func TestTrackProvidersSkipsUnavailableIdentity(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	p := &fakeProvider{name: "spotify", identityUnavailable: true}

	review, err := newResolver(lib, cat, &fakeMaterializer{}, []discovery.Provider{p}, nil, Options{}).
		ReviewAlbum(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReviewAlbum: %v", err)
	}
	if len(review.IdentityProviders) != 0 {
		t.Fatalf("identity providers = %v, want unavailable provider excluded", review.IdentityProviders)
	}
	if p.idCalls != 0 || p.isrcCalls != 0 {
		t.Fatalf("provider calls id=%d isrc=%d, want 0", p.idCalls, p.isrcCalls)
	}
}

func TestResolveUnmatchedSkipsReviewOnlyProvider(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1"}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "musicbrainz", reviewOnly: true, byISRC: map[string]*discovery.TrackIdentity{
		"US1": {
			ProviderName: "musicbrainz", ProviderTrackID: "rec1", ProviderAlbumID: "rg1",
			Title: "Song", ArtistName: "Artist", AlbumTitle: "Song",
			AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1",
		},
	}}

	res, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ResolveUnmatched(context.Background(), nil)
	if !errors.Is(err, ErrNoTrackProviders) {
		t.Fatalf("err = %v, want ErrNoTrackProviders", err)
	}
	if res.Total != 1 {
		t.Fatalf("result = %+v, want the album counted", res)
	}
	if p.isrcCalls != 0 || len(mat.calls) != 0 {
		t.Fatalf("provider calls=%d materialize=%+v, want none", p.isrcCalls, mat.calls)
	}
}

func TestReviewAlbumIncludesReviewOnlyProvider(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000, ISRC: "US1"}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	p := &fakeProvider{name: "musicbrainz", reviewOnly: true, byISRC: map[string]*discovery.TrackIdentity{
		"US1": {
			ProviderName: "musicbrainz", ProviderTrackID: "rec1", ProviderAlbumID: "rg1",
			Title: "Song", ArtistName: "Artist", AlbumTitle: "Song",
			AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000, ISRC: "US1",
		},
	}}

	review, err := newResolver(lib, cat, &fakeMaterializer{}, []discovery.Provider{p}, nil, Options{}).
		ReviewAlbum(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReviewAlbum: %v", err)
	}
	if len(review.IdentityProviders) != 1 || review.IdentityProviders[0] != "musicbrainz" {
		t.Fatalf("identity providers = %v, want musicbrainz", review.IdentityProviders)
	}
	if len(review.Tracks) != 1 || len(review.Tracks[0].Candidates) != 1 {
		t.Fatalf("review = %+v, want one review-only candidate", review)
	}
	if got := review.Tracks[0].Candidates[0].Provider; got != "musicbrainz" {
		t.Errorf("candidate provider = %q, want musicbrainz", got)
	}
	if review.Tracks[0].Candidates[0].Auto {
		t.Error("review-only candidate must not advertise automatic application")
	}
}

func TestApplyTrackMatchAllowsReviewOnlyProvider(t *testing.T) {
	lib := &fakeLibrary{tracks: map[int64][]domain.Track{
		1: {{ID: 10, AlbumID: 1, Title: "Song", TrackNumber: 1, Duration: 200000}},
	}}
	cat := &fakeCatalog{albums: []catalog.UnmatchedAlbum{{AlbumID: 1, Artist: "Artist", Title: "Song"}}}
	mat := &fakeMaterializer{}
	p := &fakeProvider{name: "musicbrainz", reviewOnly: true, byID: map[string]*discovery.TrackIdentity{
		"rec1": {
			ProviderName: "musicbrainz", ProviderTrackID: "rec1", ProviderAlbumID: "rg1",
			Title: "Song", ArtistName: "Artist", AlbumTitle: "Song",
			AlbumTrackCount: 1, TrackNumber: 1, DurationMs: 200000,
		},
	}}

	applied, err := newResolver(lib, cat, mat, []discovery.Provider{p}, nil, Options{}).
		ApplyTrackMatch(context.Background(), 1, 10, "musicbrainz", "rec1")
	if err != nil {
		t.Fatalf("ApplyTrackMatch: %v", err)
	}
	if !applied || len(mat.calls) != 1 {
		t.Fatalf("applied=%v calls=%+v, want one materialization", applied, mat.calls)
	}
}

func TestValidProviderID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"4AkAnumox7l5vrSf8KhInL", true},
		{"12345", true},
		{"8f1c2f7a-1234-5678-9abc-def012345678", true},
		{"", false},
		{"a/b", false},
		{"a?b", false},
		{"a#b", false},
		{"a%2Fb", false},
		{"a b", false},
		{".", false},
		{"..", false},
	}
	for _, tc := range cases {
		if got := validProviderID(tc.id); got != tc.want {
			t.Errorf("validProviderID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
