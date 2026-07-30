package qbittorrent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testServer creates an httptest server that simulates the qBittorrent WebUI API.
type testServer struct {
	srv      *httptest.Server
	handlers map[string]http.HandlerFunc
}

func newQbitServer(t *testing.T) *testServer {
	t.Helper()
	ts := &testServer{
		handlers: make(map[string]http.HandlerFunc),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if h, ok := ts.handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	ts.srv = httptest.NewServer(mux)
	return ts
}

func (ts *testServer) close() { ts.srv.Close() }

// newPlugin creates a Plugin pointed at the test server.
func (ts *testServer) newPlugin(t *testing.T) *Plugin {
	t.Helper()
	p, err := newPlugin(Config{
		URL:      ts.srv.URL,
		APIKey:   "test-api-key",
		Category: "music",
	}, "", nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	return p
}

func TestCheckConnection(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	var gotAuth bool
	ts.handlers["/api/v2/app/version"] = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") == "Bearer test-api-key" {
			gotAuth = true
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("v5.0.0"))
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	if err := p.CheckConnection(ctx); err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if !p.Connected() {
		t.Error("expected Connected() = true after successful check")
	}
	if !gotAuth {
		t.Error("Authorization: Bearer header was not sent")
	}
}

func TestCheckConnectionUnauthorized(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	ts.handlers["/api/v2/app/version"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	err := p.CheckConnection(ctx)
	if err == nil {
		t.Fatal("expected CheckConnection to fail with 403")
	}
	if !strings.Contains(err.Error(), "unexpected status") {
		t.Errorf("expected 'unexpected status' in error, got: %v", err)
	}
	if p.Connected() {
		t.Error("expected Connected() = false after failed connection check")
	}
}

func TestCheckConnectionNotConfigured(t *testing.T) {
	p, err := newPlugin(Config{URL: "http://localhost:8080"}, "", nil) // no API key
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	ctx := context.Background()
	err = p.CheckConnection(ctx)
	if err == nil {
		t.Fatal("expected CheckConnection to fail when not configured")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected 'not configured' in error, got: %v", err)
	}
}

func TestAddDownload(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	var addCalled bool
	var gotAuth bool

	// Serve a minimal .torrent file.
	ts.handlers["/torrent/test.torrent"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		w.Write([]byte("d8:announce0:13:announce-list0:e"))
	}

	ts.handlers["/api/v2/torrents/add"] = func(w http.ResponseWriter, r *http.Request) {
		addCalled = true
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") == "Bearer test-api-key" {
			gotAuth = true
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Ok."))
	}

	// Return a hash when resolving recent torrents.
	ts.handlers["/api/v2/torrents/info"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]torrentInfo{
			{Hash: "abc123", Name: "test", State: "downloading"},
		})
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	hash, err := p.AddDownload(ctx, ts.srv.URL+"/torrent/test.torrent", "", "/downloads/music")
	if err != nil {
		t.Fatalf("AddDownload: %v", err)
	}
	if hash != "abc123" {
		t.Errorf("expected resolved hash, got: %s", hash)
	}
	if !addCalled {
		t.Error("torrents/add was not called")
	}
	if !gotAuth {
		t.Error("Authorization: Bearer header was not sent on torrents/add")
	}
}

func TestGetStatus(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	ts.handlers["/api/v2/torrents/info"] = func(w http.ResponseWriter, r *http.Request) {
		hash := r.URL.Query().Get("hashes")
		if hash == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		info := torrentInfo{
			Hash:      hash,
			Name:      "Test Album",
			State:     "downloading",
			Progress:  0.45,
			Size:      350_000_000,
			TotalSize: 350_000_000,
			SavePath:  "/downloads/Test Album",
			ContentPath: "/downloads/Test Album",
			Category:  "music",
			DlSpeed:   1_048_576,
			AmountLeft: 192_500_000,
			CompletionOn: 0,
		}
		json.NewEncoder(w).Encode([]torrentInfo{info})
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	rec, err := p.GetStatus(ctx, "abc123")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if rec == nil {
		t.Fatal("GetStatus returned nil record")
	}
	if rec.State != "downloading" {
		t.Errorf("State = %s, want downloading", rec.State)
	}
	if rec.Progress != 45.0 {
		t.Errorf("Progress = %f, want 45.0", rec.Progress)
	}
	if rec.Size != 350_000_000 {
		t.Errorf("Size = %d, want 350000000", rec.Size)
	}
}

func TestGetStatusCompleted(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	ts.handlers["/api/v2/torrents/info"] = func(w http.ResponseWriter, r *http.Request) {
		info := torrentInfo{
			Hash:      "complete123",
			Name:      "Test Album",
			State:     "uploading",
			Progress:  1.0,
			Size:      350_000_000,
			TotalSize: 350_000_000,
			SavePath:  "/downloads/Test Album",
			ContentPath: "/downloads/Test Album/content",
			AmountLeft: 0,
			CompletionOn: 1712345678,
		}
		json.NewEncoder(w).Encode([]torrentInfo{info})
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	rec, err := p.GetStatus(ctx, "complete123")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if rec.State != "importPending" {
		t.Errorf("State = %s, want importPending", rec.State)
	}
	if rec.FilePath != "/downloads/Test Album/content" {
		t.Errorf("FilePath = %s, want /downloads/Test Album/content", rec.FilePath)
	}
}

func TestGetStatusNotFound(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	ts.handlers["/api/v2/torrents/info"] = func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]torrentInfo{}) // empty — magnet resolving or not found
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	rec, err := p.GetStatus(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetStatus should not error for resolving/not-found: %v", err)
	}
	if rec != nil {
		t.Error("expected nil record for resolving/not-found torrent")
	}
}

func TestCancel(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	var deleteCalled bool
	var deleteFiles string

	ts.handlers["/api/v2/torrents/delete"] = func(w http.ResponseWriter, r *http.Request) {
		deleteCalled = true
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deleteFiles = r.FormValue("deleteFiles")
		if deleteFiles != "true" {
			t.Errorf("expected deleteFiles=true, got %s", deleteFiles)
		}
		w.WriteHeader(http.StatusOK)
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	if err := p.Cancel(ctx, "abc123", true); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !deleteCalled {
		t.Error("torrents/delete was not called")
	}
}

func TestGetProgress(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	ts.handlers["/api/v2/torrents/info"] = func(w http.ResponseWriter, r *http.Request) {
		info := torrentInfo{
			Hash:       "testhash",
			Name:       "Test",
			State:      "downloading",
			TotalSize:  350_000_000,
			DlSpeed:    2_097_152,
			AmountLeft: 100_000_000,
		}
		json.NewEncoder(w).Encode([]torrentInfo{info})
	}

	p := ts.newPlugin(t)
	ctx := context.Background()

	prog, err := p.GetProgress(ctx, "testhash")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if prog == nil {
		t.Fatal("GetProgress returned nil")
	}
	if prog.Total != 350_000_000 {
		t.Errorf("Total = %d, want 350000000", prog.Total)
	}
	if prog.Transferred != 250_000_000 {
		t.Errorf("Transferred = %d, want 250000000 (total - amountLeft)", prog.Transferred)
	}
	if prog.Speed != 2_097_152 {
		t.Errorf("Speed = %d, want 2097152", prog.Speed)
	}
}

// TestAddDownloadTorrentFetchError tests that a failed torrent URL fetch
// propagates the error back to the caller.
func TestAddDownloadTorrentFetchError(t *testing.T) {
	ts := newQbitServer(t)
	defer ts.close()

	// No /torrent handler registered — returns 404.
	p := ts.newPlugin(t)
	ctx := context.Background()

	_, err := p.AddDownload(ctx, ts.srv.URL+"/torrent/nonexistent.torrent", "", "")
	if err == nil {
		t.Fatal("expected error fetching missing torrent file")
	}
	if !strings.Contains(err.Error(), "download torrent file") {
		t.Errorf("expected 'download torrent file' in error, got: %v", err)
	}
}

func TestDownloadTimeout(t *testing.T) {
	p, err := newPlugin(Config{
		URL:    "http://localhost:8080",
		APIKey: "test-api-key",
	}, "", nil)
	if err != nil {
		t.Fatalf("newPlugin: %v", err)
	}
	if timeout := p.DownloadTimeout(); timeout != 0 {
		// 2 hours
		if timeout.Hours() != 2 {
			t.Errorf("DownloadTimeout = %v, want 2h", timeout)
		}
	}
}

func TestMapToRecord(t *testing.T) {
	p, _ := newPlugin(Config{URL: "http://localhost:8080", APIKey: "test-api-key"}, "", nil)

	tests := []struct {
		name      string
		info      *torrentInfo
		wantState string
		wantPath  string
	}{
		{
			name: "downloading",
			info: &torrentInfo{
				Hash: "h1", Name: "Album", State: "downloading", Progress: 0.5,
				TotalSize: 100, AmountLeft: 50, ContentPath: "/dl/Album",
			},
			wantState: "downloading",
			wantPath:  "/dl/Album",
		},
		{
			name: "completed",
			info: &torrentInfo{
				Hash: "h2", Name: "Album2", State: "uploading", Progress: 1.0,
				TotalSize: 100, AmountLeft: 0, CompletionOn: 12345, ContentPath: "/dl/Album2",
			},
			wantState: "importPending",
			wantPath:  "/dl/Album2",
		},
		{
			name: "no content path",
			info: &torrentInfo{
				Hash: "h3", Name: "Album3", State: "downloading", Progress: 0.1,
				TotalSize: 100, AmountLeft: 90, SavePath: "/dl/save",
			},
			wantState: "downloading",
			wantPath:  "/dl/save/Album3",
		},
		{
			name: "error state",
			info: &torrentInfo{
				Hash: "h4", Name: "Broken", State: "error", TotalSize: 0,
			},
			wantState: "failed",
			wantPath:  "/Broken",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := p.mapToRecord(tt.info)
			if string(rec.State) != tt.wantState {
				t.Errorf("State = %s, want %s", rec.State, tt.wantState)
			}
			if rec.FilePath != tt.wantPath {
				t.Errorf("FilePath = %s, want %s", rec.FilePath, tt.wantPath)
			}
		})
	}
}
