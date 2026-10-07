package api

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// stubTrackStore embeds the library.Store interface and overrides only
// GetTrack, the one method the download handler calls. Any other method panics
// when invoked, which keeps these narrow handler tests honest.
type stubTrackStore struct {
	library.Store
	tracks map[int64]*domain.Track
	err    error
}

func (s *stubTrackStore) GetTrack(_ context.Context, id int64) (*domain.Track, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tracks[id], nil
}

// newDownloadServer builds a Server wired to a temp library root and a stub
// store. A missing map entry reads as a nil track (unknown id).
func newDownloadServer(t *testing.T, root string, tracks map[int64]*domain.Track) *Server {
	t.Helper()
	p, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Update(func(c *config.Config) error {
		c.Library.LibraryPath = root
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: p, store: &stubTrackStore{tracks: tracks}, log: testAPILogger()}
}

func TestAudioContentType(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"track.flac", "audio/flac"},
		{"track.FLAC", "audio/flac"},
		{"track.mp3", "audio/mpeg"},
		{"track.m4a", "audio/mp4"},
		{"track.opus", "audio/ogg"},
		{"track.ogg", "audio/ogg"},
		{"track.wav", "audio/wav"},
		{"track.aac", "audio/aac"},
		{"track.unknownext", "application/octet-stream"},
		{"noextension", "application/octet-stream"},
		{"", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := audioContentType(tt.name); got != tt.want {
				t.Errorf("audioContentType(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestContentDispositionAttachment(t *testing.T) {
	t.Run("ascii name in both parameters", func(t *testing.T) {
		got := contentDispositionAttachment("01 - Song.flac")
		if !strings.HasPrefix(got, "attachment;") {
			t.Errorf("missing attachment disposition: %q", got)
		}
		if !strings.Contains(got, `filename="01 - Song.flac"`) {
			t.Errorf("missing ascii filename parameter: %q", got)
		}
		if !strings.Contains(got, "filename*=UTF-8''01%20-%20Song.flac") {
			t.Errorf("missing/nwrong filename* parameter: %q", got)
		}
	})

	t.Run("non-ascii name keeps RFC 5987 encoding", func(t *testing.T) {
		got := contentDispositionAttachment("Café.flac")
		// Legacy fallback must stay ASCII-safe.
		if !strings.Contains(got, `filename="Caf_.flac"`) {
			t.Errorf("ascii fallback not sanitized: %q", got)
		}
		// RFC 5987 form preserves the real bytes.
		if !strings.Contains(got, "filename*=UTF-8''Caf%C3%A9.flac") {
			t.Errorf("non-ascii filename* encoding wrong: %q", got)
		}
	})
}

func TestHandleLibraryTrackDownload(t *testing.T) {
	root := t.TempDir()
	body := []byte("FLAC-BYTES-0123456789")

	trackPath := filepath.Join(root, "Artist", "Album", "01 - Song.flac")
	if err := os.MkdirAll(filepath.Dir(trackPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trackPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	// A real, readable file that lives OUTSIDE the library root — proving the
	// rejection is containment, not just a missing file.
	outsidePath := filepath.Join(t.TempDir(), "secret.flac")
	if err := os.WriteFile(outsidePath, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracks := map[int64]*domain.Track{
		1: {ID: 1, FilePath: trackPath},
		2: {ID: 2, FilePath: ""},
		3: {ID: 3, FilePath: outsidePath},
		4: {ID: 4, FilePath: "../../etc/passwd"},
	}
	s := newDownloadServer(t, root, tracks)

	request := func(id, method string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/library/tracks/"+id+"/download", nil)
		req.SetPathValue("trackID", id)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.handleLibraryTrackDownload(rec, req)
		return rec
	}

	assertNoLeak := func(t *testing.T, rec *httptest.ResponseRecorder, secrets ...string) {
		t.Helper()
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
		}
		b := rec.Body.String()
		for _, secret := range secrets {
			if secret != "" && strings.Contains(b, secret) {
				t.Errorf("404 body leaked server path %q: %q", secret, b)
			}
		}
		if strings.Contains(b, "outside") || strings.Contains(b, "resolve") {
			t.Errorf("404 body leaked resolver text: %q", b)
		}
	}

	t.Run("200 streams exact bytes with audio type and attachment", func(t *testing.T) {
		rec := request("1", http.MethodGet, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		if got := rec.Body.Bytes(); !bytes.Equal(got, body) {
			t.Errorf("body = %q, want %q", got, body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "audio/flac" {
			t.Errorf("Content-Type = %q, want audio/flac", ct)
		}
		if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", xcto)
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
			t.Errorf("Content-Disposition = %q, want attachment", cd)
		}
	})

	t.Run("Range returns 206 partial bytes", func(t *testing.T) {
		rec := request("1", http.MethodGet, map[string]string{"Range": "bytes=0-3"})
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
		if got := rec.Body.Bytes(); !bytes.Equal(got, body[:4]) {
			t.Errorf("partial body = %q, want %q", got, body[:4])
		}
		if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
			t.Errorf("Accept-Ranges = %q, want bytes", ar)
		}
		if cr := rec.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 0-3/") {
			t.Errorf("Content-Range = %q, want bytes 0-3/*", cr)
		}
	})

	t.Run("HEAD returns headers with no body", func(t *testing.T) {
		rec := request("1", http.MethodHead, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("HEAD body = %q, want empty", rec.Body.Bytes())
		}
		if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(body)) {
			t.Errorf("Content-Length = %q, want %d", cl, len(body))
		}
	})

	t.Run("404 unknown id", func(t *testing.T) {
		assertNoLeak(t, request("999", http.MethodGet, nil), root)
	})

	t.Run("404 empty FilePath", func(t *testing.T) {
		assertNoLeak(t, request("2", http.MethodGet, nil), root)
	})

	t.Run("404 non-numeric id", func(t *testing.T) {
		assertNoLeak(t, request("abc", http.MethodGet, nil), root)
	})

	t.Run("404 traversal outside root", func(t *testing.T) {
		assertNoLeak(t, request("3", http.MethodGet, nil), root, outsidePath)
	})

	t.Run("404 relative traversal", func(t *testing.T) {
		assertNoLeak(t, request("4", http.MethodGet, nil), root, "/etc/passwd")
	})
}

// deadlineRecorder is a deterministic http.ResponseWriter for H4 tests. It
// implements SetWriteDeadline (which http.ResponseController discovers) and
// records every deadline it is handed, plus how many Write calls the stream
// produced. Embedding the recorder keeps all normal ResponseWriter behavior.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlineCalls int
	deadline      time.Time
	writes        int
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadlineCalls++
	d.deadline = t
	return nil
}

func (d *deadlineRecorder) Write(p []byte) (int, error) {
	d.writes++
	return d.ResponseRecorder.Write(p)
}

func TestExtendWriteDeadline(t *testing.T) {
	t.Run("sets a finite future deadline when the writer supports it", func(t *testing.T) {
		rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		before := time.Now()
		if err := extendWriteDeadline(rec); err != nil {
			t.Fatalf("extendWriteDeadline() error = %v, want nil", err)
		}
		if rec.deadlineCalls != 1 {
			t.Errorf("SetWriteDeadline calls = %d, want 1", rec.deadlineCalls)
		}
		// An unbounded (zero) deadline is the H4 defect: it lets a stalled
		// authenticated client pin a connection forever.
		if rec.deadline.IsZero() {
			t.Fatal("deadline = zero, want a finite cap (unbounded clearing is the defect)")
		}
		if !rec.deadline.After(before) {
			t.Errorf("deadline = %v, want a time after the call", rec.deadline)
		}
		// Allow scheduling slack around the configured cap.
		if upper := before.Add(streamWriteDeadline + time.Minute); rec.deadline.After(upper) {
			t.Errorf("deadline = %v, want at most %v (streamWriteDeadline = %v)", rec.deadline, upper, streamWriteDeadline)
		}
	})

	t.Run("unsupported writer returns an ignorable error", func(t *testing.T) {
		// httptest.ResponseRecorder does not implement SetWriteDeadline, so
		// http.ResponseController reports ErrNotSupported. The handler treats
		// this as non-fatal; this test pins that contract.
		if err := extendWriteDeadline(httptest.NewRecorder()); !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("extendWriteDeadline() error = %v, want http.ErrNotSupported", err)
		}
	})
}

// TestHandleLibraryTrackDownloadExtendsDeadline proves the handler extends the
// per-request write deadline (to a finite cap) and still streams a multi-chunk
// body. A real >30s download is impractical in a unit test; the custom writer
// makes the deadline extension deterministic without any sleep.
func TestHandleLibraryTrackDownloadExtendsDeadline(t *testing.T) {
	root := t.TempDir()

	// >32 KiB so io.Copy inside ServeContent emits several Write calls; this
	// exercises the streaming path without relying on wall-clock time.
	body := bytes.Repeat([]byte("groovearr-stream-chunk!"), 8*1024) // ~172 KiB
	trackPath := filepath.Join(root, "Artist", "Album", "01 - Long.flac")
	if err := os.MkdirAll(filepath.Dir(trackPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trackPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newDownloadServer(t, root, map[int64]*domain.Track{
		1: {ID: 1, FilePath: trackPath},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/library/tracks/1/download", nil)
	req.SetPathValue("trackID", "1")
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	s.handleLibraryTrackDownload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, body) {
		t.Errorf("streamed %d bytes, want %d", len(got), len(body))
	}
	if rec.deadlineCalls != 1 {
		t.Fatalf("SetWriteDeadline calls = %d, want 1 (handler must extend the deadline)", rec.deadlineCalls)
	}
	if rec.deadline.IsZero() {
		t.Errorf("deadline = zero, want a finite cap for a streaming download")
	}
	if !rec.deadline.After(time.Now()) {
		t.Errorf("deadline = %v, want a future time", rec.deadline)
	}
	if rec.writes < 2 {
		t.Errorf("Write calls = %d, want >= 2 (multi-chunk stream)", rec.writes)
	}
}

// TestDownloadRouteExtendsDeadlineThroughRealChain is the H4 regression at the
// wiring level: it drives the real download route through the ACTUAL production
// middleware chain (withAccessLog -> withRequestID -> withCORS -> withAuth ->
// mux) with a deadline-capable base writer. Before responseWriter grew Unwrap,
// http.NewResponseController could not see through the access-log wrapper,
// SetWriteDeadline returned http.ErrNotSupported, and the global 30s
// WriteTimeout silently cut every large stream. This test fails without Unwrap.
func TestDownloadRouteExtendsDeadlineThroughRealChain(t *testing.T) {
	root := t.TempDir()
	body := bytes.Repeat([]byte("groovearr-stream-chunk!"), 8*1024) // ~172 KiB
	trackPath := filepath.Join(root, "Artist", "Album", "01 - Long.flac")
	if err := os.MkdirAll(filepath.Dir(trackPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trackPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newDownloadServer(t, root, map[int64]*domain.Track{
		1: {ID: 1, FilePath: trackPath},
	})
	// registerAPIRoutes wires the real route table, which needs the limiter the
	// production server always has.
	limiter := newIPRateLimiter(roleTestBuckets(), testAPILogger())
	t.Cleanup(limiter.Shutdown)
	s.rateLimiter = limiter

	mux := http.NewServeMux()
	s.registerAPIRoutes(mux)

	// The exact production chain built in NewServer.
	var accessBuf bytes.Buffer
	handler := withAccessLog(&accessBuf)(withRequestID(withCORS(s.withAuth(mux))))

	// Deadline-capable base writer: http.ResponseController can only reach it
	// if responseWriter implements Unwrap.
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/api/library/tracks/1/download", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, body) {
		t.Errorf("streamed %d bytes, want %d", len(got), len(body))
	}
	if rec.deadlineCalls == 0 {
		t.Fatal("SetWriteDeadline never reached the base writer: responseWriter.Unwrap is missing or broken (H4 regression)")
	}
	if rec.deadline.IsZero() {
		t.Fatal("deadline was cleared to zero, want a finite cap (an unbounded stream pins a connection)")
	}
	if !rec.deadline.After(time.Now()) {
		t.Errorf("deadline = %v, want a future time", rec.deadline)
	}
}

func TestHandleLibraryTrackDownloadStoreError(t *testing.T) {
	s := newDownloadServer(t, t.TempDir(), nil)
	s.store = &stubTrackStore{err: errors.New("db down at /srv/music/tracks.db")}

	req := httptest.NewRequest(http.MethodGet, "/api/library/tracks/1/download", nil)
	req.SetPathValue("trackID", "1")
	rec := httptest.NewRecorder()
	s.handleLibraryTrackDownload(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "db down") || strings.Contains(body, "/srv/music") {
		t.Errorf("500 body leaked internal error detail: %q", body)
	}
}

// stubAlbumStore embeds the library.Store interface and overrides only the
// three methods the album download handler calls. The album's id is not
// distinguished — a nil album models an unknown id.
type stubAlbumStore struct {
	library.Store
	album     *domain.Album
	tracks    []domain.Track
	artist    *domain.Artist
	artistErr error
}

func (s *stubAlbumStore) GetAlbum(_ context.Context, _ int64) (*domain.Album, error) {
	return s.album, nil
}

func (s *stubAlbumStore) GetTracksByAlbum(_ context.Context, _ int64) ([]domain.Track, error) {
	return s.tracks, nil
}

func (s *stubAlbumStore) GetArtist(_ context.Context, _ int64) (*domain.Artist, error) {
	if s.artistErr != nil {
		return nil, s.artistErr
	}
	return s.artist, nil
}

// newAlbumDownloadServer wires a Server to a temp library root and a stub store.
func newAlbumDownloadServer(t *testing.T, root string, store *stubAlbumStore) *Server {
	t.Helper()
	p, err := config.LoadOrCreate(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Update(func(c *config.Config) error {
		c.Library.LibraryPath = root
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: p, store: store, log: testAPILogger()}
}

// writeAlbumFile creates path (with parents) holding body and returns the path.
func writeAlbumFile(t *testing.T, path string, body []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func requestAlbum(s *Server, id string) *httptest.ResponseRecorder {
	return requestAlbumMethod(s, id, http.MethodGet)
}

// requestAlbumMethod drives the album download handler with an explicit HTTP
// method. Go's ServeMux routes HEAD through the GET handler, so the handler
// itself must recognize HEAD.
func requestAlbumMethod(s *Server, id, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/library/albums/"+id+"/download", nil)
	req.SetPathValue("albumID", id)
	rec := httptest.NewRecorder()
	s.handleLibraryAlbumDownload(rec, req)
	return rec
}

func openZip(t *testing.T, rec *httptest.ResponseRecorder) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v (body %d bytes)", err, rec.Body.Len())
	}
	return zr
}

func zipNames(zr *zip.Reader) []string {
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func zipEntryBytes(t *testing.T, f *zip.File) []byte {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("open zip entry %q: %v", f.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read zip entry %q: %v", f.Name, err)
	}
	return b
}

func TestHandleLibraryAlbumDownload(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")

	firstBytes := []byte("FIRST-FLAC-BYTES")
	secondBytes := []byte("SECOND-MP3-BYTES")
	thirdBytes := []byte("THIRD-FLAC-BYTES")
	first := writeAlbumFile(t, filepath.Join(albumDir, "01 - First.flac"), firstBytes)
	second := writeAlbumFile(t, filepath.Join(albumDir, "02 - Second.mp3"), secondBytes)
	third := writeAlbumFile(t, filepath.Join(albumDir, "03 - Third.flac"), thirdBytes)

	singleDisc := &stubAlbumStore{
		album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
		artist: &domain.Artist{ID: 3, Name: "Artist"},
		tracks: []domain.Track{
			{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "First", FilePath: first},
			{ID: 2, TrackNumber: 2, DiscNumber: 1, Title: "Second", FilePath: second},
			{ID: 3, TrackNumber: 3, DiscNumber: 1, Title: "Third", FilePath: third},
		},
	}

	t.Run("single disc streams ordered entries with matching bytes and headers", func(t *testing.T) {
		s := newAlbumDownloadServer(t, root, singleDisc)
		rec := requestAlbum(s, "7")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
			t.Errorf("Content-Type = %q, want application/zip", ct)
		}
		if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", xcto)
		}
		cd := rec.Header().Get("Content-Disposition")
		if !strings.Contains(cd, "attachment") {
			t.Errorf("Content-Disposition = %q, want attachment", cd)
		}
		if !strings.Contains(cd, `filename="Artist - Album.zip"`) {
			t.Errorf("Content-Disposition = %q, want sanitized filename", cd)
		}

		zr := openZip(t, rec)
		want := []string{"01 - First.flac", "02 - Second.mp3", "03 - Third.flac"}
		got := zipNames(zr)
		if !stringSlicesEqual(got, want) {
			t.Fatalf("entry names = %v, want %v", got, want)
		}
		if len(zr.File) != 3 {
			t.Fatalf("entry count = %d, want 3", len(zr.File))
		}
		for i, want := range [][]byte{firstBytes, secondBytes, thirdBytes} {
			if got := zipEntryBytes(t, zr.File[i]); !bytes.Equal(got, want) {
				t.Errorf("entry %q bytes = %q, want %q", zr.File[i].Name, got, want)
			}
		}
	})

	t.Run("multi-disc album prefixes the disc number", func(t *testing.T) {
		disc1 := writeAlbumFile(t, filepath.Join(albumDir, "disc1.flac"), []byte("D1"))
		disc2 := writeAlbumFile(t, filepath.Join(albumDir, "disc2.flac"), []byte("D2"))
		st := &stubAlbumStore{
			album:  &domain.Album{ID: 8, ArtistID: 3, Title: "Album"},
			artist: &domain.Artist{ID: 3, Name: "Artist"},
			tracks: []domain.Track{
				{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "One", FilePath: disc1},
				{ID: 2, TrackNumber: 1, DiscNumber: 2, Title: "Two", FilePath: disc2},
			},
		}
		s := newAlbumDownloadServer(t, root, st)
		zr := openZip(t, requestAlbum(s, "8"))
		want := []string{"01-01 - One.flac", "02-01 - Two.flac"}
		if got := zipNames(zr); !stringSlicesEqual(got, want) {
			t.Fatalf("entry names = %v, want %v", got, want)
		}
	})

	t.Run("colliding sanitized names are deduplicated", func(t *testing.T) {
		a := writeAlbumFile(t, filepath.Join(albumDir, "dup-a.flac"), []byte("A"))
		b := writeAlbumFile(t, filepath.Join(albumDir, "dup-b.flac"), []byte("B"))
		st := &stubAlbumStore{
			album:  &domain.Album{ID: 9, ArtistID: 3, Title: "Album"},
			artist: &domain.Artist{ID: 3, Name: "Artist"},
			tracks: []domain.Track{
				{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Same", FilePath: a},
				{ID: 2, TrackNumber: 1, DiscNumber: 1, Title: "Same", FilePath: b},
			},
		}
		s := newAlbumDownloadServer(t, root, st)
		zr := openZip(t, requestAlbum(s, "9"))
		got := zipNames(zr)
		want := []string{"01 - Same.flac", "01 - Same (2).flac"}
		if !stringSlicesEqual(got, want) {
			t.Fatalf("entry names = %v, want %v", got, want)
		}
		seen := map[string]bool{}
		for _, n := range got {
			if seen[n] {
				t.Errorf("duplicate entry name %q", n)
			}
			seen[n] = true
		}
	})

	t.Run("album with no serveable files returns 404 without leaking paths", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.flac")
		if err := os.WriteFile(outside, []byte("SECRET"), 0o644); err != nil {
			t.Fatal(err)
		}
		st := &stubAlbumStore{
			album:  &domain.Album{ID: 10, ArtistID: 3, Title: "Album"},
			artist: &domain.Artist{ID: 3, Name: "Artist"},
			tracks: []domain.Track{
				{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Empty", FilePath: ""},
				{ID: 2, TrackNumber: 2, DiscNumber: 1, Title: "Outside", FilePath: outside},
			},
		}
		s := newAlbumDownloadServer(t, root, st)
		rec := requestAlbum(s, "10")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if strings.Contains(body, outside) || strings.Contains(body, root) {
			t.Errorf("404 body leaked a server path: %q", body)
		}
	})

	t.Run("unknown album returns 404", func(t *testing.T) {
		s := newAlbumDownloadServer(t, root, &stubAlbumStore{album: nil})
		if rec := requestAlbum(s, "999"); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("non-numeric album id returns 404", func(t *testing.T) {
		s := newAlbumDownloadServer(t, root, singleDisc)
		if rec := requestAlbum(s, "abc"); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}

// zipEntry returns the entry with the given name, or nil when absent.
func zipEntry(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// TestHandleLibraryAlbumDownloadCover pins the conditional, lookup-only cover
// entry (Phase 2.3): present on disk => streamed in byte-identically under its
// original basename; absent or escaping the root => skipped silently while the
// zip still succeeds.
func TestHandleLibraryAlbumDownloadCover(t *testing.T) {
	trackBytes := []byte("TRACK-BYTES")

	newStore := func(trackPath string) *stubAlbumStore {
		return &stubAlbumStore{
			album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
			artist: &domain.Artist{ID: 3, Name: "Artist"},
			tracks: []domain.Track{
				{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Song", FilePath: trackPath},
			},
		}
	}

	t.Run("cover present is added under its basename with matching bytes", func(t *testing.T) {
		root := t.TempDir()
		albumDir := filepath.Join(root, "Artist", "Album")
		track := writeAlbumFile(t, filepath.Join(albumDir, "01 - Song.flac"), trackBytes)
		coverBytes := []byte("JPEG-COVER-BYTES-0123456789")
		writeAlbumFile(t, filepath.Join(albumDir, "cover.jpg"), coverBytes)

		s := newAlbumDownloadServer(t, root, newStore(track))
		rec := requestAlbum(s, "7")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}

		zr := openZip(t, rec)
		want := []string{"01 - Song.flac", "cover.jpg"}
		if got := zipNames(zr); !stringSlicesEqual(got, want) {
			t.Fatalf("entry names = %v, want %v", got, want)
		}
		entry := zipEntry(zr, "cover.jpg")
		if entry == nil {
			t.Fatalf("cover.jpg entry missing from zip: %v", zipNames(zr))
		}
		if got := zipEntryBytes(t, entry); !bytes.Equal(got, coverBytes) {
			t.Errorf("cover.jpg bytes = %q, want %q", got, coverBytes)
		}
	})

	t.Run("no cover on disk omits the entry and still succeeds", func(t *testing.T) {
		root := t.TempDir()
		albumDir := filepath.Join(root, "Artist", "Album")
		track := writeAlbumFile(t, filepath.Join(albumDir, "01 - Song.flac"), trackBytes)

		s := newAlbumDownloadServer(t, root, newStore(track))
		rec := requestAlbum(s, "7")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}

		zr := openZip(t, rec) // also proves the archive is valid
		if got := zipNames(zr); !stringSlicesEqual(got, []string{"01 - Song.flac"}) {
			t.Fatalf("entry names = %v, want [01 - Song.flac]", got)
		}
		if entry := zipEntry(zr, "cover.jpg"); entry != nil {
			t.Errorf("unexpected cover.jpg entry in a cover-less album zip")
		}
	})

	t.Run("cover escaping the library root is skipped silently", func(t *testing.T) {
		root := t.TempDir()
		albumDir := filepath.Join(root, "Artist", "Album")
		track := writeAlbumFile(t, filepath.Join(albumDir, "01 - Song.flac"), trackBytes)

		// cover.jpg is a symlink pointing at a real file outside the root;
		// CoverFilePath sees it, ResolveWithinRoot rejects the escape.
		outside := writeAlbumFile(t, filepath.Join(t.TempDir(), "outside.jpg"), []byte("SECRET"))
		if err := os.Symlink(outside, filepath.Join(albumDir, "cover.jpg")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}

		s := newAlbumDownloadServer(t, root, newStore(track))
		rec := requestAlbum(s, "7")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		zr := openZip(t, rec)
		if entry := zipEntry(zr, "cover.jpg"); entry != nil {
			t.Errorf("escaping cover was included in the zip")
		}
	})
}

// TestHandleLibraryAlbumDownloadPlaylists pins the conditional, pass-through
// playlist entry (Phase 2.4): an existing .m3u/.m3u8 in the album tree is
// streamed in verbatim under its album-relative, slash-separated path (subdir
// layout preserved); none on disk => no entry and the zip still succeeds.
func TestHandleLibraryAlbumDownloadPlaylists(t *testing.T) {
	trackBytes := []byte("TRACK-BYTES")

	newStore := func(trackPath string) *stubAlbumStore {
		return &stubAlbumStore{
			album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
			artist: &domain.Artist{ID: 3, Name: "Artist"},
			tracks: []domain.Track{
				{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Song", FilePath: trackPath},
			},
		}
	}

	t.Run("playlists pass through byte-identically with subdir layout", func(t *testing.T) {
		root := t.TempDir()
		albumDir := filepath.Join(root, "Artist", "Album")
		track := writeAlbumFile(t, filepath.Join(albumDir, "01 - Song.flac"), trackBytes)

		m3uBytes := []byte("#EXTM3U\n#EXTINF:1,Song\n01 - Song.flac\n")
		subBytes := []byte("#EXTM3U\n#EXTINF:2,Other\n../Other/02 - Other.flac\n")
		writeAlbumFile(t, filepath.Join(albumDir, "album.m3u"), m3uBytes)
		writeAlbumFile(t, filepath.Join(albumDir, "discs", "extra.m3u8"), subBytes)

		s := newAlbumDownloadServer(t, root, newStore(track))
		rec := requestAlbum(s, "7")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}

		zr := openZip(t, rec)
		want := []string{"01 - Song.flac", "album.m3u", "discs/extra.m3u8"}
		if got := zipNames(zr); !stringSlicesEqual(got, want) {
			t.Fatalf("entry names = %v, want %v", got, want)
		}

		m3uEntry := zipEntry(zr, "album.m3u")
		if m3uEntry == nil {
			t.Fatalf("album.m3u entry missing from zip: %v", zipNames(zr))
		}
		if got := zipEntryBytes(t, m3uEntry); !bytes.Equal(got, m3uBytes) {
			t.Errorf("album.m3u bytes = %q, want %q", got, m3uBytes)
		}

		subEntry := zipEntry(zr, "discs/extra.m3u8")
		if subEntry == nil {
			t.Fatalf("discs/extra.m3u8 entry missing from zip: %v", zipNames(zr))
		}
		if got := zipEntryBytes(t, subEntry); !bytes.Equal(got, subBytes) {
			t.Errorf("discs/extra.m3u8 bytes = %q, want %q", got, subBytes)
		}
	})

	t.Run("no playlist on disk omits the entry and still succeeds", func(t *testing.T) {
		root := t.TempDir()
		albumDir := filepath.Join(root, "Artist", "Album")
		track := writeAlbumFile(t, filepath.Join(albumDir, "01 - Song.flac"), trackBytes)
		// A non-playlist sibling must never be picked up.
		writeAlbumFile(t, filepath.Join(albumDir, "notes.txt"), []byte("not a playlist"))

		s := newAlbumDownloadServer(t, root, newStore(track))
		rec := requestAlbum(s, "7")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}

		zr := openZip(t, rec) // also proves the archive is valid
		if got := zipNames(zr); !stringSlicesEqual(got, []string{"01 - Song.flac"}) {
			t.Fatalf("entry names = %v, want [01 - Song.flac]", got)
		}
		if entry := zipEntry(zr, "notes.txt"); entry != nil {
			t.Errorf("non-playlist file was included in the zip")
		}
	})

	t.Run("library-root track skips the playlist scan entirely", func(t *testing.T) {
		root := t.TempDir()
		// The track sits directly under the library root, so its album dir is
		// the resolved root itself.
		track := writeAlbumFile(t, filepath.Join(root, "01 - Song.flac"), trackBytes)
		// A deep playlist tree elsewhere under the root would otherwise be
		// walked and pulled into this album's zip.
		writeAlbumFile(t, filepath.Join(root, "Artist", "Album", "album.m3u"), []byte("#EXTM3U\nother\n"))
		writeAlbumFile(t, filepath.Join(root, "Artist", "Album", "discs", "extra.m3u8"), []byte("#EXTM3U\nother\n"))

		s := newAlbumDownloadServer(t, root, newStore(track))
		rec := requestAlbum(s, "7")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}

		zr := openZip(t, rec)
		want := []string{"01 - Song.flac"}
		got := zipNames(zr)
		if !stringSlicesEqual(got, want) {
			t.Fatalf("entry names = %v, want %v (library-root album must not walk the library)", got, want)
		}
		for _, n := range got {
			lower := strings.ToLower(n)
			if strings.HasSuffix(lower, ".m3u") || strings.HasSuffix(lower, ".m3u8") {
				t.Errorf("zip contains playlist %q from outside the album dir", n)
			}
		}
	})
}

// TestHandleLibraryAlbumDownloadConcurrency proves the album-zip concurrency
// cap: a saturated semaphore returns 503, and a released slot lets the next
// request stream and return its own slot. It acquires slots through the same
// helpers the handler uses and never sleeps, so it is deterministic.
func TestHandleLibraryAlbumDownloadConcurrency(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	file := writeAlbumFile(t, filepath.Join(albumDir, "01 - First.flac"), []byte("BYTES"))
	store := &stubAlbumStore{
		album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
		artist: &domain.Artist{ID: 3, Name: "Artist"},
		tracks: []domain.Track{
			{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "First", FilePath: file},
		},
	}
	s := newAlbumDownloadServer(t, root, store)

	// Saturate every slot through the same helper the handler uses.
	for i := 0; i < albumZipMaxConcurrent; i++ {
		if !s.acquireAlbumZip() {
			t.Fatalf("acquireAlbumZip() slot %d = false before saturation", i+1)
		}
	}

	// The next request must be rejected without touching the store, and the
	// body must carry the busy error (not an internal one).
	rec := requestAlbum(s, "7")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated status = %d, want 503 (body %q)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, errAlbumZipBusy.Error()) {
		t.Errorf("503 body = %q, want it to carry the busy error", body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("503 response missing Retry-After header")
	}

	// Releasing one slot lets the next request through; a 200 proves the
	// handler acquired a slot and its deferred release ran to completion.
	s.releaseAlbumZip()
	if rec := requestAlbum(s, "7"); rec.Code != http.StatusOK {
		t.Fatalf("after release status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	// The handler's deferred release returned its own slot, so only the
	// manually-held slots remain. Drain them and prove full capacity is
	// available again.
	for i := 0; i < albumZipMaxConcurrent-1; i++ {
		s.releaseAlbumZip()
	}
	for i := 0; i < albumZipMaxConcurrent; i++ {
		if !s.acquireAlbumZip() {
			t.Fatalf("acquireAlbumZip() slot %d = false: handler did not release its slot", i+1)
		}
	}
	for i := 0; i < albumZipMaxConcurrent; i++ {
		s.releaseAlbumZip()
	}
}

// TestAlbumZipSemaphoreLazyInit pins the test-safety contract: a bare Server
// (no NewServer) must not panic on nil and must size the semaphore itself.
func TestAlbumZipSemaphoreLazyInit(t *testing.T) {
	s := &Server{}
	if got := cap(s.albumZipSemaphore()); got != albumZipMaxConcurrent {
		t.Fatalf("cap = %d, want %d", got, albumZipMaxConcurrent)
	}
	for i := 0; i < albumZipMaxConcurrent; i++ {
		if !s.acquireAlbumZip() {
			t.Fatalf("acquireAlbumZip() slot %d = false", i+1)
		}
	}
	if s.acquireAlbumZip() {
		t.Fatalf("acquireAlbumZip() = true past capacity %d", albumZipMaxConcurrent)
	}
}

// TestSafeZipEntryName pins the zip-slip guard: every final entry name is
// normalized so a Windows extractor cannot turn a Linux-legal '\' or a crafted
// ".." into a directory escape, while legitimate nested layout survives.
func TestSafeZipEntryName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain name unchanged", "01 - Song.flac", "01 - Song.flac"},
		{"backslash dotdot neutralized", `..\evil`, "evil"},
		{"dotdot segments dropped", "a/../../b", "a/b"},
		{"leading slash dropped", "/abs", "abs"},
		{"drive letter dropped", `C:\evil`, "evil"},
		{"control chars stripped", "a\x01b\x7fc", "abc"},
		{"c1 control stripped", "a\u0085b", "ab"},
		{"bidi override stripped", "a\u202eb", "ab"},
		{"bidi isolate stripped", "a\u2066b\u2069c", "abc"},
		{"dot segment dropped", "./a/./b", "a/b"},
		{"normal subdir preserved", "discs/extra.m3u8", "discs/extra.m3u8"},
		{"empty falls back", "", "file"},
		{"only separators fall back", "..//../", "file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeZipEntryName(tt.in); got != tt.want {
				t.Errorf("safeZipEntryName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestHandleLibraryAlbumDownloadSanitizesPlaylistEntryNames proves a playlist
// whose on-disk name contains '\' or '..' cannot escape the archive root: the
// entry name is normalized while a real nested layout is preserved.
func TestHandleLibraryAlbumDownloadSanitizesPlaylistEntryNames(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	track := writeAlbumFile(t, filepath.Join(albumDir, "01 - Song.flac"), []byte("TRACK"))

	// Both are legal single-component Linux filenames; both must be cleaned.
	writeAlbumFile(t, filepath.Join(albumDir, `..\evil.m3u`), []byte("#EXTM3U\nevil\n"))
	writeAlbumFile(t, filepath.Join(albumDir, `deep\..\x.m3u8`), []byte("#EXTM3U\nx\n"))

	store := &stubAlbumStore{
		album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
		artist: &domain.Artist{ID: 3, Name: "Artist"},
		tracks: []domain.Track{
			{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Song", FilePath: track},
		},
	}
	s := newAlbumDownloadServer(t, root, store)
	rec := requestAlbum(s, "7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	zr := openZip(t, rec)
	for _, n := range zipNames(zr) {
		if strings.Contains(n, `\`) {
			t.Errorf("zip entry %q contains a backslash", n)
		}
		if n == ".." || strings.HasPrefix(n, "../") {
			t.Errorf("zip entry %q escapes the archive root", n)
		}
	}
	if zipEntry(zr, "evil.m3u") == nil {
		t.Errorf(`..\evil.m3u was not sanitized to evil.m3u: %v`, zipNames(zr))
	}
	if zipEntry(zr, "deep/x.m3u8") == nil {
		t.Errorf(`deep\..\x.m3u8 was not sanitized to deep/x.m3u8: %v`, zipNames(zr))
	}
}

// TestHandleLibraryAlbumDownloadSymlinkedLibraryPath is the Fix 2 regression:
// when library_path is a symlink, the resolved track path and the resolved
// album dir must agree so an album.m3u recorded through the symlink is kept in
// the zip (previously Rel yielded ../… and the playlist guard dropped it).
func TestHandleLibraryAlbumDownloadSymlinkedLibraryPath(t *testing.T) {
	realRoot := t.TempDir()
	albumDir := filepath.Join(realRoot, "Artist", "Album")
	writeAlbumFile(t, filepath.Join(albumDir, "01 - Song.flac"), []byte("TRACK"))
	m3uBytes := []byte("#EXTM3U\n#EXTINF:1,Song\n01 - Song.flac\n")
	writeAlbumFile(t, filepath.Join(albumDir, "album.m3u"), m3uBytes)

	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "library")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// The scanner recorded the path as it walked it: through the symlinked
	// library_path, not the resolved target.
	storedTrack := filepath.Join(link, "Artist", "Album", "01 - Song.flac")
	store := &stubAlbumStore{
		album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
		artist: &domain.Artist{ID: 3, Name: "Artist"},
		tracks: []domain.Track{
			{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Song", FilePath: storedTrack},
		},
	}
	s := newAlbumDownloadServer(t, link, store)
	rec := requestAlbum(s, "7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	zr := openZip(t, rec)
	want := []string{"01 - Song.flac", "album.m3u"}
	if got := zipNames(zr); !stringSlicesEqual(got, want) {
		t.Fatalf("entry names = %v, want %v (playlist dropped for symlinked root?)", got, want)
	}
	entry := zipEntry(zr, "album.m3u")
	if entry == nil {
		t.Fatalf("album.m3u missing from zip: %v", zipNames(zr))
	}
	if got := zipEntryBytes(t, entry); !bytes.Equal(got, m3uBytes) {
		t.Errorf("album.m3u bytes = %q, want %q", got, m3uBytes)
	}
}

// TestHandleLibraryAlbumDownloadSkipsUnreadableTrack proves a single
// unopenable track is skipped, not fatal: the zip still succeeds with the
// remaining entries.
func TestHandleLibraryAlbumDownloadSkipsUnreadableTrack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny open")
	}
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	good := writeAlbumFile(t, filepath.Join(albumDir, "01 - Good.flac"), []byte("GOOD"))
	locked := writeAlbumFile(t, filepath.Join(albumDir, "02 - Locked.flac"), []byte("LOCKED"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

	store := &stubAlbumStore{
		album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
		artist: &domain.Artist{ID: 3, Name: "Artist"},
		tracks: []domain.Track{
			{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Good", FilePath: good},
			{ID: 2, TrackNumber: 2, DiscNumber: 1, Title: "Locked", FilePath: locked},
		},
	}
	s := newAlbumDownloadServer(t, root, store)
	rec := requestAlbum(s, "7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	zr := openZip(t, rec)
	if got := zipNames(zr); !stringSlicesEqual(got, []string{"01 - Good.flac"}) {
		t.Fatalf("entry names = %v, want [01 - Good.flac] (unreadable track must be skipped)", got)
	}
}

// TestHandleLibraryAlbumDownloadSkippedTrackDoesNotReserveName proves a skipped
// unreadable track does not consume its dedupe slot: an unreadable first track
// sharing a title with a later readable one must leave the readable entry as the
// plain name, not a spurious " (2)".
func TestHandleLibraryAlbumDownloadSkippedTrackDoesNotReserveName(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny open")
	}
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	locked := writeAlbumFile(t, filepath.Join(albumDir, "01 - Locked.flac"), []byte("LOCKED"))
	good := writeAlbumFile(t, filepath.Join(albumDir, "02 - Good.flac"), []byte("GOOD"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

	store := &stubAlbumStore{
		album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
		artist: &domain.Artist{ID: 3, Name: "Artist"},
		tracks: []domain.Track{
			{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Same", FilePath: locked},
			{ID: 2, TrackNumber: 1, DiscNumber: 1, Title: "Same", FilePath: good},
		},
	}
	s := newAlbumDownloadServer(t, root, store)
	rec := requestAlbum(s, "7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	zr := openZip(t, rec)
	want := []string{"01 - Same.flac"}
	got := zipNames(zr)
	if !stringSlicesEqual(got, want) {
		t.Fatalf("entry names = %v, want %v (skipped track must not reserve its name)", got, want)
	}
}

// TestHandleLibraryAlbumDownloadHEAD pins the HEAD fast path: ServeMux routes
// HEAD through the GET handler, so the handler must answer from track metadata
// with the download headers and no body instead of building the zip, and must
// return the same 404 as GET when no track can be served.
func TestHandleLibraryAlbumDownloadHEAD(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album")
	track := writeAlbumFile(t, filepath.Join(albumDir, "01 - Song.flac"), []byte("TRACK"))

	t.Run("serveable tracks return 200 with headers and no body", func(t *testing.T) {
		store := &stubAlbumStore{
			album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
			artist: &domain.Artist{ID: 3, Name: "Artist"},
			tracks: []domain.Track{
				{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Song", FilePath: track},
			},
		}
		s := newAlbumDownloadServer(t, root, store)
		rec := requestAlbumMethod(s, "7", http.MethodHead)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Errorf("HEAD body = %q, want empty (zip must not be built)", rec.Body.Bytes())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
			t.Errorf("Content-Type = %q, want application/zip", ct)
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "Artist - Album.zip") {
			t.Errorf("Content-Disposition = %q, want attachment Artist - Album.zip", cd)
		}
		if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", xcto)
		}
	})

	t.Run("no serveable tracks return 404 like GET", func(t *testing.T) {
		store := &stubAlbumStore{
			album:  &domain.Album{ID: 7, ArtistID: 3, Title: "Album"},
			artist: &domain.Artist{ID: 3, Name: "Artist"},
			tracks: []domain.Track{
				{ID: 1, TrackNumber: 1, DiscNumber: 1, Title: "Empty", FilePath: ""},
			},
		}
		s := newAlbumDownloadServer(t, root, store)
		if rec := requestAlbumMethod(s, "7", http.MethodHead); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
		}
	})
}

func TestZipMethodForName(t *testing.T) {
	cases := []struct {
		name string
		want uint16
	}{
		{"01 - Song.flac", zip.Store},
		{"01 - Song.mp3", zip.Store},
		{"cover.jpg", zip.Store},
		{"album.m3u", zip.Deflate},
		{"01 - Song.wav", zip.Deflate},
		{"noext", zip.Deflate},
	}
	for _, tc := range cases {
		if got := zipMethodForName(tc.name); got != tc.want {
			t.Errorf("zipMethodForName(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
}
