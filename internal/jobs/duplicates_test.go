package jobs

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
	"github.com/ramonskie/groovearr/internal/metadata"
)

type stubLibraryStore struct {
	library.Store
	artists []domain.Artist
	tracks  map[int64][]domain.Track
	merges  [][2]int64
	renames []struct {
		id   int64
		name string
	}
	scan map[string]string // duplicate_scan: group_key → canonical
}

var _ DuplicateScanStore = (*stubLibraryStore)(nil)

// stubCooldown is a minimal shared rate-limit bucket for tests.
type stubCooldown struct {
	cooling map[string]bool
	marked  map[string]time.Duration
}

func (s *stubCooldown) CoolingDown(name string) bool {
	return s.cooling != nil && s.cooling[name]
}

func (s *stubCooldown) MarkAfter(name string, retryAfter time.Duration) {
	if s.marked == nil {
		s.marked = make(map[string]time.Duration)
	}
	s.marked[name] = retryAfter
	if s.cooling == nil {
		s.cooling = make(map[string]bool)
	}
	s.cooling[name] = true
}

func (s *stubLibraryStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	if offset >= len(s.artists) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.artists) {
		end = len(s.artists)
	}
	return s.artists[offset:end], nil
}

func (s *stubLibraryStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) {
	for i := range s.artists {
		if s.artists[i].ID == id {
			return &s.artists[i], nil
		}
	}
	return nil, nil
}

func (s *stubLibraryStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	return s.tracks[artistID], nil
}

func (s *stubLibraryStore) MergeArtists(ctx context.Context, keepID, removeID int64) error {
	s.merges = append(s.merges, [2]int64{keepID, removeID})
	return nil
}

func (s *stubLibraryStore) RenameArtist(ctx context.Context, artistID int64, name string) error {
	s.renames = append(s.renames, struct {
		id   int64
		name string
	}{artistID, name})
	return nil
}

func (s *stubLibraryStore) GetDuplicateCanonical(ctx context.Context, groupKey string) (string, bool, error) {
	c, ok := s.scan[groupKey]
	return c, ok, nil
}
func (s *stubLibraryStore) ListDuplicateCanonicals(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range s.scan {
		out[k] = v
	}
	return out, nil
}
func (s *stubLibraryStore) UpsertDuplicateCanonical(ctx context.Context, groupKey, canonical string) error {
	if s.scan == nil {
		s.scan = map[string]string{}
	}
	s.scan[groupKey] = canonical
	return nil
}
func (s *stubLibraryStore) ClearDuplicateCanonicals(ctx context.Context) error {
	s.scan = map[string]string{}
	return nil
}
func (s *stubLibraryStore) DeleteDuplicateCanonical(ctx context.Context, groupKey string) error {
	delete(s.scan, groupKey)
	return nil
}

// stubNameProvider is a metadata provider whose canonical-name lookups come
// from a fixed map (keyed by normalized name), standing in for MusicBrainz.
type stubNameProvider struct {
	name  string
	names map[string]string
	err   error        // optional: return this error from CanonicalArtistName
	fails int          // return err for the first `fails` calls, then succeed
	left  atomic.Int32 // remaining failures (runtime)
}

var _ metadata.Provider = (*stubNameProvider)(nil)
var _ metadata.ArtistNameProvider = (*stubNameProvider)(nil)

func (p *stubNameProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "stubmb"
}
func (p *stubNameProvider) DisplayName() string {
	if p.name != "" {
		return p.name
	}
	return "stubmb"
}
func (p *stubNameProvider) IsConfigured() bool        { return true }
func (p *stubNameProvider) IsMetadataAvailable() bool { return true }
func (p *stubNameProvider) CapabilityStatus() map[string]string {
	return map[string]string{"metadata": "configured"}
}
func (p *stubNameProvider) CheckConnection(_ context.Context) error { return nil }
func (p *stubNameProvider) Connected() bool                         { return true }
func (p *stubNameProvider) SearchCover(_ context.Context, _, _ string) (*metadata.CoverResult, error) {
	return nil, nil
}
func (p *stubNameProvider) SearchArtistImage(_ context.Context, _ string) (*metadata.ArtistImageResult, error) {
	return nil, nil
}
func (p *stubNameProvider) SearchAlbum(_ context.Context, _, _ string) string { return "" }
func (p *stubNameProvider) EnrichTrack(_ context.Context, _ *domain.Track) (*metadata.TrackMetadata, error) {
	return nil, nil
}
func (p *stubNameProvider) CanonicalArtistName(_ context.Context, name string) (string, error) {
	// First `fails` calls return the error, then succeed (simulates a
	// transient rate limit that recovers).
	if p.left.Load() < int32(p.fails) {
		p.left.Add(1)
		return "", p.err
	}
	return p.names[normalizeName(name)], nil
}

func normalizeName(name string) string {
	var b []byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == ' ' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b = append(b, c)
	}
	return string(b)
}

func TestDuplicatesRunnerPersistsCanonicals(t *testing.T) {
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en De Munnik"},
			{ID: 2, Name: "Acda en de Munnik"},
			{ID: 3, Name: "Aaliyah"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
		},
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(&stubNameProvider{name: "musicbrainz", names: map[string]string{
		"acdaendemunnik": "Acda en de Munnik",
	}})
	runners := NewRunners(RunnerDeps{
		Log:      testLogger(),
		Store:    store,
		Config:   func() config.Config { return config.Config{} },
		Metadata: reg,
	})

	var reports []Report
	if err := runners.Duplicates()(context.Background(), func(r Report) { reports = append(reports, r) }); err != nil {
		t.Fatalf("Duplicates: %v", err)
	}

	if len(store.scan) != 1 || store.scan["acda en de munnik"] != "Acda en de Munnik" {
		t.Errorf("scan = %v, want only the duplicate group with the resolved canonical", store.scan)
	}
	if len(reports) == 0 {
		t.Error("expected progress reports")
	}
}

func TestDuplicatesRunnerRateLimitMarksSharedCooldown(t *testing.T) {
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en De Munnik"},
			{ID: 2, Name: "Acda en de Munnik"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
		},
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(&stubNameProvider{
		name: "musicbrainz",
		names: map[string]string{
			"acdaendemunnik": "Acda en de Munnik",
		},
		err:   fmt.Errorf("wrapped: %w", metadata.ErrRateLimited),
		fails: 100,
	})
	cd := &stubCooldown{}

	runners := NewRunners(RunnerDeps{
		Log:       testLogger(),
		Store:     store,
		Config:    func() config.Config { return config.Config{} },
		Metadata:  reg,
		RateLimit: cd,
	})

	if err := runners.Duplicates()(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("Duplicates: %v", err)
	}
	// Rate-limited provider is parked app-wide and the group is persisted
	// with an empty canonical — no retry that could re-arm a longer ban.
	if _, ok := cd.marked["musicbrainz"]; !ok {
		t.Errorf("cooldown not marked for musicbrainz: %v", cd.marked)
	}
	if got, ok := store.scan["acda en de munnik"]; !ok || got != "" {
		t.Errorf("group = %q (ok=%v), want empty canonical fallback (scan: %v)", got, ok, store.scan)
	}
}

func TestDuplicatesRunnerRateLimitStaysEmptyOnPersistentLimit(t *testing.T) {
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Acda en De Munnik"},
			{ID: 2, Name: "Acda en de Munnik"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
		},
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(&stubNameProvider{
		name:  "musicbrainz",
		names: map[string]string{},
		err:   fmt.Errorf("wrapped: %w", metadata.ErrRateLimited),
		fails: 100,
	})

	runners := NewRunners(RunnerDeps{
		Log:      testLogger(),
		Store:    store,
		Config:   func() config.Config { return config.Config{} },
		Metadata: reg,
	})

	if err := runners.Duplicates()(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("Duplicates: %v", err)
	}
	if _, ok := store.scan["acda en de munnik"]; !ok {
		t.Errorf("expected the group to be persisted with an empty canonical fallback, got %v", store.scan)
	}
}

func TestDuplicatesRunnerNoDuplicates(t *testing.T) {
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Aaliyah"},
			{ID: 2, Name: "Radiohead"},
		},
		scan: map[string]string{"stale group": "Stale"},
	}
	runners := NewRunners(RunnerDeps{
		Log:    testLogger(),
		Store:  store,
		Config: func() config.Config { return config.Config{} },
	})

	if err := runners.Duplicates()(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("Duplicates: %v", err)
	}
	if len(store.scan) != 0 {
		t.Errorf("expected empty scan for no duplicates, got %v", store.scan)
	}
}

func TestDuplicatesRunnerGroupsUnicodeVariants(t *testing.T) {
	// The reviewer's Tiësto/Tiesto case plus other unicode-variant pairs: the
	// normalized key must collapse them into one group so the canonical lookup
	// resolves the group, and the UI can offer a merge.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "Tiësto"},
			{ID: 2, Name: "Tiesto"},
			{ID: 3, Name: "Ne-Yo"},
			{ID: 4, Name: "Ne‐Yo"}, // U+2010 hyphen
			{ID: 5, Name: "Destiny's Child"},
			{ID: 6, Name: "Destiny’s Child"}, // curly apostrophe
			{ID: 7, Name: "René Froger"},
			{ID: 8, Name: "Rene Froger"},
			{ID: 9, Name: "Aaliyah"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
			3: {{ID: 13}},
			4: {{ID: 14}},
			5: {{ID: 15}},
			6: {{ID: 16}},
			7: {{ID: 17}},
			8: {{ID: 18}},
		},
	}
	reg := metadata.NewRegistry()
	// Keys match the stub's normalizeName output (spaces stripped, accents kept).
	_ = reg.Register(&stubNameProvider{name: "musicbrainz", names: map[string]string{
		"tiësto":        "Tiësto",
		"neyo":          "Ne-Yo",
		"destinyschild": "Destiny's Child",
		"renéfroger":    "René Froger",
	}})
	runners := NewRunners(RunnerDeps{
		Log:      testLogger(),
		Store:    store,
		Config:   func() config.Config { return config.Config{} },
		Metadata: reg,
	})

	if err := runners.Duplicates()(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("Duplicates: %v", err)
	}

	// Expect exactly 4 groups (the 4 unicode pairs); solo Aaliyah is skipped.
	if len(store.scan) != 4 {
		t.Errorf("scan = %v, want 4 groups (Tiësto/Tiesto, Ne-Yo/Ne‐Yo, Destiny's, René)", store.scan)
	}
	for _, key := range []string{"tiesto", "ne-yo", "destiny's child", "rene froger"} {
		if _, ok := store.scan[key]; !ok {
			t.Errorf("missing group %q in scan: %v", key, store.scan)
		}
	}
}

func TestDuplicatesRunnerGroupsFeatMarkedWithPrimary(t *testing.T) {
	// A feat-marked row ("2Pac feat. X") must group with its primary ("2Pac")
	// so the duplicates job — which is now the merge flow — heals the split
	// discography that a pre-guard import can leave behind.
	store := &stubLibraryStore{
		artists: []domain.Artist{
			{ID: 1, Name: "2Pac"},
			{ID: 2, Name: "2Pac feat. Anthony Hamilton"},
			{ID: 3, Name: "Simon & Garfunkel"}, // real band, must stay alone
			{ID: 4, Name: "Chaka Demus & Pliers"},
		},
		tracks: map[int64][]domain.Track{
			1: {{ID: 10}, {ID: 11}},
			2: {{ID: 12}},
			3: {{ID: 13}},
			4: {{ID: 14}},
		},
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(&stubNameProvider{name: "musicbrainz", names: map[string]string{
		"2pac": "2Pac",
	}})
	runners := NewRunners(RunnerDeps{
		Log:      testLogger(),
		Store:    store,
		Config:   func() config.Config { return config.Config{} },
		Metadata: reg,
	})

	if err := runners.Duplicates()(context.Background(), func(Report) {}); err != nil {
		t.Fatalf("Duplicates: %v", err)
	}

	// Exactly one group: 2Pac + its feat row. Real bands are not grouped.
	if len(store.scan) != 1 {
		t.Errorf("scan = %v, want only the 2Pac feat group", store.scan)
	}
	if c, ok := store.scan["2pac"]; !ok || c != "2Pac" {
		t.Errorf("scan[\"2pac\"] = %q, want canonical \"2Pac\" (scan: %v)", c, store.scan)
	}
}

func TestLookupCanonicalArtistSkipsCoolingProvider(t *testing.T) {
	p := &stubNameProvider{name: "musicbrainz", names: map[string]string{"tiësto": "Tiësto"}}
	reg := metadata.NewRegistry()
	_ = reg.Register(p)
	cd := &stubCooldown{cooling: map[string]bool{"musicbrainz": true}}

	runners := NewRunners(RunnerDeps{
		Log:       testLogger(),
		Config:    func() config.Config { return config.Config{} },
		Metadata:  reg,
		RateLimit: cd,
	})

	got, err := runners.LookupCanonicalArtist(context.Background(), "Tiësto")
	if err != nil {
		t.Fatalf("LookupCanonicalArtist: %v", err)
	}
	if got != "" {
		t.Errorf("canonical = %q, want empty (provider cooling down)", got)
	}
	if p.left.Load() != 0 {
		t.Errorf("provider called %d times, want 0 (must skip cooling provider)", p.left.Load())
	}
}

func TestLookupCanonicalArtistMarksCooldownOnRateLimit(t *testing.T) {
	p := &stubNameProvider{
		name:  "musicbrainz",
		names: map[string]string{},
		err:   fmt.Errorf("wrapped: %w", metadata.ErrRateLimited),
		fails: 100,
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(p)
	cd := &stubCooldown{}

	runners := NewRunners(RunnerDeps{
		Log:       testLogger(),
		Config:    func() config.Config { return config.Config{} },
		Metadata:  reg,
		RateLimit: cd,
	})

	got, err := runners.LookupCanonicalArtist(context.Background(), "Tiësto")
	if err == nil {
		t.Fatal("expected rate-limit error to propagate")
	}
	if got != "" {
		t.Errorf("canonical = %q, want empty on rate limit", got)
	}
	if _, ok := cd.marked["musicbrainz"]; !ok {
		t.Errorf("shared cooldown not marked for musicbrainz: %v", cd.marked)
	}
}

func TestLookupCanonicalArtistHonorsRetryAfter(t *testing.T) {
	p := &stubNameProvider{
		name:  "musicbrainz",
		names: map[string]string{},
		err:   metadata.NewRateLimitError("musicbrainz", 7*time.Minute, "HTTP 503"),
		fails: 100,
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(p)
	cd := &stubCooldown{}

	runners := NewRunners(RunnerDeps{
		Log:       testLogger(),
		Config:    func() config.Config { return config.Config{} },
		Metadata:  reg,
		RateLimit: cd,
	})

	_, _ = runners.LookupCanonicalArtist(context.Background(), "Tiësto")
	if got := cd.marked["musicbrainz"]; got != 7*time.Minute {
		t.Errorf("cooldown mark = %v, want 7m honoring Retry-After", got)
	}
}

func TestLookupCanonicalArtistNilRateLimiter(t *testing.T) {
	p := &stubNameProvider{
		name:  "musicbrainz",
		names: map[string]string{},
		err:   fmt.Errorf("wrapped: %w", metadata.ErrRateLimited),
		fails: 100,
	}
	reg := metadata.NewRegistry()
	_ = reg.Register(p)

	runners := NewRunners(RunnerDeps{
		Log:      testLogger(),
		Config:   func() config.Config { return config.Config{} },
		Metadata: reg,
	})

	got, err := runners.LookupCanonicalArtist(context.Background(), "Tiësto")
	if err == nil {
		t.Fatal("expected rate-limit error to propagate with nil cooldown")
	}
	if got != "" {
		t.Errorf("canonical = %q, want empty", got)
	}
}
