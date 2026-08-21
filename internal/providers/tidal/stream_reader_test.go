package tidal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// isContextCanceled reports whether err is a context cancellation, either
// directly or wrapped by the HTTP transport.
func isContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled")
}

// segmentServer returns a test server serving /seg/{n} bodies from the map.
func segmentServer(t *testing.T, bodies map[string]string, fallback http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := bodies[r.URL.Path]; ok {
			_, _ = io.WriteString(w, body)
			return
		}
		if fallback != nil {
			fallback(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMultiStreamReaderConcatenatesSegments(t *testing.T) {
	const (
		seg1 = "segment-one-"
		seg2 = "segment-two"
	)

	srv := segmentServer(t, map[string]string{
		"/seg/1": seg1,
		"/seg/2": seg2,
	}, nil)

	reader := newMultiStreamReader(context.Background(), &http.Client{Timeout: 5 * time.Second},
		[]string{srv.URL + "/seg/1", srv.URL + "/seg/2"})
	t.Cleanup(func() { _ = reader.Close() })

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != seg1+seg2 {
		t.Fatalf("data = %q, want %q", got, seg1+seg2)
	}
}

func TestMultiStreamReaderPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	reader := newMultiStreamReader(context.Background(), &http.Client{Timeout: 5 * time.Second},
		[]string{srv.URL + "/seg/1"})
	t.Cleanup(func() { _ = reader.Close() })

	_, err := io.ReadAll(reader)
	if err == nil {
		t.Fatal("expected error for HTTP 403 segment")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("err = %q, want HTTP 403 mention", err)
	}
}

func TestMultiStreamReaderCloseIsIdempotent(t *testing.T) {
	srv := segmentServer(t, map[string]string{"/seg/1": "ok"}, nil)

	reader := newMultiStreamReader(context.Background(), &http.Client{Timeout: 5 * time.Second},
		[]string{srv.URL + "/seg/1"})

	if err := reader.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	_, err := reader.Read(make([]byte, 8))
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Read after Close error = %v, want closed error", err)
	}
}

func TestMultiStreamReaderPrefetchFetchesNextSegment(t *testing.T) {
	var seg2Requested atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/seg/1":
			_, _ = io.WriteString(w, strings.Repeat("a", 128))
		case "/seg/2":
			seg2Requested.Store(true)
			_, _ = io.WriteString(w, "b")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	reader := newMultiStreamReader(context.Background(), &http.Client{Timeout: 5 * time.Second},
		[]string{srv.URL + "/seg/1", srv.URL + "/seg/2"})
	t.Cleanup(func() { _ = reader.Close() })

	// Read a small prefix from segment 1; the reader should already be
	// prefetching segment 2 in the background.
	if _, err := reader.Read(make([]byte, 8)); err != nil {
		t.Fatalf("first Read: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for !seg2Requested.Load() {
		if err := waitCtx.Err(); err != nil {
			t.Fatal("prefetch did not start fetching the next segment")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMultiStreamReaderEmptyURLs(t *testing.T) {
	reader := newMultiStreamReader(context.Background(), &http.Client{}, nil)
	t.Cleanup(func() { _ = reader.Close() })

	_, err := reader.Read(make([]byte, 8))
	if err != io.EOF {
		t.Fatalf("Read = %v, want io.EOF", err)
	}
}

func TestMultiStreamReaderContextCancellationWhileWaitingForPrefetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/seg/1":
			_, _ = io.WriteString(w, "one")
		case "/seg/2":
			// Block until the client cancels so the prefetch never completes
			// and Read must notice ctx.Done().
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	reader := newMultiStreamReader(ctx, &http.Client{Timeout: 5 * time.Second},
		[]string{srv.URL + "/seg/1", srv.URL + "/seg/2"})
	t.Cleanup(func() { _ = reader.Close() })

	buf := make([]byte, 16)
	n, err := reader.Read(buf)
	if err != nil || n != 3 || string(buf[:n]) != "one" {
		t.Fatalf("first Read = %d, %v, want 3 bytes %q", n, err, "one")
	}

	cancel()
	_, err = reader.Read(buf)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read after cancel = %v, want context.Canceled", err)
	}
}

func TestMultiStreamReaderContextCancellationDuringSegmentRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "abc")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Keep the response open so a subsequent Read blocks until cancel.
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	reader := newMultiStreamReader(ctx, &http.Client{Timeout: 5 * time.Second},
		[]string{srv.URL + "/seg/1"})
	t.Cleanup(func() { _ = reader.Close() })

	buf := make([]byte, 16)
	n, err := reader.Read(buf)
	if err != nil || n != 3 || string(buf[:n]) != "abc" {
		t.Fatalf("first Read = %d, %v, want 3 bytes %q", n, err, "abc")
	}

	cancel()
	_, err = reader.Read(buf)
	if err == nil || !isContextCanceled(err) {
		t.Fatalf("Read after cancel = %v, want context cancellation", err)
	}
}
