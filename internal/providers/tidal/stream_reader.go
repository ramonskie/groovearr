// Package tidal implements the Tidal music streaming plugin.
//
// This file ports the upstream Tidal client's segment reader (stream_reader.go)
// to the native plugin client.
//
// Ported from the binozo Tidal client library v0.1.0 (Apache-2.0).
// The upstream client does NO decryption and NO urlPost/signing — plain GET works.
package tidal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// multiStreamReader reads from a sequence of HTTP URLs, concatenating their
// response bodies into a single io.ReadCloser. It prefetches the next segment
// concurrently for improved throughput.
type multiStreamReader struct {
	ctx        context.Context
	cancel     context.CancelFunc
	client     *http.Client
	urls       []string
	index      int
	current    io.ReadCloser
	nextDone   chan struct{}
	nextResult *prefetchResult
	mu         sync.Mutex
	closed     bool
}

type prefetchResult struct {
	reader io.ReadCloser
	err    error
}

func newMultiStreamReader(ctx context.Context, client *http.Client, urls []string) *multiStreamReader {
	ctx, cancel := context.WithCancel(ctx)

	return &multiStreamReader{
		ctx:    ctx,
		cancel: cancel,
		client: client,
		urls:   urls,
		index:  0,
	}
}

// Read concatenates the segment bodies. It honors the reader's context
// cancellation both while waiting for a prefetch and during segment requests.
// All shared state is guarded by m.mu; the only I/O performed without the
// lock is the actual Read on a captured segment body, which Close may
// concurrently terminate — never a data race on reader state.
func (m *multiStreamReader) Read(p []byte) (int, error) {
	for {
		cur, err := m.currentReader()
		if err != nil {
			return 0, err
		}
		if cur == nil {
			return 0, io.EOF
		}

		n, err := cur.Read(p)
		if err == nil {
			return n, nil
		}
		if err != io.EOF {
			m.dropCurrent()
			return n, err
		}
		m.dropCurrent()
		if n > 0 {
			return n, nil
		}
	}
}

// currentReader returns the next segment body to read from, opening segments
// or consuming a prefetch as needed. It returns (nil, nil) at EOF. It never
// holds m.mu while waiting on a prefetch or performing network I/O, so Close
// can always proceed.
func (m *multiStreamReader) currentReader() (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for m.current == nil {
		if m.closed {
			return nil, fmt.Errorf("tidal: stream reader closed")
		}

		if m.nextDone != nil {
			// Wait for the in-flight prefetch without holding the lock so
			// Close() is not blocked from cancelling the context.
			m.mu.Unlock()
			select {
			case <-m.nextDone:
			case <-m.ctx.Done():
				m.mu.Lock()
				return nil, m.ctx.Err()
			}
			m.mu.Lock()

			// Close may have run while we waited; prefer the explicit closed
			// error over a transport error from the prefetched body.
			if m.closed {
				return nil, fmt.Errorf("tidal: stream reader closed")
			}

			// nextResult is fully written before nextDone is closed
			// (happens-before via the channel close), so this is safe.
			m.current = m.nextResult.reader
			err := m.nextResult.err
			m.nextResult = nil
			m.nextDone = nil
			if err != nil {
				return nil, err
			}
			if m.current == nil {
				return nil, nil // EOF
			}
			m.prefetchNextLocked()
			continue
		}

		// No prefetch pending — open the next segment synchronously.
		m.mu.Unlock()
		reader, err := m.openSegment()
		m.mu.Lock()
		if err != nil {
			return nil, err
		}
		if reader == nil {
			return nil, nil // EOF
		}
		m.current = reader
		m.prefetchNextLocked()
	}

	return m.current, nil
}

// dropCurrent closes and clears the current segment body. Safe to call at any
// time, including concurrently with Close.
func (m *multiStreamReader) dropCurrent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.current.Close()
		m.current = nil
	}
}

// openSegment opens the next segment URL and returns its response body.
// It returns (nil, nil) when all segments have been consumed.
func (m *multiStreamReader) openSegment() (io.ReadCloser, error) {
	m.mu.Lock()
	if m.index >= len(m.urls) {
		m.mu.Unlock()
		return nil, nil
	}
	url := m.urls[m.index]
	m.index++
	m.mu.Unlock()

	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("tidal: failed to create segment request: %w", err)
	}

	res, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tidal: failed to download stream segment: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		err := fmt.Errorf("tidal: failed to download stream segment: HTTP %d", res.StatusCode)
		res.Body.Close()
		return nil, err
	}

	return res.Body, nil
}

// prefetchNextLocked starts fetching the next segment in the background so it
// is ready when the current segment is exhausted. The caller must hold m.mu.
func (m *multiStreamReader) prefetchNextLocked() {
	if m.index >= len(m.urls) {
		return
	}

	done := make(chan struct{})
	m.nextDone = done

	go func() {
		reader, err := m.openSegment()
		if m.ctx.Err() != nil {
			if reader != nil {
				reader.Close()
			}
			err = m.ctx.Err()
		}
		m.mu.Lock()
		m.nextResult = &prefetchResult{reader: reader, err: err}
		m.mu.Unlock()
		close(done)
	}()
}

// Close cancels the reader context and releases the current segment body.
func (m *multiStreamReader) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}

	m.closed = true
	m.cancel()

	if m.current != nil {
		m.current.Close()
		m.current = nil
	}

	return nil
}
