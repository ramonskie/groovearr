package download

import (
	"context"
	"errors"
	"testing"
)

// fakeTrackingLinker records LinkImportedAlbum calls for assertions.
type fakeTrackingLinker struct {
	calls  int
	artist string
	album  string
	err    error
}

func (f *fakeTrackingLinker) LinkImportedAlbum(ctx context.Context, artistName, albumTitle string) error {
	f.calls++
	f.artist = artistName
	f.album = albumTitle
	return f.err
}

func TestTrackingLinkHandler_PromotesMatchingAlbum(t *testing.T) {
	linker := &fakeTrackingLinker{}
	handler := NewTrackingLinkHandler(linker, testLogger())

	record := &Record{
		ID:     "test-tracking-1",
		Artist: "Tool",
		Album:  "Lateralus",
	}

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if linker.calls != 1 {
		t.Fatalf("LinkImportedAlbum calls = %d, want 1", linker.calls)
	}
	if linker.artist != "Tool" || linker.album != "Lateralus" {
		t.Fatalf("LinkImportedAlbum(%q, %q), want (Tool, Lateralus)", linker.artist, linker.album)
	}
}

func TestTrackingLinkHandler_SkipsMissingMetadata(t *testing.T) {
	tests := []struct {
		name   string
		record *Record
	}{
		{name: "missing artist", record: &Record{ID: "t", Album: "Lateralus"}},
		{name: "missing album", record: &Record{ID: "t", Artist: "Tool"}},
		{name: "missing both", record: &Record{ID: "t"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			linker := &fakeTrackingLinker{}
			handler := NewTrackingLinkHandler(linker, testLogger())

			if err := handler.Handle(context.Background(), tt.record); err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}
			if linker.calls != 0 {
				t.Fatalf("LinkImportedAlbum calls = %d, want 0", linker.calls)
			}
		})
	}
}

func TestTrackingLinkHandler_NilLinkerIsNoop(t *testing.T) {
	handler := NewTrackingLinkHandler(nil, testLogger())

	record := &Record{ID: "test-tracking-nil", Artist: "Tool", Album: "Lateralus"}

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle with nil linker returned error: %v", err)
	}
}

func TestTrackingLinkHandler_BestEffortOnLinkerError(t *testing.T) {
	linker := &fakeTrackingLinker{err: errors.New("tracking store down")}
	handler := NewTrackingLinkHandler(linker, testLogger())

	record := &Record{ID: "test-tracking-err", Artist: "Tool", Album: "Lateralus"}

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle must swallow linker error (best-effort), got: %v", err)
	}
	if linker.calls != 1 {
		t.Fatalf("LinkImportedAlbum calls = %d, want 1", linker.calls)
	}
}

// Album imports feed synthetic per-track records through the standard chain,
// so the handler must also promote when the record is an album record.
func TestTrackingLinkHandler_PromotesAlbumRecord(t *testing.T) {
	linker := &fakeTrackingLinker{}
	handler := NewTrackingLinkHandler(linker, testLogger())

	record := &Record{
		ID:        "test-tracking-album",
		Artist:    "A Perfect Circle",
		Album:     "Mer de Noms",
		AlbumType: "album",
	}
	if !record.IsAlbum() {
		t.Fatalf("test record should be an album record")
	}

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if linker.calls != 1 || linker.album != "Mer de Noms" {
		t.Fatalf("LinkImportedAlbum calls=%d album=%q, want 1 / Mer de Noms", linker.calls, linker.album)
	}
}
