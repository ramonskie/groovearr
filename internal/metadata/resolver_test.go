package metadata

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// mockProvider — implements metadata.Provider for testing
// ---------------------------------------------------------------------------

// mockProvider is a configurable MetadataResolver test double.
// Every field is safe to leave at the zero value; the default behaviour is
// a configured provider that returns no cover and no error.
type mockProvider struct {
	name       string
	configured bool
	cover      *CoverResult
	coverErr   error
	coverCalls int // incremented on each SearchCover invocation
}

// compile-time interface check
var _ Provider = (*mockProvider)(nil)

func (m *mockProvider) Name() string                                  { return m.name }
func (m *mockProvider) DisplayName() string                           { return m.name }
func (m *mockProvider) IsConfigured() bool                            { return m.configured }
func (m *mockProvider) CheckConnection(_ context.Context) error       { return nil }
func (m *mockProvider) Connected() bool                               { return true }

func (m *mockProvider) SearchCover(_ context.Context, _, _ string) (*CoverResult, error) {
	m.coverCalls++
	return m.cover, m.coverErr
}

func (m *mockProvider) SearchArtistImage(_ context.Context, _ string) (*ArtistImageResult, error) {
	return nil, nil
}

func (m *mockProvider) EnrichTrack(_ context.Context, _ *domain.Track) (*TrackMetadata, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// helper: create a resolver wired to one or more mock providers
// ---------------------------------------------------------------------------

func newTestResolver(providers ...*mockProvider) *MetadataResolver {
	reg := NewRegistry()
	for _, p := range providers {
		_ = reg.Register(p)
	}
	return NewMetadataResolver(reg, testLogger())
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

// TestEnrichMetadata_EmptyAlbum verifies the resolver returns early when
// album is empty — no provider is consulted. (User test case 1)
func TestEnrichMetadata_EmptyAlbum(t *testing.T) {
	tests := []struct {
		name   string
		artist string
		album  string
	}{
		{"empty album", "Radiohead", ""},
		{"empty artist", "", "OK Computer"},
		{"both empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &mockProvider{name: "mock", configured: true}
			resolver := newTestResolver(p)

			result, err := resolver.EnrichMetadata(context.Background(), tt.artist, "Karma Police", tt.album, 1997)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.CoverURL != "" {
				t.Errorf("expected empty CoverURL, got %q", result.CoverURL)
			}
			if p.coverCalls > 0 {
				t.Errorf("expected 0 SearchCover calls, got %d", p.coverCalls)
			}
		})
	}
}

// TestEnrichMetadata_CoverFound verifies the resolver populates CoverURL
// when a configured provider returns a cover result with a non-empty ImageURL.
// (User test case 2; also satisfies JSON CoverFound)
func TestEnrichMetadata_CoverFound(t *testing.T) {
	tests := []struct {
		name     string
		coverURL string
	}{
		{"http URL", "http://example.com/cover.jpg"},
		{"https URL", "https://images.example.com/album/1234/front"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &mockProvider{
				name:       "coverartarchive",
				configured: true,
				cover:      &CoverResult{ImageURL: tt.coverURL},
			}
			resolver := newTestResolver(p)

			result, err := resolver.EnrichMetadata(context.Background(), "Radiohead", "Karma Police", "OK Computer", 1997)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.CoverURL != tt.coverURL {
				t.Errorf("CoverURL = %q, want %q", result.CoverURL, tt.coverURL)
			}
			if p.coverCalls != 1 {
				t.Errorf("SearchCover calls = %d, want 1", p.coverCalls)
			}
		})
	}
}

// TestEnrichMetadata_NoConfiguredProviders verifies the resolver returns
// input fields unchanged when no providers are registered (empty registry).
// (User test case 3)
func TestEnrichMetadata_NoConfiguredProviders(t *testing.T) {
	// empty registry
	reg := NewRegistry()
	resolver := NewMetadataResolver(reg, testLogger())

	result, err := resolver.EnrichMetadata(context.Background(), "Radiohead", "Karma Police", "OK Computer", 1997)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CoverURL != "" {
		t.Errorf("CoverURL = %q, want empty", result.CoverURL)
	}
}

// TestEnrichMetadata_ProviderError verifies the resolver logs a warning and
// returns partial data when a provider returns an error.
// (User test case 4; also satisfies JSON MusicBrainzUnavailable)
func TestEnrichMetadata_ProviderError(t *testing.T) {
	p := &mockProvider{
		name:       "musicbrainz",
		configured: true,
		coverErr:   fmt.Errorf("connection refused"),
	}
	resolver := newTestResolver(p)

	result, err := resolver.EnrichMetadata(context.Background(), "Radiohead", "Karma Police", "OK Computer", 1997)
	if err != nil {
		t.Fatalf("expected nil error (graceful degradation), got %v", err)
	}
	// CoverURL must remain empty when provider errors
	if result.CoverURL != "" {
		t.Errorf("CoverURL = %q, want empty", result.CoverURL)
	}
	// Input fields must be preserved
	assertFields(t, result, "Radiohead", "Karma Police", "OK Computer", 1997)
	if p.coverCalls != 1 {
		t.Errorf("SearchCover calls = %d, want 1", p.coverCalls)
	}
}

// TestEnrichMetadata_CoverNotFound verifies CoverURL stays empty when a
// configured provider returns no cover (nil result with no error).
// (JSON acceptance criteria: CoverNotFound)
func TestEnrichMetadata_CoverNotFound(t *testing.T) {
	tests := []struct {
		name  string
		cover *CoverResult
	}{
		{"nil cover", nil},
		{"empty ImageURL", &CoverResult{ImageURL: ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &mockProvider{
				name:       "coverartarchive",
				configured: true,
				cover:      tt.cover,
			}
			resolver := newTestResolver(p)

			result, err := resolver.EnrichMetadata(context.Background(), "Radiohead", "Karma Police", "OK Computer", 1997)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.CoverURL != "" {
				t.Errorf("CoverURL = %q, want empty", result.CoverURL)
			}
			if p.coverCalls != 1 {
				t.Errorf("SearchCover calls = %d, want 1", p.coverCalls)
			}
		})
	}
}

// TestEnrichMetadata_AllFieldsPreserved verifies every input field survives
// enrichment even when no cover is found.
// (JSON acceptance criteria: AllFieldsComplete)
func TestEnrichMetadata_AllFieldsPreserved(t *testing.T) {
	p := &mockProvider{name: "mock", configured: false} // not configured → skipped
	resolver := newTestResolver(p)

	result, err := resolver.EnrichMetadata(context.Background(), "Artist Name", "Track Title", "Album Name", 2024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertFields(t, result, "Artist Name", "Track Title", "Album Name", 2024)
	// provider not configured → zero calls
	if p.coverCalls != 0 {
		t.Errorf("SearchCover calls = %d, want 0", p.coverCalls)
	}
}

// TestEnrichMetadata_MultipleProviders verifies the resolver tries providers
// in registration order: first error / no-cover, second succeeds.
func TestEnrichMetadata_MultipleProviders(t *testing.T) {
	tests := []struct {
		name         string
		p1Cover      *CoverResult
		p1Err        error
		p2Cover      *CoverResult
		p2Err        error
		wantCoverURL string
		wantCalls1   int
		wantCalls2   int
	}{
		{
			name:         "first errors, second succeeds",
			p1Err:        fmt.Errorf("timeout"),
			p2Cover:      &CoverResult{ImageURL: "http://fallback.example/cover.jpg"},
			wantCoverURL: "http://fallback.example/cover.jpg",
			wantCalls1:   1,
			wantCalls2:   1,
		},
		{
			name:       "first nil cover, second succeeds",
			p2Cover:    &CoverResult{ImageURL: "http://second.example/cover.jpg"},
			wantCoverURL: "http://second.example/cover.jpg",
			wantCalls1: 1,
			wantCalls2: 1,
		},
		{
			name:       "first empty URL, second succeeds",
			p1Cover:    &CoverResult{ImageURL: ""},
			p2Cover:    &CoverResult{ImageURL: "http://second.example/cover.jpg"},
			wantCoverURL: "http://second.example/cover.jpg",
			wantCalls1: 1,
			wantCalls2: 1,
		},
		{
			name:       "first succeeds, second not called",
			p1Cover:    &CoverResult{ImageURL: "http://first.example/cover.jpg"},
			wantCoverURL: "http://first.example/cover.jpg",
			wantCalls1: 1,
			wantCalls2: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p1 := &mockProvider{name: "p1", configured: true, cover: tt.p1Cover, coverErr: tt.p1Err}
			p2 := &mockProvider{name: "p2", configured: true, cover: tt.p2Cover, coverErr: tt.p2Err}
			resolver := newTestResolver(p1, p2)

			result, err := resolver.EnrichMetadata(context.Background(), "Radiohead", "Karma Police", "OK Computer", 1997)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.CoverURL != tt.wantCoverURL {
				t.Errorf("CoverURL = %q, want %q", result.CoverURL, tt.wantCoverURL)
			}
			if p1.coverCalls != tt.wantCalls1 {
				t.Errorf("p1 SearchCover calls = %d, want %d", p1.coverCalls, tt.wantCalls1)
			}
			if p2.coverCalls != tt.wantCalls2 {
				t.Errorf("p2 SearchCover calls = %d, want %d", p2.coverCalls, tt.wantCalls2)
			}
		})
	}
}

// TestEnrichMetadata_OnlyConfiguredProvidersAreUsed verifies only providers
// with IsConfigured()==true are consulted.
func TestEnrichMetadata_OnlyConfiguredProvidersAreUsed(t *testing.T) {
	configured := &mockProvider{
		name:       "configured",
		configured: true,
		cover:      &CoverResult{ImageURL: "http://cover.example/art.jpg"},
	}
	unconfigured := &mockProvider{
		name:       "unconfigured",
		configured: false,
		cover:      &CoverResult{ImageURL: "http://should-not-use.example/art.jpg"},
	}
	resolver := newTestResolver(unconfigured, configured)

	result, err := resolver.EnrichMetadata(context.Background(), "Artist", "Title", "Album", 2024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CoverURL != "http://cover.example/art.jpg" {
		t.Errorf("CoverURL = %q, want configured provider's URL", result.CoverURL)
	}
	if unconfigured.coverCalls > 0 {
		t.Errorf("unconfigured provider called %d times, want 0", unconfigured.coverCalls)
	}
	if configured.coverCalls != 1 {
		t.Errorf("configured provider called %d times, want 1", configured.coverCalls)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertFields(t *testing.T, m *domain.TrackMetadata, artist, title, album string, year int) {
	t.Helper()
	if m.Artist != artist {
		t.Errorf("Artist = %q, want %q", m.Artist, artist)
	}
	if m.Title != title {
		t.Errorf("Title = %q, want %q", m.Title, title)
	}
	if m.Album != album {
		t.Errorf("Album = %q, want %q", m.Album, album)
	}
	if m.Year != year {
		t.Errorf("Year = %d, want %d", m.Year, year)
	}
}
