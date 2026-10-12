package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ramonskie/groovearr/internal/catalog"
	"github.com/ramonskie/groovearr/internal/domain"
)

func TestScanAudioFiles_Recursive(t *testing.T) {
	tmp := t.TempDir()

	// Flat files in root.
	createFile(t, filepath.Join(tmp, "01 Intro.flac"))
	createFile(t, filepath.Join(tmp, "02 Song.mp3"))
	createFile(t, filepath.Join(tmp, "cover.jpg")) // non-audio, should be skipped

	// Multi-disc subdirectories.
	cd1 := filepath.Join(tmp, "CD1")
	cd2 := filepath.Join(tmp, "CD2")
	mustMkdir(t, cd1)
	mustMkdir(t, cd2)
	createFile(t, filepath.Join(cd1, "01 Track One.flac"))
	createFile(t, filepath.Join(cd1, "02 Track Two.opus"))
	createFile(t, filepath.Join(cd2, "01 Live One.mp3"))
	createFile(t, filepath.Join(cd2, "02 Live Two.wav"))

	// Nested subdirectory.
	bonus := filepath.Join(tmp, "Bonus")
	mustMkdir(t, bonus)
	createFile(t, filepath.Join(bonus, "Bonus Track.aac"))

	h := &AlbumImportHandler{}
	got, err := h.scanAudioFiles(tmp)
	if err != nil {
		t.Fatalf("scanAudioFiles: %v", err)
	}

	want := []string{
		filepath.Join(tmp, "01 Intro.flac"),
		filepath.Join(tmp, "02 Song.mp3"),
		filepath.Join(bonus, "Bonus Track.aac"),
		filepath.Join(cd1, "01 Track One.flac"),
		filepath.Join(cd1, "02 Track Two.opus"),
		filepath.Join(cd2, "01 Live One.mp3"),
		filepath.Join(cd2, "02 Live Two.wav"),
	}
	sort.Strings(got)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("count mismatch: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("file[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScanAudioFiles_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	h := &AlbumImportHandler{}
	got, err := h.scanAudioFiles(tmp)
	if err != nil {
		t.Fatalf("scanAudioFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 files in empty dir, got %d: %v", len(got), got)
	}
}

func TestScanAudioFiles_NoAudioFiles(t *testing.T) {
	tmp := t.TempDir()
	createFile(t, filepath.Join(tmp, "cover.jpg"))
	createFile(t, filepath.Join(tmp, "info.txt"))
	createFile(t, filepath.Join(tmp, "folder.png"))

	h := &AlbumImportHandler{}
	got, err := h.scanAudioFiles(tmp)
	if err != nil {
		t.Fatalf("scanAudioFiles: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 audio files, got %d: %v", len(got), got)
	}
}

func TestScanAudioFiles_NonexistentDir(t *testing.T) {
	h := &AlbumImportHandler{}
	_, err := h.scanAudioFiles("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

// TestAlbumImport_LinksOnceForWholeAlbum proves the album-level hook replaced
// the per-track link: an album with N matched files calls LinkImportedAlbum
// exactly once, not N times.
func TestAlbumImport_LinksOnceForWholeAlbum(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"01 - One.flac", "02 - Two.flac", "03 - Three.flac"} {
		createFile(t, filepath.Join(tmp, name))
	}

	resolver := func(context.Context, string, string, string, int, string) ([]domain.ExpectedTrack, string, error) {
		return []domain.ExpectedTrack{
			{TrackNumber: 1, Title: "One"},
			{TrackNumber: 2, Title: "Two"},
			{TrackNumber: 3, Title: "Three"},
		}, "mbid-1", nil
	}
	linker := &fakeTrackingLinker{}
	chain := []ImportHandler{NewTrackingLinkHandler(linker, testLogger())}
	h := NewAlbumImportHandler(chain, resolver, newMockAlbumStore(), nil, testLogger())

	record := &Record{
		ID: "album-link-once", SourceName: "prowlarr", State: StateImporting,
		Artist: "Tool", Album: "Lateralus", AlbumType: "album", FilePath: tmp,
	}
	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if linker.calls != 1 {
		t.Fatalf("LinkImportedAlbum calls = %d, want exactly 1 for the whole album", linker.calls)
	}
	if linker.album != "Lateralus" {
		t.Fatalf("linked album = %q, want Lateralus", linker.album)
	}
}

// TestAlbumImport_HookErrorDoesNotFailImport proves the album hook is
// best-effort: a linking failure is swallowed and the import still succeeds.
func TestAlbumImport_HookErrorDoesNotFailImport(t *testing.T) {
	tmp := t.TempDir()
	createFile(t, filepath.Join(tmp, "01 - One.flac"))

	resolver := func(context.Context, string, string, string, int, string) ([]domain.ExpectedTrack, string, error) {
		return []domain.ExpectedTrack{{TrackNumber: 1, Title: "One"}}, "", nil
	}
	linker := &fakeTrackingLinker{err: errors.New("tracking store down")}
	chain := []ImportHandler{NewTrackingLinkHandler(linker, testLogger())}
	h := NewAlbumImportHandler(chain, resolver, newMockAlbumStore(), nil, testLogger())

	record := &Record{
		ID: "album-hook-err", SourceName: "prowlarr", State: StateImporting,
		Artist: "Tool", Album: "Lateralus", AlbumType: "album", FilePath: tmp,
	}
	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle must swallow album hook error, got: %v", err)
	}
	if linker.calls != 1 {
		t.Fatalf("LinkImportedAlbum calls = %d, want 1", linker.calls)
	}
}

// captureRecordHandler is a chain handler that records every record it sees.
// It lets a test assert what the album importer passed down to the per-track
// chain without depending on the real library store.
type captureRecordHandler struct {
	records []*Record
}

func (c *captureRecordHandler) Handle(_ context.Context, r *Record) error {
	c.records = append(c.records, r)
	return nil
}

// TestAlbumImport_PropagatesRequesterToSyntheticRecords proves an album
// download's requester is copied onto each synthetic per-track record, so the
// per-track LibraryImporterHandler stamps attribution on every imported row.
func TestAlbumImport_PropagatesRequesterToSyntheticRecords(t *testing.T) {
	tmp := t.TempDir()
	createFile(t, filepath.Join(tmp, "01 - One.flac"))
	createFile(t, filepath.Join(tmp, "02 - Two.flac"))

	resolver := func(context.Context, string, string, string, int, string) ([]domain.ExpectedTrack, string, error) {
		return []domain.ExpectedTrack{
			{TrackNumber: 1, Title: "One"},
			{TrackNumber: 2, Title: "Two"},
		}, "", nil
	}
	capturer := &captureRecordHandler{}
	h := NewAlbumImportHandler([]ImportHandler{capturer}, resolver, newMockAlbumStore(), nil, testLogger())

	record := &Record{
		ID: "album-attr", SourceName: "prowlarr", State: StateImporting,
		Artist: "Tool", Album: "Lateralus", AlbumType: "album", FilePath: tmp,
		RequestedByUserID: 42, RequestedByUsername: "alice",
	}
	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(capturer.records) != 2 {
		t.Fatalf("chain saw %d records, want 2", len(capturer.records))
	}
	for i, r := range capturer.records {
		if r.RequestedByUserID != 42 || r.RequestedByUsername != "alice" {
			t.Errorf("synth record %d attribution = (%d, %q), want (42, alice)",
				i, r.RequestedByUserID, r.RequestedByUsername)
		}
	}
}

// importingChainHandler simulates the library importer: it stamps a library
// track id on each synthetic record so AlbumImportHandler links the tracks and
// can resolve the album id to enqueue.
type importingChainHandler struct {
	trackIDs []int64
	next     int
}

func (c *importingChainHandler) Handle(_ context.Context, r *Record) error {
	if c.next < len(c.trackIDs) {
		r.LibraryTrackID = c.trackIDs[c.next]
	}
	c.next++
	return nil
}

// stubLibraryStore is a minimal libraryStore backed by an in-memory map.
// "Not found" returns (nil, nil) to match the store contract.
type stubLibraryStore struct {
	tracks map[int64]*domain.Track
}

func (s *stubLibraryStore) GetTrack(_ context.Context, id int64) (*domain.Track, error) {
	return s.tracks[id], nil
}

// stubEnqueuer records EnqueueAlbum calls so a test can assert the import
// requested a background metadata refresh.
type stubEnqueuer struct {
	calls []enqueueCall
	err   error
}

type enqueueCall struct {
	albumID  int64
	reason   string
	priority int
}

func (e *stubEnqueuer) EnqueueAlbum(_ context.Context, albumID int64, reason string, priority int) error {
	e.calls = append(e.calls, enqueueCall{albumID: albumID, reason: reason, priority: priority})
	return e.err
}

// albumEnqueueFixture builds a one-track album import backed by a stub library
// store that resolves track 7 to album 55.
func albumEnqueueFixture(t *testing.T, id string, enq Enqueuer) (*AlbumImportHandler, *Record) {
	t.Helper()
	tmp := t.TempDir()
	createFile(t, filepath.Join(tmp, "01 - One.flac"))

	resolver := func(context.Context, string, string, string, int, string) ([]domain.ExpectedTrack, string, error) {
		return []domain.ExpectedTrack{{TrackNumber: 1, Title: "One"}}, "", nil
	}
	libStore := &stubLibraryStore{tracks: map[int64]*domain.Track{
		7: {ID: 7, AlbumID: 55},
	}}
	chain := []ImportHandler{&importingChainHandler{trackIDs: []int64{7}}}
	h := NewAlbumImportHandler(chain, resolver, newMockAlbumStore(), libStore, testLogger())
	if enq != nil {
		h.SetEnqueuer(enq)
	}
	record := &Record{
		ID: id, SourceName: "prowlarr", State: StateImporting,
		Artist: "Tool", Album: "Lateralus", AlbumType: "album", FilePath: tmp,
	}
	return h, record
}

// TestAlbumImport_EnqueuesHighPriorityRefresh proves a successful album import
// asks the refresh engine to materialize the album with an event reason and
// high priority, once, for the imported album's id.
func TestAlbumImport_EnqueuesHighPriorityRefresh(t *testing.T) {
	enq := &stubEnqueuer{}
	h, record := albumEnqueueFixture(t, "album-enqueue", enq)

	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(enq.calls) != 1 {
		t.Fatalf("EnqueueAlbum calls = %d, want exactly 1", len(enq.calls))
	}
	got := enq.calls[0]
	if got.albumID != 55 || got.reason != catalog.ReasonEvent || got.priority != catalog.PriorityHigh {
		t.Fatalf("enqueue = %+v, want album 55 reason %q priority %d",
			got, catalog.ReasonEvent, catalog.PriorityHigh)
	}
}

// TestAlbumImport_NilEnqueuerDoesNotPanic proves enqueueing is optional: an
// import with no enqueuer wired still succeeds.
func TestAlbumImport_NilEnqueuerDoesNotPanic(t *testing.T) {
	h, record := albumEnqueueFixture(t, "album-no-enqueuer", nil)

	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle with nil enqueuer: %v", err)
	}
}

// TestAlbumImport_EnqueueErrorDoesNotFailImport proves the enqueue is
// best-effort: a queue write failure is swallowed and the import still succeeds.
func TestAlbumImport_EnqueueErrorDoesNotFailImport(t *testing.T) {
	enq := &stubEnqueuer{err: errors.New("queue write failed")}
	h, record := albumEnqueueFixture(t, "album-enqueue-err", enq)

	if err := h.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle must swallow enqueue error, got: %v", err)
	}
	if len(enq.calls) != 1 {
		t.Fatalf("EnqueueAlbum calls = %d, want 1", len(enq.calls))
	}
}

func createFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("dummy"), 0644); err != nil {
		t.Fatalf("createFile(%q): %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir(%q): %v", path, err)
	}
}
