package logger

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLevelGatingAndRuntimeChange(t *testing.T) {
	dir := t.TempDir()
	_, rot, closeFn := New(Config{Level: "info", MaxSizeMB: 5}, filepath.Join(dir, "test.log"))
	defer closeFn()
	if rot.Level() != "info" {
		t.Fatalf("expected default level info, got %s", rot.Level())
	}

	rot.SetLevel("debug")
	if rot.Level() != "debug" {
		t.Fatalf("expected level debug after change, got %s", rot.Level())
	}
}

func TestRotatorWriteProducesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rot.log")
	_, rot, closeFn := New(DefaultConfig(), path)
	defer closeFn()

	slog.New(slog.NewJSONHandler(rot.lj, nil)).Info("write to file")

	if rot.Path() != path {
		t.Fatalf("unexpected path %q", rot.Path())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
}

func TestRotatorSetConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	_, rot, closeFn := New(Config{}, filepath.Join(dir, "x.log"))
	defer closeFn()
	rot.SetConfig(Config{MaxSizeMB: 10, MaxBackups: 3, MaxAgeDays: 7, Compress: true})
	if rot.lj.MaxSize != 10 || rot.lj.MaxBackups != 3 || rot.lj.MaxAge != 7 || !rot.lj.Compress {
		t.Fatalf("expected default rotation values, got %+v", rot.lj)
	}
}

func TestParseTextLine(t *testing.T) {
	line := `time=2026-08-21T10:00:00.000+02:00 level=WARN msg="hello world" k=v`
	e, ok := parseTextMeta(line)
	if !ok {
		t.Fatalf("expected text line to parse")
	}
	// Raw is preserved verbatim — the viewer must show exactly the file line.
	if e.Raw != line {
		t.Fatalf("expected raw line preserved, got %q", e.Raw)
	}
	if e.Level != "WARN" {
		t.Fatalf("expected level WARN, got %s", e.Level)
	}
	if e.Time.IsZero() {
		t.Fatalf("expected parsed time, got zero")
	}

	// Bare unquoted level token.
	e, ok = parseTextMeta(`time=2026-08-21T10:00:00Z level=INFO msg=single`)
	if !ok || e.Level != "INFO" || e.Raw == "" {
		t.Fatalf("unexpected bare-token parse: %+v ok=%v", e, ok)
	}

	// Non-token garbage must not parse.
	if _, ok := parseTextMeta("just a plain line"); ok {
		t.Fatalf("plain text should not parse as a token line")
	}
}

func TestReadTailParsesJSONLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.log")

	lines := []string{
		`{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"one","k":"v"}`,
		`{"time":"2026-08-21T10:00:01Z","level":"ERROR","msg":"two"}`,
		"raw text line",
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadTail(path, 10)
	if err != nil {
		t.Fatalf("read tail failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// Raw content is the exact file line — no reconstruction.
	if entries[0].Raw != lines[0] {
		t.Fatalf("expected raw line preserved, got %q", entries[0].Raw)
	}
	if entries[0].Level != "INFO" {
		t.Fatalf("unexpected first level: %+v", entries[0])
	}
	if entries[1].Level != "ERROR" {
		t.Fatalf("unexpected second level: %+v", entries[1])
	}
	// Non-JSON line is kept verbatim, not dropped.
	if entries[2].Raw != "raw text line" || entries[2].Level != "INFO" {
		t.Fatalf("expected raw fallback entry, got %+v", entries[2])
	}
}

func TestReadTailRespectsMaxLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.log")
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, `{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"line"}`)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadTail(path, 5)
	if err != nil {
		t.Fatalf("read tail failed: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}
}

func TestReadTailMissingAndEmpty(t *testing.T) {
	dir := t.TempDir()
	entries, err := ReadTail(filepath.Join(dir, "missing.log"), 10)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries for missing file, got %d", len(entries))
	}

	path := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err = ReadTail(path, 10)
	if err != nil {
		t.Fatalf("empty file should not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries for empty file, got %d", len(entries))
	}
}

func TestTailerEmitsNewLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tail.log")
	if err := os.WriteFile(path, []byte(`{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"old"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	tr := NewTailer(path, func(e Entry) {
		mu.Lock()
		got = append(got, e.Raw)
		mu.Unlock()
	})
	tr.interval = 10 * time.Millisecond
	tr.tail() // first poll only reads existing content

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.Start(ctx)

	// Append new lines after the tailer has started.
	time.Sleep(20 * time.Millisecond)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"time":"2026-08-21T10:00:01Z","level":"INFO","msg":"new1"}` + "\n")
	f.WriteString(`{"time":"2026-08-21T10:00:02Z","level":"WARN","msg":"new2"}` + "\n")
	f.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != `{"time":"2026-08-21T10:00:01Z","level":"INFO","msg":"new1"}` || got[1] != `{"time":"2026-08-21T10:00:02Z","level":"WARN","msg":"new2"}` {
		t.Fatalf("expected new lines in order, got %v", got)
	}
}

func TestTailerDrainsOldFileOnRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tail.log")
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	tr := NewTailer(path, func(e Entry) {
		mu.Lock()
		got = append(got, e.Raw)
		mu.Unlock()
	})
	tr.interval = 10 * time.Millisecond
	tr.tail() // consume existing content

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.Start(ctx)

	// Simulate rotation: rename the file, append a line to the OLD inode
	// (now the .1 backup), then create the fresh file. The tailer must drain
	// the old-file append and emit it even though the path now points at a
	// different file.
	time.Sleep(20 * time.Millisecond)
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	oldF, err := os.OpenFile(path+".1", os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	oldF.WriteString(`{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"drained-before-rotation"}` + "\n")
	oldF.Close()
	if err := os.WriteFile(path, []byte(`{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"fresh-file"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != `{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"drained-before-rotation"}` || got[1] != `{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"fresh-file"}` {
		t.Fatalf("expected drained + fresh lines in order, got %v", got)
	}
}

func TestTailerReopensAfterRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tail.log")
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got []string
	tr := NewTailer(path, func(e Entry) {
		mu.Lock()
		got = append(got, e.Raw)
		mu.Unlock()
	})
	tr.interval = 10 * time.Millisecond
	tr.tail() // consume existing content

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.Start(ctx)

	// Simulate lumberjack rotation: rename current file, create a fresh one.
	time.Sleep(20 * time.Millisecond)
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"after-rotation"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != `{"time":"2026-08-21T10:00:00Z","level":"INFO","msg":"after-rotation"}` {
		t.Fatalf("expected post-rotation line emitted, got %v", got)
	}
}
