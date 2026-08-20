package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestBufferRingAndDedupe(t *testing.T) {
	b := NewBuffer(3)
	for i := 0; i < 5; i++ {
		b.Append(Entry{Seq: uint64(i + 1), Message: "m"})
	}
	snap := b.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 entries after overflow, got %d", len(snap))
	}
	if snap[0].Seq != 3 || snap[2].Seq != 5 {
		t.Fatalf("expected oldest evicted (seq 3..5), got %v", seqs(snap))
	}
	if b.Len() != 3 || b.Max() != 3 {
		t.Fatalf("Len/Max mismatch: %d/%d", b.Len(), b.Max())
	}

	b.Clear()
	if b.Len() != 0 {
		t.Fatalf("expected empty buffer after clear")
	}
	b.Append(Entry{Seq: 9, Message: "x"})
	snap = b.Snapshot()
	if len(snap) != 1 || snap[0].Seq != 9 {
		t.Fatalf("clear must preserve monotonic seq, got %v", seqs(snap))
	}
}

func TestBufferSubscribe(t *testing.T) {
	b := NewBuffer(10)
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Append(Entry{Seq: 1, Message: "one"})
	b.Append(Entry{Seq: 2, Message: "two"})

	got := []string{}
	for i := 0; i < 2; i++ {
		e := <-ch
		got = append(got, e.Message)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("expected both entries in order, got %v", got)
	}
}

func TestCaptureHandlerStoresEntries(t *testing.T) {
	b := NewBuffer(50)
	inner := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	capture := &captureHandler{inner: inner, buff: b}

	log := slog.New(capture)
	log.Info("hello", "key", "value", "num", 42)
	log.Warn("grouped", slog.Group("g", slog.String("a", "b")))

	snap := b.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 captured entries, got %d", len(snap))
	}
	if snap[0].Message != "hello" || snap[0].Level != "INFO" {
		t.Fatalf("unexpected first entry: %+v", snap[0])
	}
	if snap[0].Attrs["key"] != "value" || snap[0].Attrs["num"] != int64(42) {
		t.Fatalf("attrs not captured: %+v", snap[0].Attrs)
	}
	if snap[1].Attrs["g.a"] != "b" {
		t.Fatalf("group attrs not flattened: %+v", snap[1].Attrs)
	}
}

func TestLevelGatingAndRuntimeChange(t *testing.T) {
	dir := t.TempDir()
	log, b, rot, closeFn := New(Config{Level: "info", CapturedMax: 50, MaxSizeMB: 5}, filepath.Join(dir, "test.log"))
	defer closeFn()
	if rot.Level() != "info" {
		t.Fatalf("expected default level info, got %s", rot.Level())
	}

	log.Debug("hidden")
	if b.Len() != 0 {
		t.Fatalf("debug should be filtered at info level")
	}

	rot.SetLevel("debug")
	if rot.Level() != "debug" {
		t.Fatalf("expected level debug after change, got %s", rot.Level())
	}
	log.Debug("visible", "now", true)
	if b.Len() != 1 {
		t.Fatalf("expected debug entry after raising level, got %d", b.Len())
	}
}

func TestRotatorWriteProducesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rot.log")
	_, _, rot, closeFn := New(DefaultConfig(), path)
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
	_, _, rot, closeFn := New(Config{}, filepath.Join(dir, "x.log"))
	defer closeFn()
	rot.SetConfig(Config{MaxSizeMB: 10, MaxBackups: 3, MaxAgeDays: 7, Compress: true})
	if rot.lj.MaxSize != 10 || rot.lj.MaxBackups != 3 || rot.lj.MaxAge != 7 || !rot.lj.Compress {
		t.Fatalf("expected default rotation values, got %+v", rot.lj)
	}
}

func seqs(entries []Entry) []uint64 {
	out := make([]uint64, len(entries))
	for i, e := range entries {
		out[i] = e.Seq
	}
	return out
}
