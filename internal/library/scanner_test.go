package library

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dhowden/tag"
	"github.com/ramonskie/groovearr/internal/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParsePath_ArtistAlbumTrack(t *testing.T) {
	artist, album, track := ParseFileMetadata("Daft Punk/Random Access Memories (2013)/01 - Get Lucky.flac")
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
	artist, album, track := ParseFileMetadata("Queen/Greatest Hits/Bohemian Rhapsody.mp3")
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
	_, _, track := ParseFileMetadata("Artist/Album/05 Song Title.flac")
	if track != "Song Title" {
		t.Errorf("track = %q, want Song Title (stripped number)", track)
	}
}

func TestParsePath_NoAlbum(t *testing.T) {
	artist, album, track := ParseFileMetadata("Artist/Song.mp3")
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
	artist, album, track := ParseFileMetadata("song.mp3")
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

func TestParsePath_FlatArtistTrack(t *testing.T) {
	tests := []struct {
		path       string
		wantTrack  string
		wantArtist string
		wantAlbum  string
	}{
		{"Daft Punk - Get Lucky.flac", "Get Lucky", "Daft Punk", "Unknown Album"},
		{"Artist - Album - Title.mp3", "Title", "Artist", "Album"},
		{"SingleArtist.flac", "SingleArtist", "Unknown Artist", "Unknown Album"},
	}
	for _, tt := range tests {
		artist, album, track := ParseFileMetadata(tt.path)
		if track != tt.wantTrack {
			t.Errorf("%s: track = %q, want %q", tt.path, track, tt.wantTrack)
		}
		if artist != tt.wantArtist {
			t.Errorf("%s: artist = %q, want %q", tt.path, artist, tt.wantArtist)
		}
		if album != tt.wantAlbum {
			t.Errorf("%s: album = %q, want %q", tt.path, album, tt.wantAlbum)
		}
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

func TestReadFileTags(t *testing.T) {
	t.Run("non-audio file returns nil", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "not-audio.txt")
		if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
			t.Fatal(err)
		}
		tags, err := readFileTags(path)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if tags != nil {
			t.Errorf("expected nil tags for non-audio file, got %+v", tags)
		}
	})

	t.Run("empty file returns nil", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.mp3")
		if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		tags, err := readFileTags(path)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if tags != nil {
			t.Errorf("expected nil tags for empty file, got %+v", tags)
		}
	})

	t.Run("missing file returns error", func(t *testing.T) {
		_, err := readFileTags("/nonexistent/file.mp3")
		if err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("minimal FLAC with vorbis comments", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.flac")
		if err := os.WriteFile(path, minimalFLAC("Test Artist", "Test Album", "Test Title", 2024, 5, 1), 0o644); err != nil {
			t.Fatal(err)
		}
		tags, err := readFileTags(path)
		if err != nil {
			t.Fatal(err)
		}
		if tags == nil {
			t.Fatal("expected tags from FLAC file")
		}
		if tags.Artist != "Test Artist" {
			t.Errorf("artist = %q, want Test Artist", tags.Artist)
		}
		if tags.Album != "Test Album" {
			t.Errorf("album = %q, want Test Album", tags.Album)
		}
		if tags.Title != "Test Title" {
			t.Errorf("title = %q, want Test Title", tags.Title)
		}
		if tags.Year != 2024 {
			t.Errorf("year = %d, want 2024", tags.Year)
		}
		if tags.TrackNum != 5 {
			t.Errorf("trackNum = %d, want 5", tags.TrackNum)
		}
		if tags.DiscNum != 1 {
			t.Errorf("discNum = %d, want 1", tags.DiscNum)
		}
	})
}

func TestHasArtistImage(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	track := filepath.Join(albumDir, "01 - Track.flac")

	if hasArtistImage(track) {
		t.Error("expected no artist image initially")
	}
	os.WriteFile(filepath.Join(artistDir, "artist.jpg"), []byte("x"), 0o644)
	if !hasArtistImage(track) {
		t.Error("expected artist image detected")
	}
}

func TestIsLocalArtistThumb(t *testing.T) {
	local := []string{
		"artist.jpg", "artist.jpeg", "artist.png", "artist.webp", "artist.gif",
		"artists.jpg", "artists.png",
	}
	for _, v := range local {
		if !IsLocalArtistThumb(v) {
			t.Errorf("IsLocalArtistThumb(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "https://example.com/artist.jpg", "cover.jpg", "artist", "artists.example.com/x.jpg", "artist.example.com/x.jpg"} {
		if IsLocalArtistThumb(v) {
			t.Errorf("IsLocalArtistThumb(%q) = true, want false", v)
		}
	}
}

func TestRecordArtistThumbFlatLayout(t *testing.T) {
	root := t.TempDir()
	// Flat layout: no album level, the artist folder holds the tracks and a
	// pre-existing portrait under the plural name.
	artistDir := filepath.Join(root, "Flat Artist")
	os.MkdirAll(artistDir, 0o755)
	os.WriteFile(filepath.Join(artistDir, "artists.jpg"), []byte("x"), 0o644)
	track := filepath.Join(artistDir, "01 - Track.flac")

	store := &mockStore{
		artists: map[string]int64{"Flat Artist": 1},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
	}
	scanner := NewScanner(store, testLogger())

	scanner.recordArtistThumbOnce(t.Context(), track, ArtistDirFromTrack(track), root, map[string]bool{}, map[string]int{})

	if got := store.thumbs[1]; got != "artists.jpg" {
		t.Fatalf("thumb = %q, want artists.jpg (flat layout portrait surfaced)", got)
	}
}

func TestRecordArtistThumbOnceSkipsLooseRootTracks(t *testing.T) {
	root := t.TempDir()
	track := filepath.Join(root, "01 - Track.flac")
	os.WriteFile(track, []byte("fake"), 0o644)

	store := &mockStore{
		artists: map[string]int64{"root": 1},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
	}
	scanner := NewScanner(store, testLogger())

	scanner.recordArtistThumbOnce(t.Context(), track, ArtistDirFromTrack(track), root, map[string]bool{}, map[string]int{})

	if _, set := store.thumbs[1]; set {
		t.Error("loose root tracks must not be attributed an artist thumbnail")
	}
}

func TestRecordArtistThumbOnceRetriesUntilMatch(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	os.WriteFile(filepath.Join(artistDir, "artists.jpg"), []byte("x"), 0o644)
	// First track walked is a guest feature whose tag artist doesn't match the
	// folder; a later sibling carries the folder artist's name.
	track01 := filepath.Join(albumDir, "01 - Feat.flac")
	os.WriteFile(track01, minimalFLAC("Guest Artist", "Album", "Feat", 2020, 1, 1), 0o644)
	track02 := filepath.Join(albumDir, "02 - Main.flac")
	os.WriteFile(track02, minimalFLAC("Artist", "Album", "Main", 2020, 2, 1), 0o644)

	store := &mockStore{
		artists: map[string]int64{"Artist": 1},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
	}
	scanner := NewScanner(store, testLogger())
	done := map[string]bool{}

	scanner.recordArtistThumbOnce(t.Context(), track01, artistDir, root, done, map[string]int{})
	if _, set := store.thumbs[1]; set {
		t.Fatal("guest-featured first track must not record the portrait")
	}

	scanner.recordArtistThumbOnce(t.Context(), track02, artistDir, root, done, map[string]int{})
	if got := store.thumbs[1]; got != "artists.jpg" {
		t.Fatalf("thumb = %q, want artists.jpg (later sibling surfaced the portrait)", got)
	}
}

func TestRecordArtistThumbOnceTerminalWithoutImage(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	track := filepath.Join(albumDir, "01 - Track.flac")
	os.WriteFile(track, []byte("fake"), 0o644)

	store := &mockStore{artists: map[string]int64{"Artist": 1}, albums: map[string]int64{}, thumbs: map[int64]string{}}
	scanner := NewScanner(store, testLogger())

	done := map[string]bool{}
	scanner.recordArtistThumbOnce(t.Context(), track, artistDir, root, done, map[string]int{})
	if !done[artistDir] {
		t.Error("image-less directory should be marked done so sibling tracks don't re-stat the candidates")
	}
}

func TestRecordArtistThumbOnceBoundsRetries(t *testing.T) {
	root := t.TempDir()
	// A grouping folder with an on-disk portrait whose tracks all carry a
	// different tag artist — the compilation guard retries forever. The
	// attempt bound must terminate the probe so a big VA folder doesn't re-stat
	// the image candidates and re-parse tags for every track.
	artistDir := filepath.Join(root, "Various Artists")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	os.WriteFile(filepath.Join(artistDir, "artists.jpg"), []byte("x"), 0o644)
	track := filepath.Join(albumDir, "01 - Track.flac")
	os.WriteFile(track, minimalFLAC("2Pac", "Album", "Track", 2020, 1, 1), 0o644)

	store := &mockStore{artists: map[string]int64{"Various Artists": 1}, albums: map[string]int64{}, thumbs: map[int64]string{}}
	scanner := NewScanner(store, testLogger())

	done := map[string]bool{}
	attempts := map[string]int{}
	for i := 0; i < maxArtistThumbAttempts; i++ {
		scanner.recordArtistThumbOnce(t.Context(), track, artistDir, root, done, attempts)
	}
	if !done[artistDir] {
		t.Errorf("directory not marked done after %d retries", maxArtistThumbAttempts)
	}
	if _, set := store.thumbs[1]; set {
		t.Error("compilation guard should prevent recording the grouping portrait")
	}
}

func TestRecordArtistThumbCompilationGuard(t *testing.T) {
	root := t.TempDir()
	// A compilation grouping directory whose embedded tag artist differs from
	// the folder name must not get the compilation cover recorded as its
	// portrait — writeAlbumCover applies the same guard.
	vaDir := filepath.Join(root, "Various Artists")
	albumDir := filepath.Join(vaDir, "Compilation Album")
	os.MkdirAll(albumDir, 0o755)
	os.WriteFile(filepath.Join(vaDir, "artists.jpg"), []byte("x"), 0o644)
	track := filepath.Join(albumDir, "01 - Track.flac")
	os.WriteFile(track, minimalFLAC("Daft Punk", "Compilation Album", "Track", 2020, 1, 1), 0o644)

	store := &mockStore{
		artists: map[string]int64{"Various Artists": 1},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
	}
	scanner := NewScanner(store, testLogger())

	scanner.recordArtistThumb(t.Context(), track, vaDir)

	if _, set := store.thumbs[1]; set {
		t.Error("compilation cover was recorded as the folder-name artist's thumbnail")
	}
}

func TestRecordArtistThumbMatchingTagRecords(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	os.WriteFile(filepath.Join(artistDir, "artists.jpg"), []byte("x"), 0o644)
	track := filepath.Join(albumDir, "01 - Track.flac")
	os.WriteFile(track, minimalFLAC("Artist", "Album", "Track", 2020, 1, 1), 0o644)

	store := &mockStore{
		artists: map[string]int64{"Artist": 1},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
	}
	scanner := NewScanner(store, testLogger())

	scanner.recordArtistThumb(t.Context(), track, artistDir)

	if got := store.thumbs[1]; got != "artists.jpg" {
		t.Fatalf("thumb = %q, want artists.jpg (matching folder/tag artist)", got)
	}
}

func TestRecordArtistThumbSkipsCompilationArtist(t *testing.T) {
	root := t.TempDir()
	// A track tagged "Various Artists" in the matching folder: the compilation
	// guard passes, but a grouping artist must never get a portrait — it shows
	// its placeholder avatar instead.
	artistDir := filepath.Join(root, "Various Artists")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	os.WriteFile(filepath.Join(artistDir, "artists.jpg"), []byte("x"), 0o644)
	track := filepath.Join(albumDir, "01 - Track.flac")
	os.WriteFile(track, minimalFLAC("Various Artists", "Album", "Track", 2020, 1, 1), 0o644)

	store := &mockStore{
		artists: map[string]int64{"Various Artists": 1},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
	}
	scanner := NewScanner(store, testLogger())

	scanner.recordArtistThumb(t.Context(), track, artistDir)

	if _, set := store.thumbs[1]; set {
		t.Error("compilation grouping artist must not get a portrait")
	}
}

func TestWriteAlbumCoverDoesNotDuplicateArtistImage(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	track := filepath.Join(albumDir, "01 - Track.flac")

	store := &mockStore{artists: map[string]int64{}, albums: map[string]int64{}}
	scanner := NewScanner(store, testLogger())

	png := &tag.Picture{Ext: "png", MIMEType: "image/png", Data: []byte("pngdata")}
	jpg := &tag.Picture{Ext: "jpg", MIMEType: "image/jpeg", Data: []byte("jpgdata")}

	scanner.writeAlbumCover(t.Context(), track, root, "Artist", png)
	if _, err := os.Stat(filepath.Join(artistDir, "artist.png")); err != nil {
		t.Fatalf("expected artist.png written: %v", err)
	}

	scanner.writeAlbumCover(t.Context(), track, root, "Artist", jpg)
	if _, err := os.Stat(filepath.Join(artistDir, "artist.png")); err != nil {
		t.Fatalf("artist.png should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(artistDir, "artist.jpg")); err == nil {
		t.Error("artist.jpg should not be added when artist.png already exists")
	}
}

func TestRecordArtistThumb(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	// A pre-existing portrait under the common "artists.jpg" name.
	os.WriteFile(filepath.Join(artistDir, "artists.jpg"), []byte("x"), 0o644)
	track := filepath.Join(albumDir, "01 - Track.flac")

	store := &mockStore{
		artists: map[string]int64{"Artist": 1},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
	}
	scanner := NewScanner(store, testLogger())

	scanner.recordArtistThumb(t.Context(), track, artistDir)

	if got := store.thumbs[1]; got != "artists.jpg" {
		t.Fatalf("thumb = %q, want artists.jpg (pre-existing portrait surfaced)", got)
	}
}

func TestRecordArtistThumbDoesNotClobberRemote(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(albumDir, 0o755)
	os.WriteFile(filepath.Join(artistDir, "artist.jpg"), []byte("x"), 0o644)
	track := filepath.Join(albumDir, "01 - Track.flac")

	store := &mockStore{
		artists: map[string]int64{"Artist": 1},
		albums:  map[string]int64{},
		thumbs:  map[int64]string{},
	}
	// Simulate an artist with a remote portrait (e.g. from an external source).
	store.remoteThumbs = map[string]string{"Artist": "https://example.com/artist.jpg"}
	scanner := NewScanner(store, testLogger())

	scanner.recordArtistThumb(t.Context(), track, artistDir)

	if _, set := store.thumbs[1]; set {
		t.Error("remote thumb_url was clobbered")
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
	scanner := NewScanner(store, testLogger())

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
	artists      map[string]int64
	albums       map[string]int64
	tracks       []domain.Track
	thumbs       map[int64]string
	remoteThumbs map[string]string // artist name → pre-existing thumb_url
	upsertErr    error             // when set, UpsertTrack fails (organize rollback tests)
	nextID       int64
}

func (m *mockStore) next() int64 { m.nextID++; return m.nextID }
func (m *mockStore) UpsertArtist(ctx context.Context, a *domain.Artist) (int64, error) {
	if id, ok := m.artists[a.Name]; ok {
		return id, nil
	}
	id := m.next()
	m.artists[a.Name] = id
	return id, nil
}
func (m *mockStore) GetArtist(ctx context.Context, id int64) (*domain.Artist, error) { return nil, nil }
func (m *mockStore) GetArtistByName(ctx context.Context, name string) (*domain.Artist, error) {
	if id, ok := m.artists[name]; ok {
		a := &domain.Artist{ID: id, Name: name}
		if m.remoteThumbs != nil {
			a.ThumbURL = m.remoteThumbs[name]
		}
		return a, nil
	}
	return nil, nil
}
func (m *mockStore) ListArtists(ctx context.Context, offset, limit int) ([]domain.Artist, error) {
	return nil, nil
}
func (m *mockStore) SearchArtists(ctx context.Context, query string, limit int) ([]domain.Artist, error) {
	return nil, nil
}
func (m *mockStore) SetArtistThumbURL(ctx context.Context, artistID int64, thumbURL string) error {
	if m.thumbs != nil {
		m.thumbs[artistID] = thumbURL
	}
	return nil
}
func (m *mockStore) MergeArtists(ctx context.Context, keepID, removeID int64) error      { return nil }
func (m *mockStore) RenameArtist(ctx context.Context, artistID int64, name string) error { return nil }
func (m *mockStore) UpsertAlbum(ctx context.Context, a *domain.Album) (int64, error) {
	key := fmt.Sprintf("%d:%s", a.ArtistID, a.Title)
	if id, ok := m.albums[key]; ok {
		return id, nil
	}
	id := m.next()
	m.albums[key] = id
	return id, nil
}
func (m *mockStore) GetAlbum(ctx context.Context, id int64) (*domain.Album, error) { return nil, nil }
func (m *mockStore) GetAlbumsByArtist(ctx context.Context, artistID int64) ([]domain.Album, error) {
	return nil, nil
}
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
	if m.upsertErr != nil {
		return 0, m.upsertErr
	}
	if t.ID != 0 {
		for i := range m.tracks {
			if m.tracks[i].ID == t.ID {
				m.tracks[i] = *t
				return t.ID, nil
			}
		}
	}
	id := m.next()
	t.ID = id
	m.tracks = append(m.tracks, *t)
	return id, nil
}
func (m *mockStore) GetTrack(ctx context.Context, id int64) (*domain.Track, error) { return nil, nil }
func (m *mockStore) GetTracksByAlbum(ctx context.Context, albumID int64) ([]domain.Track, error) {
	return nil, nil
}
func (m *mockStore) GetTracksByArtist(ctx context.Context, artistID int64) ([]domain.Track, error) {
	return nil, nil
}
func (m *mockStore) SearchTracks(ctx context.Context, query string, limit int) ([]domain.Track, error) {
	return nil, nil
}
func (m *mockStore) GetTrackByFilePath(ctx context.Context, fp string) (*domain.Track, error) {
	for _, t := range m.tracks {
		if t.FilePath == fp {
			return &t, nil
		}
	}
	return nil, nil
}
func (m *mockStore) GetTrackByISRC(ctx context.Context, isrc string) (*domain.Track, error) {
	return nil, nil
}
func (m *mockStore) DeleteTrack(ctx context.Context, id int64) error { return nil }
func (m *mockStore) GetArtistByExternalID(ctx context.Context, svc, eid string) (*domain.Artist, error) {
	return nil, nil
}
func (m *mockStore) GetAlbumByExternalID(ctx context.Context, svc, eid string) (*domain.Album, error) {
	return nil, nil
}
func (m *mockStore) GetTrackByExternalID(ctx context.Context, svc, eid string) (*domain.Track, error) {
	return nil, nil
}
func (m *mockStore) UpsertPlaylist(ctx context.Context, p *domain.Playlist) (int64, error) {
	return 0, nil
}
func (m *mockStore) GetPlaylist(ctx context.Context, id int64) (*domain.Playlist, error) {
	return nil, nil
}
func (m *mockStore) GetPlaylistBySourceID(ctx context.Context, source, sourceID string) (*domain.Playlist, error) {
	return nil, nil
}
func (m *mockStore) ListPlaylists(ctx context.Context) ([]domain.Playlist, error) { return nil, nil }
func (m *mockStore) CountPlaylistsByName(ctx context.Context, source, name string) (int64, error) {
	return 0, nil
}
func (m *mockStore) DeletePlaylist(ctx context.Context, id int64) error           { return nil }
func (m *mockStore) UpsertPlaylistTrack(ctx context.Context, t *domain.PlaylistTrack) error {
	return nil
}
func (m *mockStore) GetPlaylistTracks(ctx context.Context, playlistID int64) ([]domain.PlaylistTrack, error) {
	return nil, nil
}
func (m *mockStore) DeletePlaylistTracks(ctx context.Context, playlistID int64) error { return nil }
func (m *mockStore) Close() error                                                     { return nil }
func (m *mockStore) ListTracksWithQuality(ctx context.Context) ([]domain.Track, error) {
	return nil, nil
}
func (m *mockStore) ImportTrack(ctx context.Context, track *domain.Track, artistName, albumTitle string, albumYear int, genres []string) (int64, error) {
	return m.UpsertTrack(ctx, track)
}

// minimalFLAC creates a valid minimal FLAC file with Vorbis comments.
// The file contains enough structure for dhowden/tag to parse artist, album,
// title, year, track number, and disc number.
func minimalFLAC(artist, album, title string, year, trackNum, discNum int) []byte {
	var buf bytes.Buffer

	// FLAC marker.
	buf.WriteString("fLaC")

	// STREAMINFO metadata block (last block = false, since vorbis comment follows).
	// Block header: 1 byte (0x00 = not last) + 3 byte BE length.
	streamInfoData := make([]byte, 34)
	// Minimum block size (2 bytes): 4096
	binary.BigEndian.PutUint16(streamInfoData[0:2], 4096)
	// Maximum block size (2 bytes): 4096
	binary.BigEndian.PutUint16(streamInfoData[2:4], 4096)
	// Minimum frame size (3 bytes): 0
	// Maximum frame size (3 bytes): 0
	// Sample rate (20 bits) + channels (3 bits) + bits per sample (5 bits) + total samples (36 bits)
	// Sample rate: 44100 = 0xAC44, shifted left 4: 0xAC440
	binary.BigEndian.PutUint32(streamInfoData[10:14], 0x0AC44000) // sample_rate=44100, channels=2, bps=16
	binary.BigEndian.PutUint32(streamInfoData[14:18], 0x00000000) // total samples upper
	binary.BigEndian.PutUint32(streamInfoData[18:22], 0x00000000) // total samples lower
	buf.WriteByte(0x00)                                           // not last block
	writeUint24BE(&buf, 34)                                       // length
	buf.Write(streamInfoData)                                     // STREAMINFO data

	// Vorbis comment block.
	var commentsBuf bytes.Buffer
	vendor := "reference libFLAC 1.3.2 20170101"
	writeVorbisString(&commentsBuf, vendor)
	// Comment count: 6 (ARTIST, ALBUM, TITLE, DATE, TRACKNUMBER, DISCNUMBER)
	writeUint32LE(&commentsBuf, 6)
	writeVorbisComment(&commentsBuf, "ARTIST", artist)
	writeVorbisComment(&commentsBuf, "ALBUM", album)
	writeVorbisComment(&commentsBuf, "TITLE", title)
	writeVorbisComment(&commentsBuf, "DATE", fmt.Sprintf("%d", year))
	writeVorbisComment(&commentsBuf, "TRACKNUMBER", fmt.Sprintf("%d", trackNum))
	writeVorbisComment(&commentsBuf, "DISCNUMBER", fmt.Sprintf("%d", discNum))

	commentsData := commentsBuf.Bytes()
	buf.WriteByte(0x84) // last block = true (0x80) | block type = 4 (Vorbis comment)
	writeUint24BE(&buf, len(commentsData))
	buf.Write(commentsData)

	// Append FLAC frame sync bytes so go-flac v1 parser has audio data to parse.
	buf.Write([]byte{0xFF, 0xF8, 0x00, 0x00})

	return buf.Bytes()
}

// writeUint24BE writes a 24-bit big-endian integer.
func writeUint24BE(buf *bytes.Buffer, v int) {
	buf.WriteByte(byte(v >> 16))
	buf.WriteByte(byte(v >> 8))
	buf.WriteByte(byte(v))
}

// writeUint32LE writes a 32-bit little-endian integer.
func writeUint32LE(buf *bytes.Buffer, v int) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(v))
	buf.Write(b[:])
}

// writeVorbisString writes a string with a 4-byte LE length prefix (Vorbis format).
func writeVorbisString(buf *bytes.Buffer, s string) {
	writeUint32LE(buf, len(s))
	buf.WriteString(s)
}

// writeVorbisComment writes a KEY=VALUE pair in Vorbis comment format.
func writeVorbisComment(buf *bytes.Buffer, key, value string) {
	writeVorbisString(buf, key+"="+value)
}

func TestAlbumDirFromTrack(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	// Multi-disc album: Disc 1 needs a Disc 2 sibling to be recognized.
	os.MkdirAll(filepath.Join(albumDir, "Disc 2"), 0o755)
	// An album literally named "Disc 1" — parent has no disc-named sibling.
	literalDiscAlbum := filepath.Join(artistDir, "Disc 1")
	os.MkdirAll(literalDiscAlbum, 0o755)

	tests := []struct {
		name string
		path string
		want string
	}{
		{"standard", filepath.Join(albumDir, "01 - Track.flac"), albumDir},
		{"multi-disc", filepath.Join(albumDir, "Disc 1", "01 - Track.flac"), albumDir},
		{"multi-disc-cd", filepath.Join(albumDir, "CD2", "02 - Track.flac"), albumDir},
		{"disc-named-album", filepath.Join(literalDiscAlbum, "01 - Track.flac"), literalDiscAlbum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AlbumDirFromTrack(tt.path); got != tt.want {
				t.Errorf("AlbumDirFromTrack(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestArtistDirFromTrack(t *testing.T) {
	root := t.TempDir()
	artistDir := filepath.Join(root, "Artist")
	albumDir := filepath.Join(artistDir, "Album")
	os.MkdirAll(filepath.Join(albumDir, "Disc 2"), 0o755)

	tests := []struct {
		name string
		path string
		want string
	}{
		{"standard", filepath.Join(albumDir, "01 - Track.flac"), artistDir},
		{"multi-disc", filepath.Join(albumDir, "Disc 1", "01 - Track.flac"), artistDir},
		{"multi-disc-cd", filepath.Join(albumDir, "CD2", "02 - Track.flac"), artistDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ArtistDirFromTrack(tt.path); got != tt.want {
				t.Errorf("ArtistDirFromTrack(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
