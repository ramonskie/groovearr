package tidal

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/binozo/go-tiddl"
	"github.com/ramonskie/groovearr/internal/download"
)

// newTestClient builds a tidal Client with an isolated download dir.
// The tiddl client is constructed offline — no network calls.
func newTestClient(t *testing.T, cfg TidalConfig) *Client {
	t.Helper()
	c, err := NewClient(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	t.Cleanup(func() { c.tiddlClient.Close() })
	return c
}

func testCfg() TidalConfig {
	return TidalConfig{
		AccessToken:  "test-token",
		RefreshToken: "test-refresh",
		CountryCode:  "NL",
		Quality:      "LOSSLESS",
	}
}

// ─── StartDownload ──────────────────────────────────────────────────────

func TestStartDownloadUnconfigured(t *testing.T) {
	c := newTestClient(t, TidalConfig{})
	_, err := c.StartDownload(context.Background(), download.Meta{TrackID: "123", Artist: "Mock Artist", Title: "Mock Title"})
	if err == nil {
		t.Fatal("expected error for unconfigured client")
	}
	if !strings.Contains(err.Error(), "access token") {
		t.Errorf("err = %q, want access token error", err)
	}
}

func TestStartDownloadMissingTrackID(t *testing.T) {
	c := newTestClient(t, testCfg())
	_, err := c.StartDownload(context.Background(), download.Meta{Artist: "Mock Artist", Title: "Mock Title"})
	if err == nil {
		t.Fatal("expected error when no track ID and no filename")
	}
	if !strings.Contains(err.Error(), "track ID") {
		t.Errorf("err = %q, want track ID error", err)
	}
}

func TestStartDownloadParsesTrackIDFromFilename(t *testing.T) {
	c := newTestClient(t, testCfg())
	// TrackID comes from the "N||artist - title" filename convention. Use a
	// non-numeric id so the async goroutine fails fast (no network); the point
	// here is the filename→trackID parsing, not a real download.
	_, err := c.StartDownload(context.Background(), download.Meta{
		Artist:   "Mock Artist",
		Title:    "Mock Title",
		Filename: "notanumber||Mock Artist - Mock Title",
	})
	if err != nil {
		t.Fatalf("StartDownload failed: %v", err)
	}
}

func TestStartDownloadReturnsWellFormedIDAndQueued(t *testing.T) {
	c := newTestClient(t, testCfg())
	id, err := c.StartDownload(context.Background(), download.Meta{
		TrackID: "not-a-number", // fails fast in the goroutine, no network
		Artist:  "Mock Artist",
		Title:   "Mock Title",
	})
	if err != nil {
		t.Fatalf("StartDownload failed: %v", err)
	}
	if !strings.HasPrefix(id, "tidal-not-a-number-") {
		t.Errorf("download ID = %q, want tidal-<id>-<ts> prefix", id)
	}

	rec, err := c.GetStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if rec.State != download.StateQueued {
		t.Errorf("state = %q, want queued", rec.State)
	}
	if rec.SourceName != pluginName || rec.TrackID != "not-a-number" {
		t.Errorf("record = %+v", rec)
	}

	// The goroutine fails synchronously at ParseUint — no network involved.
	waitForState(t, c, id, download.StateFailed, 2*time.Second)
	rec, _ = c.GetStatus(context.Background(), id)
	if !strings.Contains(rec.Error, "invalid track ID") {
		t.Errorf("error = %q, want invalid track ID", rec.Error)
	}
}

func waitForState(t *testing.T, c *Client, id string, want download.State, timeout time.Duration) *download.Record {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rec, err := c.GetStatus(context.Background(), id)
		if err == nil && rec.State == want {
			return rec
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec, err := c.GetStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("GetStatus(%s) failed: %v", id, err)
	}
	t.Fatalf("state = %q, want %q (error=%q)", rec.State, want, rec.Error)
	return nil
}

// ─── GetStatus / GetProgress / Cancel ───────────────────────────────────

func TestGetStatusUnknown(t *testing.T) {
	c := newTestClient(t, testCfg())
	if _, err := c.GetStatus(context.Background(), "tidal-nope"); err == nil {
		t.Fatal("expected error for unknown download")
	}
}

func TestCancelUnknown(t *testing.T) {
	c := newTestClient(t, testCfg())
	if err := c.Cancel(context.Background(), "tidal-nope", false); err == nil {
		t.Fatal("expected error for unknown download")
	}
}

func TestCancelMarksIgnoredAndCleansCancelFunc(t *testing.T) {
	c := newTestClient(t, testCfg())
	id := "tidal-1-12345"
	c.downloadsMu.Lock()
	c.downloads[id] = &download.Record{ID: id, State: download.StateDownloading}
	c.downloadsMu.Unlock()
	cancel := func() {}
	c.cancelMu.Lock()
	c.cancelFuncs[id] = cancel
	c.cancelMu.Unlock()

	if err := c.Cancel(context.Background(), id, false); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	rec, err := c.GetStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if rec.State != download.StateIgnored {
		t.Errorf("state = %q, want ignored", rec.State)
	}

	c.cancelMu.Lock()
	_, stillThere := c.cancelFuncs[id]
	c.cancelMu.Unlock()
	if stillThere {
		t.Error("cancel func not cleaned up")
	}
}

func TestCancelWithRemoveDeletesRecord(t *testing.T) {
	c := newTestClient(t, testCfg())
	id := "tidal-2-67890"
	c.downloadsMu.Lock()
	c.downloads[id] = &download.Record{ID: id, State: download.StateDownloading}
	c.downloadsMu.Unlock()
	c.cancelMu.Lock()
	c.cancelFuncs[id] = func() {}
	c.cancelMu.Unlock()

	if err := c.Cancel(context.Background(), id, true); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	if _, err := c.GetStatus(context.Background(), id); err == nil {
		t.Error("record still present after Cancel(remove=true)")
	}
}

func TestGetProgressMirrorsRecord(t *testing.T) {
	c := newTestClient(t, testCfg())
	id := "tidal-3-111"
	c.downloadsMu.Lock()
	c.downloads[id] = &download.Record{
		ID: id, State: download.StateDownloading,
		Transferred: 1000, Size: 5000, Speed: 250,
	}
	c.downloadsMu.Unlock()

	prog, err := c.GetProgress(context.Background(), id)
	if err != nil {
		t.Fatalf("GetProgress failed: %v", err)
	}
	if prog.Transferred != 1000 || prog.Total != 5000 || prog.Speed != 250 {
		t.Errorf("progress = %+v", prog)
	}
}

func TestActiveDownloads(t *testing.T) {
	c := newTestClient(t, testCfg())
	for _, id := range []string{"tidal-a", "tidal-b"} {
		c.downloadsMu.Lock()
		c.downloads[id] = &download.Record{ID: id}
		c.downloadsMu.Unlock()
	}
	ids := c.ActiveDownloads()
	if len(ids) != 2 {
		t.Errorf("ActiveDownloads = %v, want 2 entries", ids)
	}
}

func TestConcurrencyConstants(t *testing.T) {
	c := newTestClient(t, testCfg())
	if c.MaxConcurrent() != 2 {
		t.Errorf("MaxConcurrent = %d, want 2", c.MaxConcurrent())
	}
	if c.DownloadTimeout() != 10*time.Minute {
		t.Errorf("DownloadTimeout = %v, want 10m", c.DownloadTimeout())
	}
}

// ─── writeStream ────────────────────────────────────────────────────────

func TestWriteStreamWritesFileAndTracksProgress(t *testing.T) {
	c := newTestClient(t, testCfg())
	id := "tidal-4-222"
	c.downloadsMu.Lock()
	c.downloads[id] = &download.Record{ID: id, State: download.StateQueued}
	c.downloadsMu.Unlock()

	data := bytes.Repeat([]byte("x"), 1024*512)
	outPath := filepath.Join(t.TempDir(), "out.flac")
	if err := c.writeStream(context.Background(), id, bytes.NewReader(data), outPath); err != nil {
		t.Fatalf("writeStream failed: %v", err)
	}

	rec, _ := c.GetStatus(context.Background(), id)
	if rec.State != download.StateDownloading {
		t.Errorf("state = %q, want downloading", rec.State)
	}
	if rec.Transferred != int64(len(data)) {
		t.Errorf("transferred = %d, want %d", rec.Transferred, len(data))
	}
	if rec.Speed <= 0 {
		t.Errorf("speed = %d, want > 0", rec.Speed)
	}

	fi, err := filepath.Glob(outPath)
	if err != nil || len(fi) != 1 {
		t.Fatalf("file not written: %v", fi)
	}
}

func TestWriteStreamCancelledRemovesFile(t *testing.T) {
	c := newTestClient(t, testCfg())
	id := "tidal-5-333"
	c.downloadsMu.Lock()
	c.downloads[id] = &download.Record{ID: id}
	c.downloadsMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	outPath := filepath.Join(t.TempDir(), "out.flac")
	if err := c.writeStream(ctx, id, bytes.NewReader([]byte("data")), outPath); err == nil {
		t.Fatal("expected context cancelled error")
	}
	if fi, err := filepath.Glob(outPath); err == nil && len(fi) != 0 {
		t.Errorf("file not removed after cancel: %v", fi)
	}
}

// ─── Quality selection ──────────────────────────────────────────────────

func TestCfgQualityToTiddl(t *testing.T) {
	tests := []struct {
		in   string
		want tiddl.AudioQuality
	}{
		{"LOSSLESS", tiddl.Lossless},
		{"HIGH", tiddl.High},
		{"LOW", tiddl.Low},
		{"", tiddl.Lossless},
		{"WEIRD", tiddl.Lossless},
	}
	for _, tt := range tests {
		if got := cfgQualityToTiddl(tt.in); got != tt.want {
			t.Errorf("cfgQualityToTiddl(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSelectQuality(t *testing.T) {
	// Desired quality available → keep desired.
	if got := selectQuality(tiddl.Lossless, tiddl.HiResLossless); got != tiddl.Lossless {
		t.Errorf("selectQuality(Lossless, HiRes) = %v, want Lossless", got)
	}
	// Desired quality unavailable → fall back to available.
	if got := selectQuality(tiddl.Lossless, tiddl.High); got != tiddl.High {
		t.Errorf("selectQuality(Lossless, High) = %v, want High", got)
	}
}

func TestQualityLevel(t *testing.T) {
	if qualityLevel(tiddl.Low) >= qualityLevel(tiddl.High) {
		t.Error("Low should rank below High")
	}
	if qualityLevel(tiddl.High) >= qualityLevel(tiddl.Lossless) {
		t.Error("High should rank below Lossless")
	}
	if qualityLevel(tiddl.Lossless) >= qualityLevel(tiddl.HiResLossless) {
		t.Error("Lossless should rank below HiResLossless")
	}
}
