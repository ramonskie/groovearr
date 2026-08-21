package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// Entry is one log line exposed to the UI. Raw is the untouched line exactly
// as it appears in the log file — the viewer renders it verbatim. Time and
// Level are parsed only as metadata for ordering and color coding; they never
// replace the raw content.
type Entry struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Raw   string    `json:"raw"`
}

// parseLine extracts time/level metadata from one log file line and keeps the
// line verbatim in Raw. Lines written by the slog JSON handler or TextHandler
// are recognized; anything else is kept as an INFO entry so no log content is
// ever dropped or transformed.
func parseLine(line string) Entry {
	if e, ok := parseJSONMeta(line); ok {
		return e
	}
	if e, ok := parseTextMeta(line); ok {
		return e
	}
	return Entry{Level: "INFO", Raw: line}
}

// parseJSONMeta reads time/level from one slog JSON handler line.
func parseJSONMeta(line string) (Entry, bool) {
	var raw struct {
		Time  string `json:"time"`
		Level string `json:"level"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Entry{}, false
	}
	e := Entry{Level: "INFO", Raw: line}
	if raw.Level != "" {
		e.Level = raw.Level
	}
	if t, err := time.Parse(time.RFC3339Nano, raw.Time); err == nil {
		e.Time = t
	}
	return e, true
}

// parseTextMeta reads time/level from one slog TextHandler line (time=...,
// level=... tokens). Values are space-separated key=value tokens; quoted
// values are unquoted.
func parseTextMeta(line string) (Entry, bool) {
	e := Entry{Level: "INFO", Raw: line}
	found := false

	for pos := 0; pos < len(line); {
		// Skip whitespace between tokens.
		for pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
			pos++
		}
		if pos >= len(line) {
			break
		}

		// Read key up to '='.
		eq := pos
		for eq < len(line) && line[eq] != '=' && line[eq] != ' ' && line[eq] != '\t' {
			eq++
		}
		if eq >= len(line) || line[eq] != '=' {
			// No '=' — not a key=value token; give up on this line.
			break
		}
		key := line[pos:eq]
		pos = eq + 1

		// Read value: quoted string or bare token.
		var val string
		if pos < len(line) && line[pos] == '"' {
			end := pos + 1
			for end < len(line) {
				if line[end] == '\\' {
					end += 2
					continue
				}
				if line[end] == '"' {
					break
				}
				end++
			}
			if end >= len(line) {
				return Entry{}, false // unterminated quote
			}
			if unq, err := strconv.Unquote(line[pos : end+1]); err == nil {
				val = unq
			} else {
				val = line[pos+1 : end]
			}
			pos = end + 1
		} else {
			start := pos
			for pos < len(line) && line[pos] != ' ' && line[pos] != '\t' {
				pos++
			}
			val = line[start:pos]
		}

		switch key {
		case "time":
			if t, err := time.Parse(time.RFC3339Nano, val); err == nil {
				e.Time = t
				found = true
			}
		case "level":
			if val != "" {
				e.Level = val
				found = true
			}
		}
	}
	if !found {
		return Entry{}, false
	}
	return e, true
}

// ReadTail returns up to maxLines entries from the end of the log file at
// filePath, oldest first. It scans backward so it only reads the tail of large
// files. Missing files return an empty result with a nil error so the API can
// report "no logs yet" instead of failing.
func ReadTail(filePath string, maxLines int) ([]Entry, error) {
	if maxLines <= 0 {
		maxLines = 500
	}
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() == 0 {
		return nil, nil
	}

	lines, err := tailLines(f, st.Size(), maxLines)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(lines))
	for _, ln := range lines {
		if ln != "" {
			entries = append(entries, parseLine(ln))
		}
	}
	return entries, nil
}

// tailLines scans a file backward from size and returns the last maxLines
// non-empty lines in reading order (oldest first).
func tailLines(f *os.File, size int64, maxLines int) ([]string, error) {
	const chunkSize = 64 * 1024

	// Collect chunks from the end of the file until we have enough newlines
	// or reach the start. Chunks are stored reversed so we can assemble the
	// tail in reading order afterwards.
	var chunks [][]byte
	seen := 0
	pos := size
	for pos > 0 && seen <= maxLines {
		n := int64(chunkSize)
		if pos < n {
			n = pos
		}
		pos -= n
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return nil, err
		}
		seen += bytes.Count(buf, []byte{'\n'})
		chunks = append(chunks, buf)
	}

	// Reassemble in reading order (reverse the chunk list).
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	all := make([]byte, 0, total)
	for i := len(chunks) - 1; i >= 0; i-- {
		all = append(all, chunks[i]...)
	}

	// Keep the last maxLines lines. If the file ends with a newline there is
	// one trailing empty token after Split; drop it before counting.
	tokens := bytes.Split(all, []byte{'\n'})
	if len(tokens) > 0 && len(tokens[len(tokens)-1]) == 0 {
		tokens = tokens[:len(tokens)-1]
	}
	if len(tokens) > maxLines {
		tokens = tokens[len(tokens)-maxLines:]
	}

	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, string(t))
	}
	return out, nil
}

// Tailer follows a growing log file and invokes emit for every new entry. It
// is rotation-aware: when lumberjack renames the current file and starts a new
// one (new inode or a smaller file at the same path), it transparently reopens
// and continues from the beginning of the new file. On the first open it
// starts at the current end of the file so existing history is not replayed —
// history is served separately by ReadTail. Polling is used instead of
// inotify so there is no OS-specific dependency.
type Tailer struct {
	path     string
	emit     func(Entry)
	interval time.Duration

	mu     sync.Mutex
	f      *os.File
	off    int64
	carry  []byte // partial line carried across reads
	opened bool   // true once the file has been opened at least once
}

// NewTailer creates a Tailer for filePath. emit is called serially from the
// tail loop for every new entry.
func NewTailer(filePath string, emit func(Entry)) *Tailer {
	return &Tailer{
		path:     filePath,
		emit:     emit,
		interval: 500 * time.Millisecond,
	}
}

// Start begins following the file in a background goroutine. The goroutine
// exits when ctx is cancelled.
func (t *Tailer) Start(ctx context.Context) {
	go t.loop(ctx)
}

func (t *Tailer) loop(ctx context.Context) {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.tail()
		}
	}
}

// tail performs one poll: (re)open the file if needed, read new bytes since
// the last offset, split into complete lines, and emit parsed entries.
func (t *Tailer) tail() {
	t.mu.Lock()
	defer t.mu.Unlock()

	st, err := os.Stat(t.path)
	if err != nil {
		// File gone (e.g. rotation window) — drop the fd so the next poll
		// reopens whatever is there.
		if t.f != nil {
			_ = t.f.Close()
			t.f = nil
		}
		t.off = 0
		return
	}

	if t.f == nil {
		f, err := os.Open(t.path)
		if err != nil {
			return
		}
		t.f = f
		if !t.opened {
			// First open: skip existing history (ReadTail serves it).
			t.off = st.Size()
			t.opened = true
		} else {
			// Reopen after the file was missing: start from the top of the
			// new file.
			t.off = 0
		}
	} else {
		cur, err := t.f.Stat()
		if err != nil || cur.Size() < t.off || !os.SameFile(cur, st) {
			// Rotated or truncated. Drain any bytes appended to the OLD file
			// since our last read (they live in the renamed inode, not the
			// new file at path) before closing it, so a burst written right
			// before rotation is not lost from the viewer.
			if err == nil && cur.Size() > t.off {
				t.readAndEmit(cur.Size())
			}
			_ = t.f.Close()
			f, err := os.Open(t.path)
			if err != nil {
				t.f = nil
				t.off = 0
				return
			}
			t.f = f
			t.off = 0
			t.carry = nil
		}
	}

	if st.Size() <= t.off {
		return
	}
	t.readAndEmit(st.Size())
}

// readAndEmit reads new bytes from t.f between t.off and end, splits them
// into complete lines, and emits parsed entries. The caller must hold t.mu.
// A trailing line without a newline is carried into the next read.
func (t *Tailer) readAndEmit(end int64) {
	buf := make([]byte, end-t.off)
	n, _ := t.f.ReadAt(buf, t.off)
	t.off += int64(n)
	if n == 0 {
		return
	}
	buf = buf[:n]

	// Prepend any carried partial line from the previous read.
	if len(t.carry) > 0 {
		buf = append(t.carry, buf...)
		t.carry = nil
	}

	lines := bytes.Split(buf, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) > 0 {
		// Last token has no trailing newline yet — carry it forward.
		t.carry = append(t.carry, lines[len(lines)-1]...)
		lines = lines[:len(lines)-1]
	}

	for _, ln := range lines {
		if len(ln) == 0 {
			continue
		}
		t.emit(parseLine(string(ln)))
	}
}

// Close releases the underlying file handle.
func (t *Tailer) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.f != nil {
		_ = t.f.Close()
		t.f = nil
	}
}
