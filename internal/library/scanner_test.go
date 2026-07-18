package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ramonskie/groovearr/internal/domain"
)

func TestParsePath_ArtistAlbumTrack(t *testing.T) {
	track, artist, album := parsePath("Daft Punk/Random Access Memories (2013)/01 - Get Lucky.flac")
	if artist != "Daft Punk" {
		t.Errorf("artist = %q, want Daft Punk", artist)
	}
	if album != "Random Access Memories" {
		t.Errorf("album = %q, want Random Access Memories", album)
	}
	if track != "Get Lucky" {
		t.Errorf("track = %q, want Get Lucky", track)
	}
}

func TestParsePath_NoYear(t *testing.T) {
	track, artist, album := parsePath("Queen/Greatest Hits/Bohemian Rhapsody.mp3")
	if artist != "Queen" {
		t.Errorf("artist = %q, want Queen", artist)
	}
	if album != "Greatest Hits" {
		t.Errorf("album = %q, want Greatest Hits", album)
	}
	if track != "Bohemian Rhapsody" {
		t.Errorf("track = %q, want Bohemian Rhapsody", track)
	}
}

func TestParsePath_TrackNumberPrefix(t *testing.T) {
	track, _, _ := parsePath("Artist/Album/05 Song Title.flac")
	if track != "Song Title" {
		t.Errorf("track = %q, want Song Title (stripped number)", track)
	}
}

func TestParsePath_NoAlbum(t *testing.T) {
	track, artist, album := parsePath("Artist/Song.mp3")
	if artist != "Artist" {
		t.Errorf("artist = %q, want Artist", artist)
	}
	if album != "Unknown Album" {
		t.Errorf("album = %q, want Unknown Album", album)
	}
	if track != "Song" {
		t.Errorf("track = %q, want Song", track)
	}
}

func TestParsePath_SingleFile(t *testing.T) {
	track, artist, album := parsePath("song.mp3")
	if artist != "Unknown Artist" {
		t.Errorf("artist = %q, want Unknown Artist", artist)
	}
	if album != "Unknown Album" {
		t.Errorf("album = %q, want Unknown Album", album)
	}
	if track != "song" {
		t.Errorf("track = %q, want song", track)
	}
}

func TestAudioExtensions(t *testing.T) {
	valid := []string{".mp3", ".flac", ".ogg", ".m4a", ".wav"}
	for _, ext := range valid {
		if !audioExtensions[ext] {
			t.Errorf("%s should be a valid audio extension", ext)
		}
	}
	invalid := []string{".txt", ".jpg", ".pdf", ""}
	for _, ext := range invalid {
		if audioExtensions[ext] {
			t.Errorf("%s should NOT be a valid audio extension", ext)
		}
	}
	// Uppercase extensions — map is lowercase-only, but scanner lowercases before lookup.
	if audioExtensions[".FLAC"] {
		t.Error(".FLAC should not match directly (scanner lowercases first)")
	}
}

func TestFormatHumanSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		got := FormatHumanSize(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatHumanSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0:00"},
		{30000, "0:30"},
		{65000, "1:05"},
		{3600000, "60:00"},
	}
	for _, tt := range tests {
		got := FormatDuration(tt.ms)
		if got != tt.want {
			t.Errorf("FormatDuration(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

func TestParseTrackNumber(t *testing.T) {
	tests := []struct {
		filename string
		want     int
	}{
		{"01 - Song.flac", 1},
		{"12 Track.mp3", 12},
		{"Song.mp3", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := ParseTrackNumber(tt.filename)
		if got != tt.want {
			t.Errorf("ParseTrackNumber(%q) = %d, want %d", tt.filename, got, tt.want)
		}
	}
}

func TestScannerScanPath(t *testing.T) {
	// Create temp directory with test files.
	dir := t.TempDir()
	artistDir := filepath.Join(dir, "Test Artist")
	albumDir := filepath.Join(artistDir, "Test Album (2024)")
	os.MkdirAll(albumDir, 0755)
	os.WriteFile(filepath.Join(albumDir, "01 - First Track.flac"), []byte("fake flac"), 0644)
	os.WriteFile(filepath.Join(albumDir, "02 - Second Track.mp3"), []byte("fake mp3"), 0644)
	os.WriteFile(filepath.Join(albumDir, "cover.jpg"), []byte("not audio"), 0644) // should be skipped

	// Use in-memory mock store.
	store := &mockStore{
		artists: map[string]int64{},
		albums:  map[string]int64{},
	}
	scanner := NewScanner(store)

	stats, err := scanner.ScanPath(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Imported != 2 {
		t.Errorf("imported %d files, want 2", stats.Imported)
	}
	if stats.Scanned != 2 {
		t.Errorf("scanned %d files, want 2", stats.Scanned)
	}
	if stats.Skipped != 0 {
		t.Errorf("skipped %d files, want 0", stats.Skipped)
	}
}

// mockStore is a minimal in-memory implementation of Store for testing.
type mockStore struct {
	artists  map[string]int64
	albums   map[string]int64
	tracks   []domain.Track
	nextID   int64
}

func (m *mockStore) next() int64 { m.nextID++; return m.nextID }
func (m *mockStore) UpsertArtist(ctx context.Context, a *domain.Artist) (int64, error) {
	if id, ok := m.artists[a.Name]; ok { return id, nil }
	id := m.next()
	m.artists[a.Name] = id
	return id, nil
}
func (m *mockStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error)                { return nil, nil }
func (m *mockStore) GetArtistByName(ctx context.Context, name string) (*domain.Artist, error) {
	if id, ok := m.artists[name]; ok { return &domain.Artist{ID: id, Name: name}, nil }
	return nil, nil
}
func (m *mockStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error)     { return nil, nil }
func (m *mockStore) SearchArtists(ctx context.Context, query string, limit int) ([]domain.Artist, error) { return nil, nil }
func (m *mockStore) UpsertAlbum(ctx context.Context, a *domain.Album) (int64, error) {
	key := fmt.Sprintf("%d:%s", a.ArtistID, a.Title)
	if id, ok := m.albums[key]; ok { return id, nil }
	id := m.next()
	m.albums[key] = id
	return id, nil
}
func (m *mockStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error)                      { return nil, nil }
func (m *mockStore) GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error)      { return nil, nil }
func (m *mockStore) SearchAlbums(ctx context.Context, query string, limit int) ([]domain.Album, error) {
	var out []domain.Album
	for key, id := range m.albums {
		var artistID int64
		var title string
		fmt.Sscanf(key, "%d:%s", &artistID, &title)
		if title == query || query == "" {
			out = append(out, domain.Album{ID: id, ArtistID: artistID, Title: title})
		}
	}
	return out, nil
}
func (m *mockStore) UpsertTrack(ctx context.Context, t *domain.Track) (int64, error) {
	id := m.next()
	t.ID = id
	m.tracks = append(m.tracks, *t)
	return id, nil
}
func (m *mockStore) GetTrack(ctx context.Context, id int64) (*domain.Track, error)                       { return nil, nil }
func (m *mockStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error)         { return nil, nil }
func (m *mockStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error)       { return nil, nil }
func (m *mockStore) SearchTracks(ctx context.Context, query string, limit int) ([]domain.Track, error)  { return nil, nil }
func (m *mockStore) GetTrackByFilePath(ctx context.Context, fp string) (*domain.Track, error) {
	for _, t := range m.tracks {
		if t.FilePath == fp { return &t, nil }
	}
	return nil, nil
}
func (m *mockStore) DeleteTrack(ctx context.Context, id int64) error                                   { return nil }
func (m *mockStore) GetArtistByExternalID(ctx context.Context, svc, eid string) (*domain.Artist, error) { return nil, nil }
func (m *mockStore) GetAlbumByExternalID(ctx context.Context, svc, eid string) (*domain.Album, error)   { return nil, nil }
func (m *mockStore) GetTrackByExternalID(ctx context.Context, svc, eid string) (*domain.Track, error)   { return nil, nil }
func (m *mockStore) Close() error                                                                       { return nil }
