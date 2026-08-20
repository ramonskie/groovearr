package logger

import "sync"

// Buffer is a thread-safe ring buffer of captured log entries, plus a small
// fan-out for pushing new entries to live subscribers (SSE bridging).
type Buffer struct {
	mu      sync.Mutex
	seq     uint64
	entries []Entry
	max     int
	subs    map[chan Entry]struct{}
}

// NewBuffer creates a ring buffer holding at most max entries. Subscribed
// channels receive every newly appended entry; slow consumers have entries
// silently dropped (non-blocking).
func NewBuffer(max int) *Buffer {
	if max <= 0 {
		max = 2000
	}
	return &Buffer{
		entries: make([]Entry, 0, max),
		max:     max,
		subs:    make(map[chan Entry]struct{}),
	}
}

// Append adds an entry, evicting the oldest when at capacity.
func (b *Buffer) Append(e Entry) {
	b.mu.Lock()
	if e.Seq == 0 {
		b.seq++
		e.Seq = b.seq
	}
	if len(b.entries) >= b.max {
		b.entries = append(b.entries[:0], b.entries[1:]...)
	}
	b.entries = append(b.entries, e)
	subs := make([]chan Entry, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// Slow subscriber — drop to avoid blocking the log path.
		}
	}
}

// Snapshot returns a copy of the buffered entries, oldest first.
func (b *Buffer) Snapshot() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Entry, len(b.entries))
	copy(out, b.entries)
	return out
}

// Clear empties the buffer. The monotonic sequence counter is preserved so UI
// deduplication continues to work across clears.
func (b *Buffer) Clear() {
	b.mu.Lock()
	b.entries = b.entries[:0]
	b.mu.Unlock()
}

// Len returns the number of buffered entries.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// Max returns the ring buffer capacity.
func (b *Buffer) Max() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max
}

// Subscribe registers ch to receive appended entries. The caller must invoke
// Unsubscribe with the same channel when done.
func (b *Buffer) Subscribe() chan Entry {
	ch := make(chan Entry, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes ch from the fan-out.
func (b *Buffer) Unsubscribe(ch chan Entry) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}
