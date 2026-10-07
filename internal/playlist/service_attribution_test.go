package playlist

import (
	"context"
	"database/sql"
	"testing"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/download"
	dlsqlite "github.com/ramonskie/groovearr/internal/download/sqlite"
	"github.com/ramonskie/groovearr/internal/events"
	libsqlite "github.com/ramonskie/groovearr/internal/library/sqlite"
)

// readPlaylistAttribution reads the raw added_by_* values for a playlist id.
func readPlaylistAttribution(t *testing.T, db *sql.DB, id int64) (sql.NullInt64, string) {
	t.Helper()
	var uid sql.NullInt64
	var name string
	if err := db.QueryRow(`SELECT added_by_user_id, added_by_username FROM playlists WHERE id=?`, id).
		Scan(&uid, &name); err != nil {
		t.Fatalf("read playlist attribution: %v", err)
	}
	return uid, name
}

// TestImportPlaylistAttributionCreateOnly verifies the importer is stamped on
// create and that a later SyncPlaylist (and a re-import) never overwrites it —
// attribution is insert-only, system paths stay blank.
func TestImportPlaylistAttributionCreateOnly(t *testing.T) {
	ctx := context.Background()

	libStore, err := libsqlite.New(t.TempDir()+"/test.db", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer libStore.Close()

	dlStore := dlsqlite.New(libStore.DB(), discardLogger())
	bus := events.NewInMemoryEventBus(discardLogger())
	downloadSvc := download.NewService(dlStore, bus, discardLogger())

	reg := NewRegistry()
	if err := reg.Register(&mockSource{
		name: "mock", display: "Mock", configured: true,
		playlists: []PlaylistInfo{{SourceID: "pl-1", Name: "Mix"}},
		tracks:    map[string][]TrackInfo{},
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(
		reg,
		libStore,
		download.NewRegistry(),
		downloadSvc,
		func() config.Config { return config.Config{} },
		nil,
		nil,
		discardLogger(),
	)

	// Import as alice.
	result, err := svc.ImportPlaylist(ctx, "mock", "pl-1", "mirror", 42, "alice")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	uid, name := readPlaylistAttribution(t, libStore.DB(), result.Playlist.ID)
	if !uid.Valid || uid.Int64 != 42 || name != "alice" {
		t.Fatalf("attribution after import = (%v, %q), want (42, alice)", uid, name)
	}

	// A later sync must not touch attribution (UPDATE path excludes added_by_*).
	if err := svc.SyncPlaylist(ctx, result.Playlist.ID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	uid, name = readPlaylistAttribution(t, libStore.DB(), result.Playlist.ID)
	if !uid.Valid || uid.Int64 != 42 || name != "alice" {
		t.Errorf("attribution after sync = (%v, %q), want (42, alice)", uid, name)
	}

	// A re-import by a different actor must also preserve the first importer.
	if _, err := svc.ImportPlaylist(ctx, "mock", "pl-1", "mirror", 99, "bob"); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	uid, name = readPlaylistAttribution(t, libStore.DB(), result.Playlist.ID)
	if !uid.Valid || uid.Int64 != 42 || name != "alice" {
		t.Errorf("attribution after re-import = (%v, %q), want (42, alice) insert-only", uid, name)
	}
}

// TestImportPlaylistSystemAttributionBlank verifies a background/system import
// (0/"") leaves the playlist attribution NULL/blank rather than a 0 user id.
func TestImportPlaylistSystemAttributionBlank(t *testing.T) {
	ctx := context.Background()

	libStore, err := libsqlite.New(t.TempDir()+"/test.db", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer libStore.Close()

	dlStore := dlsqlite.New(libStore.DB(), discardLogger())
	downloadSvc := download.NewService(dlStore, events.NewInMemoryEventBus(discardLogger()), discardLogger())

	reg := NewRegistry()
	if err := reg.Register(&mockSource{
		name: "mock", display: "Mock", configured: true,
		playlists: []PlaylistInfo{{SourceID: "pl-2", Name: "System"}},
		tracks:    map[string][]TrackInfo{},
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(reg, libStore, download.NewRegistry(), downloadSvc,
		func() config.Config { return config.Config{} }, nil, nil, discardLogger())

	result, err := svc.ImportPlaylist(ctx, "mock", "pl-2", "mirror", 0, "")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	uid, name := readPlaylistAttribution(t, libStore.DB(), result.Playlist.ID)
	if uid.Valid || name != "" {
		t.Errorf("system attribution = (%v, %q), want (NULL, \"\")", uid, name)
	}
}
