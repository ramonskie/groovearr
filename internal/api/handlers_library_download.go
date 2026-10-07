package api

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/library"
)

// errDownloadNotFound is the path-free sentinel returned for every client-visible
// 404 in the library download handlers. It deliberately carries no server path
// and no resolver text so responses cannot reveal the on-disk layout
// (AGENTS §10).
var errDownloadNotFound = errors.New("not found")

// errDownloadInternal is the path-free sentinel for a genuine server-side
// failure (e.g. the store could not be read). It keeps internal error detail
// out of the response body; the real error is logged instead.
var errDownloadInternal = errors.New("internal error")

// errAlbumZipBusy is the client-visible 503 body returned when the album-zip
// concurrency cap is already fully in use. It carries no internal detail; the
// caller is expected to retry shortly.
var errAlbumZipBusy = errors.New("album download capacity reached, retry shortly")

// albumZipMaxConcurrent bounds how many album zips may stream at once. Each
// zip holds an open archive writer and streams a whole album from disk, so an
// unbounded number of concurrent requests could exhaust file descriptors and
// memory. Three allows a couple of simultaneous users while keeping the
// process predictable; the single-track route is not counted here.
const albumZipMaxConcurrent = 3

// audioMIMEByExt is the explicit Content-Type map for the audio extensions the
// library can hold (H5). mime.TypeByExtension reads /etc/mime.types, which may
// be absent in the Docker image, so known extensions are pinned here and only
// unknown ones fall through to the stdlib lookup.
var audioMIMEByExt = map[string]string{
	".flac": "audio/flac",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".opus": "audio/ogg",
	".ogg":  "audio/ogg",
	".wav":  "audio/wav",
	".aac":  "audio/aac",
}

// audioContentType returns the Content-Type for a file name. It prefers the
// explicit audio map, then mime.TypeByExtension, and finally
// application/octet-stream so the response always carries a concrete type.
func audioContentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ct, ok := audioMIMEByExt[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// zipStoredExts holds extensions whose payload is already compressed (audio
// codecs and image formats), so storing the bytes avoids deflate's wasted CPU
// and possible size growth. Everything else (text playlists, WAV, unknown) uses
// deflate.
var zipStoredExts = map[string]bool{
	".flac": true, ".mp3": true, ".m4a": true, ".aac": true,
	".opus": true, ".ogg": true, ".oga": true,
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true,
}

// zipMethodForName returns the compression method for a zip entry based on its
// name. Already-compressed formats are stored; everything else is deflated.
func zipMethodForName(name string) uint16 {
	if zipStoredExts[strings.ToLower(filepath.Ext(name))] {
		return zip.Store
	}
	return zip.Deflate
}

// contentDispositionAttachment builds the Content-Disposition value for a
// download. It emits both a legacy ASCII `filename=` fallback and an RFC 5987
// `filename*=` parameter so non-ASCII titles survive intact.
func contentDispositionAttachment(name string) string {
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s",
		asciiFilename(name), rfc5987Escape(name))
}

// asciiFilename reduces a name to a safe ASCII-only legacy `filename=` value.
// Non-ASCII bytes fold to "_" and quotes, backslashes and control characters
// (which would break the quoted-string) fold to "_" or are dropped.
func asciiFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '"' || r == '\\' || r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		case r > 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "download"
	}
	return b.String()
}

// rfc5987Escape percent-encodes a value for the RFC 5987 `filename*` form,
// whose value is prefixed UTF-8 then two single quotes. Only attr-char
// (ALPHA / DIGIT and !#$&+-.^_`|~) pass through;
// every other byte becomes %XX so quotes, semicolons, spaces and non-ASCII
// bytes cannot break the header or the parameter grammar.
func rfc5987Escape(s string) string {
	const attrChar = "!#$&+-.^_`|~"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte(attrChar, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// streamWriteDeadline caps how long a streaming download may go without a
// successful write before the connection is cut (H4). It is deliberately
// finite: an authenticated client that opens a download and then stalls must
// eventually be released, otherwise a single caller could pin a connection,
// a goroutine and an open file descriptor forever by never reading.
const streamWriteDeadline = 30 * time.Minute

// extendWriteDeadline replaces the per-request write deadline for a streaming
// download (H4). http.Server.WriteTimeout is a single global response deadline;
// on a large library file or album zip over a slow link it would abort the
// stream at 30s. http.ResponseController lets this route extend its own
// response only, leaving the global server config untouched.
//
// The new deadline is finite (now + streamWriteDeadline) rather than cleared to
// zero. Clearing it would let a stalled authenticated caller hold the
// connection, goroutine and file descriptor indefinitely. Reaching the base
// writer depends on responseWriter.Unwrap; some ResponseWriters (e.g. httptest's
// recorder) do not support deadline control at all. Callers must tolerate the
// returned error and keep serving under the server's normal timeout.
func extendWriteDeadline(w http.ResponseWriter) error {
	return http.NewResponseController(w).SetWriteDeadline(time.Now().Add(streamWriteDeadline))
}

// handleLibraryTrackDownload streams a single library track file to the
// client. It is available to every authenticated caller (not admin-gated) and
// is path-traversal safe: the stored FilePath must resolve to a regular file
// inside the configured library root before any bytes are served.
//
// http.ServeContent provides Range/resume, HEAD, Last-Modified and
// Content-Length for free. Neither the resolved path nor a resolver error is
// ever written to the response.
func (s *Server) handleLibraryTrackDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("trackID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, errDownloadNotFound)
		return
	}

	ctx := r.Context()
	t, err := s.store.GetTrack(ctx, id)
	if err != nil {
		s.log.Error("library track lookup failed", "track_id", id, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, errDownloadInternal)
		return
	}
	if t == nil || t.FilePath == "" {
		writeError(w, http.StatusNotFound, errDownloadNotFound)
		return
	}

	// Resolve and contain the stored path before opening it. On any failure
	// respond 404 with a path-free body — an invalid id, an out-of-root path,
	// a symlink escape and an unreadable file are intentionally indistinguishable.
	resolved, err := library.ResolveWithinRoot(s.cfg.Get().Library.LibraryPath, t.FilePath)
	if err != nil {
		s.log.Warn("library track download rejected", "track_id", id, "error", err, "component", "api")
		writeError(w, http.StatusNotFound, errDownloadNotFound)
		return
	}

	f, err := os.Open(resolved)
	if err != nil {
		// Log a path-free message: *os.PathError carries the absolute path, so
		// only a boolean reason is recorded (AGENTS §10).
		s.log.Warn("library track open failed", "track_id", id, "not_exist", errors.Is(err, os.ErrNotExist), "component", "api")
		writeError(w, http.StatusNotFound, errDownloadNotFound)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		s.log.Warn("library track stat failed", "track_id", id, "component", "api")
		writeError(w, http.StatusNotFound, errDownloadNotFound)
		return
	}

	name := filepath.Base(t.FilePath)

	// Streaming begins now: extend this response's write deadline past the
	// global 30s so a large file over a slow link is not cut (H4). The deadline
	// stays finite (streamWriteDeadline), so a stalled client is eventually
	// released. A writer without deadline support is non-fatal — the download
	// proceeds under the server timeout.
	if err := extendWriteDeadline(w); err != nil {
		s.log.Debug("write deadline not adjustable for download", "track_id", id, "error", err, "component", "api")
	}

	// nosniff stops a browser from content-sniffing the audio bytes into some
	// other executable type when the Content-Type is unexpected.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", audioContentType(name))
	w.Header().Set("Content-Disposition", contentDispositionAttachment(name))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// albumZipEntry pairs a resolved on-disk path with the track metadata that
// names its zip entry. Resolving all entries up front lets the handler fail
// with a clean 404 before any zip bytes are written.
type albumZipEntry struct {
	resolved string
	track    domain.Track
}

// albumZipPlaylist pairs a resolved on-disk .m3u/.m3u8 file with the name it
// is stored under in the archive: its path relative to the album directory,
// slash-separated. Preserving the subdirectory layout lets a playlist's own
// relative track references resolve after extraction.
type albumZipPlaylist struct {
	resolved string
	name     string
}

// albumZipSemaphore returns the album-zip semaphore, creating it on first use.
// Production constructs Server through NewServer, which pre-sizes the channel,
// but handler tests build bare Server{} values; the lazy init keeps the zero
// value usable and avoids a nil-channel panic. sync.Once makes the first
// concurrent call safe, and an already-set channel is left untouched.
func (s *Server) albumZipSemaphore() chan struct{} {
	s.albumZipOnce.Do(func() {
		if s.albumZipSem == nil {
			s.albumZipSem = make(chan struct{}, albumZipMaxConcurrent)
		}
	})
	return s.albumZipSem
}

// acquireAlbumZip takes an album-zip slot without blocking. It reports false
// when every slot is in use so the caller can answer 503 immediately instead
// of queueing a potentially long download behind others.
func (s *Server) acquireAlbumZip() bool {
	select {
	case s.albumZipSemaphore() <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseAlbumZip returns a slot taken by acquireAlbumZip. It never blocks, so
// a stray release against a free semaphore cannot hang a request.
func (s *Server) releaseAlbumZip() {
	select {
	case <-s.albumZipSemaphore():
	default:
	}
}

// handleLibraryAlbumDownload streams an album as a zip archive. It is
// available to every authenticated caller (not admin-gated) and path-traversal
// safe: every stored FilePath must resolve to a regular file inside the
// configured library root before its bytes are added.
//
// Tracks are added in the store's disc/track order. The zip is written
// directly to the ResponseWriter — no temp files and no full buffering — so
// memory stays flat regardless of album size. A single unreadable or
// out-of-root track is skipped (and logged) rather than failing the album.
//
// Album cover art is a conditional, lookup-only addition: when the album
// directory already holds a recognized cover file it is validated against the
// library root and streamed in under its original basename. It is never
// generated, fetched, or rewritten, and its absence never fails the zip.
//
// Any .m3u/.m3u8 playlists already present in the album directory tree are
// likewise passed through verbatim (Phase 2.4) under their album-relative
// path; they are never generated or rewritten, and their absence never fails
// the zip. The archive carries audio tracks plus any on-disk cover and
// playlists.
func (s *Server) handleLibraryAlbumDownload(w http.ResponseWriter, r *http.Request) {
	// Cap concurrent album zips before any store or disk work: a saturated
	// server rejects immediately rather than stacking more large streams. The
	// slot is released on every exit path — success, error, or client
	// disconnect — via the deferred release below.
	if !s.acquireAlbumZip() {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, errAlbumZipBusy)
		return
	}
	defer s.releaseAlbumZip()

	id, err := strconv.ParseInt(r.PathValue("albumID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, errDownloadNotFound)
		return
	}

	ctx := r.Context()
	album, err := s.store.GetAlbum(ctx, id)
	if err != nil {
		s.log.Error("library album lookup failed", "album_id", id, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, errDownloadInternal)
		return
	}
	if album == nil {
		writeError(w, http.StatusNotFound, errDownloadNotFound)
		return
	}

	// The artist name is only needed for the suggested download filename; a
	// missing or unreadable artist is not fatal — fall back to the album title.
	artistName := ""
	if a, err := s.store.GetArtist(ctx, album.ArtistID); err != nil {
		s.log.Warn("library album artist lookup failed", "album_id", id, "artist_id", album.ArtistID, "error", err, "component", "api")
	} else if a != nil {
		artistName = a.Name
	}
	downloadName := albumZipFilename(artistName, album.Title)

	// GetTracksByAlbum is already ORDER BY disc_number, track_number, which is
	// exactly the order the archive should carry.
	tracks, err := s.store.GetTracksByAlbum(ctx, id)
	if err != nil {
		s.log.Error("library album tracks lookup failed", "album_id", id, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, errDownloadInternal)
		return
	}

	// Go's ServeMux serves HEAD through this GET route. Answer it from the
	// track metadata alone: mirror GET's 404 predicate (no track can hold a
	// file) without resolving a single path or building a zip, otherwise
	// advertise the same download headers. A HEAD probe streams no bytes.
	if r.Method == http.MethodHead {
		serveable := 0
		for _, t := range tracks {
			if t.FilePath != "" {
				serveable++
			}
		}
		if serveable == 0 {
			writeError(w, http.StatusNotFound, errDownloadNotFound)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", contentDispositionAttachment(downloadName))
		w.WriteHeader(http.StatusOK)
		return
	}

	// Resolve everything before the response starts: an album whose tracks all
	// have an empty FilePath or fail containment is a clean 404, not a header
	// followed by an empty zip.
	root := s.cfg.Get().Library.LibraryPath
	entries := make([]albumZipEntry, 0, len(tracks))
	for _, t := range tracks {
		if t.FilePath == "" {
			continue
		}
		resolved, err := library.ResolveWithinRoot(root, t.FilePath)
		if err != nil {
			// One bad path must not abort the whole album. Skip and log,
			// keeping the offending path and resolver text out of the log
			// line and the response (AGENTS §10).
			s.log.Warn("library album track rejected", "album_id", id, "track_id", t.ID, "error", err, "component", "api")
			continue
		}
		entries = append(entries, albumZipEntry{resolved: resolved, track: t})
	}
	if len(entries) == 0 {
		writeError(w, http.StatusNotFound, errDownloadNotFound)
		return
	}

	// Cover art is resolved from the album directory of the first serveable
	// track. This is a lookup, not a fetch: the scanner and post-download
	// import already place the file. A missing or rejected cover is skipped
	// silently — the zip succeeds either way. Resolution stays path-free in
	// logs and never reaches the response (AGENTS §10).
	//
	// The directory is derived from the RESOLVED track path, not the stored
	// FilePath: ResolveWithinRoot may have followed a symlinked library_path
	// (or component), so a raw dir and a resolved playlist path would make
	// filepath.Rel yield a spurious "../…" and wrongly reject every playlist.
	// Containment is likewise checked against the resolved root, so the lexical
	// pre-check compares two paths in the same (resolved) space instead of
	// failing a resolved target against a symlinked root.
	albumDir := library.AlbumDirFromTrack(entries[0].resolved)
	resolvedRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		resolvedRoot = r
	}
	coverResolved := ""
	if cover := library.CoverFilePath(albumDir); cover != "" {
		if resolved, err := library.ResolveWithinRoot(resolvedRoot, cover); err != nil {
			s.log.Warn("library album cover rejected", "album_id", id, "error", err, "component", "api")
		} else {
			coverResolved = resolved
		}
	}

	// Existing .m3u/.m3u8 playlists are passed through verbatim (Phase 2.4),
	// never generated or rewritten. Each is resolved against the resolved
	// library root and carried under its album-relative, slash-separated path
	// so a playlist's own relative track references keep working from the
	// archive root. A rejected playlist is skipped silently; a missing one
	// leaves the zip unchanged. Resolution stays path-free in logs (AGENTS §10).
	//
	// A track stored directly under the library root resolves to an album dir
	// equal to the resolved root: there is no album subdirectory, so walking it
	// would traverse the whole library to collect playlists that do not belong
	// to this album. Skip the scan in that case.
	playlists := make([]albumZipPlaylist, 0)
	if albumDir == resolvedRoot {
		s.log.Debug("library album download at library root; skipping playlist scan", "album_id", id, "component", "api")
	} else {
		for _, p := range library.FindM3UFiles(albumDir) {
			resolved, err := library.ResolveWithinRoot(resolvedRoot, p)
			if err != nil {
				s.log.Warn("library album playlist rejected", "album_id", id, "error", err, "component", "api")
				continue
			}
			rel, err := filepath.Rel(albumDir, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				s.log.Warn("library album playlist outside album dir", "album_id", id, "component", "api")
				continue
			}
			playlists = append(playlists, albumZipPlaylist{resolved: resolved, name: filepath.ToSlash(rel)})
		}
	}

	// Streaming begins: extend this response's write deadline (H4) past the
	// server's global 30s so a large album over a slow link is not cut. The
	// deadline is refreshed per entry below so a multi-entry zip keeps making
	// progress, while any single stalled write stays bounded. A writer without
	// deadline support is non-fatal.
	if err := extendWriteDeadline(w); err != nil {
		s.log.Debug("write deadline not adjustable for download", "album_id", id, "error", err, "component", "api")
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDispositionAttachment(downloadName))

	multiDisc := albumHasMultipleDiscs(entries)
	zw := zip.NewWriter(w)
	used := make(map[string]bool, len(entries))
	for _, e := range entries {
		// Refresh the finite write deadline before each entry so a large
		// multi-entry zip on a slow link is not cut mid-stream while a single
		// stalled write is still bounded by streamWriteDeadline. Best-effort:
		// an unsupported writer was already reported at stream start.
		_ = extendWriteDeadline(w)
		opened, err := writeZipFileEntry(zw, e.resolved, func() string {
			return albumZipEntryName(e.track, e.resolved, multiDisc, used)
		})
		if err == nil {
			continue
		}
		if !opened {
			// A single unreadable track is skipped, not fatal: the rest of the
			// album must still download. Log a path-free reason (AGENTS §10).
			s.log.Warn("library album track unreadable; skipping",
				"album_id", id, "track_id", e.track.ID,
				"not_exist", errors.Is(err, os.ErrNotExist), "component", "api")
			continue
		}
		// A copy/write failure here is almost always a client disconnect. The
		// status line is already sent, so log and stop — do not try to write a
		// mid-stream error response.
		s.log.Warn("library album zip write failed", "album_id", id, "track_id", e.track.ID, "component", "api")
		return
	}
	// The cover is written after the tracks and shares the same name-dedupe
	// map, so it can never collide with a track entry. The name is sanitized
	// before dedupe so the used-map keys match the written entries. A read
	// failure is logged path-free and swallowed: a cover problem must not abort
	// an otherwise valid album download.
	if coverResolved != "" {
		opened, err := writeZipFileEntry(zw, coverResolved, func() string {
			return dedupeZipName(safeZipEntryName(filepath.Base(coverResolved)), used)
		})
		if err != nil {
			if !opened {
				s.log.Warn("library album cover unreadable; skipping", "album_id", id, "not_exist", errors.Is(err, os.ErrNotExist), "component", "api")
			} else {
				s.log.Warn("library album cover write failed", "album_id", id, "component", "api")
			}
		}
	}
	// Playlists are written last and share the same name-dedupe map, so a
	// playlist can never collide with a track or cover entry. Each is copied
	// verbatim under its sanitized, album-relative path; a read failure is
	// logged path-free and swallowed so a playlist problem cannot abort an
	// otherwise valid album download.
	for _, p := range playlists {
		opened, err := writeZipFileEntry(zw, p.resolved, func() string {
			return dedupeZipName(safeZipEntryName(p.name), used)
		})
		if err != nil {
			if !opened {
				s.log.Warn("library album playlist unreadable; skipping", "album_id", id, "not_exist", errors.Is(err, os.ErrNotExist), "component", "api")
			} else {
				s.log.Warn("library album playlist write failed", "album_id", id, "component", "api")
			}
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Warn("library album zip close failed", "album_id", id, "error", err, "component", "api")
	}
}

// albumHasMultipleDiscs reports whether the served tracks span more than one
// distinct non-zero disc number, which triggers the "<DD>-<NN> - " prefix (M7).
func albumHasMultipleDiscs(entries []albumZipEntry) bool {
	discs := make(map[int]bool)
	for _, e := range entries {
		if e.track.DiscNumber > 0 {
			discs[e.track.DiscNumber] = true
		}
	}
	return len(discs) > 1
}

// writeZipFileEntry streams one resolved on-disk file into the archive. The
// entry name is produced by nameFn only AFTER the source is successfully
// opened and stat'd, so a caller that dedupes names through a shared map does
// not reserve a name for a file that is then skipped as unreadable. The name
// is sanitized here as defence in depth. The file is opened, copied straight
// into the entry writer, and closed — never buffered in memory or staged to a
// temp file.
//
// The boolean reports whether the source was opened successfully, so a caller
// can tell "this entry is unreadable, skip it" (false) from a genuine
// archive-write failure (true, err != nil) that should abort a streaming
// response. Open/stat errors are returned so the caller can inspect them, but
// they must never be logged verbatim: an *os.PathError embeds the absolute
// path, so callers log a path-free reason instead (AGENTS §10).
func writeZipFileEntry(zw *zip.Writer, resolved string, nameFn func() string) (opened bool, err error) {
	f, err := os.Open(resolved)
	if err != nil {
		return false, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return false, err
	}

	// Claim the (deduped) name now that the entry will definitely be written.
	entryName := safeZipEntryName(nameFn())
	hdr := &zip.FileHeader{
		Name:   entryName,
		Method: zipMethodForName(entryName),
	}
	hdr.SetModTime(fi.ModTime())

	entryWriter, err := zw.CreateHeader(hdr)
	if err != nil {
		return true, err
	}
	if _, err = io.Copy(entryWriter, f); err != nil {
		return true, err
	}
	return true, nil
}

// albumZipEntryName builds the unique, sanitized in-zip name for a track (M7):
// "<NN> - <Title>.<ext>", or "<DD>-<NN> - <Title>.<ext>" for a multi-disc
// album. Colliding names get " (2)", " (3)", ... before the extension.
func albumZipEntryName(t domain.Track, filePath string, multiDisc bool, used map[string]bool) string {
	// A filepath.Ext is not guaranteed free of separators (`\` is legal in a
	// Linux filename), so fold separators/control chars out of it before
	// assembling the name; safeZipEntryName then cleans the whole result.
	ext := sanitizeZipComponent(filepath.Ext(filePath))
	title := sanitizeZipComponent(t.Title)
	if title == "" {
		title = "Track"
	}
	var base string
	if multiDisc {
		base = fmt.Sprintf("%02d-%02d - %s%s", t.DiscNumber, t.TrackNumber, title, ext)
	} else {
		base = fmt.Sprintf("%02d - %s%s", t.TrackNumber, title, ext)
	}
	return dedupeZipName(safeZipEntryName(base), used)
}

// dedupeZipName guarantees every entry name is unique within the archive by
// appending " (2)", " (3)", ... before the extension. Without this, extraction
// tools silently overwrite one colliding track with another (M7).
func dedupeZipName(name string, used map[string]bool) string {
	if !used[name] {
		used[name] = true
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

// sanitizeZipComponent removes characters that are unsafe in a zip entry or a
// suggested download filename: path separators and control characters. It
// preserves Unicode so non-ASCII titles survive intact (archive/zip sets the
// UTF-8 flag for such names automatically).
func sanitizeZipComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// safeZipEntryName returns a zip entry name that can never escape the archive
// root, whatever the source filename contains. Both '/' and '\' are treated as
// separators, so a Windows extractor cannot turn a Linux-legal '\' in a
// filename (or a crafted "..\evil") into a directory traversal. Every segment
// is cleaned: a leading volume designator ("C:") and leading slash are
// dropped, and empty, "." and ".." segments are removed outright, so no ".."
// can ever survive to rejoin as a parent reference.
//
// C0/C1 control characters and Unicode bidi/format controls (which can spoof
// or reorder a displayed path) are stripped from each segment. Legitimate
// nested layout survives: "discs/extra.m3u8" is returned unchanged. When
// nothing survives, "file" is returned so the entry name is never empty.
func safeZipEntryName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	// Drop a leading drive designator ("C:", "z:") before splitting; without
	// this a "C:/evil" name would keep a "C:" first segment.
	if len(name) >= 2 && name[1] == ':' && isASCIILetter(name[0]) {
		name = name[2:]
	}
	parts := strings.Split(name, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = stripUnsafeZipRunes(part)
		if part == "" || part == "." || part == ".." {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return "file"
	}
	return strings.Join(out, "/")
}

// stripUnsafeZipRunes removes the characters that are unsafe or misleading in
// a zip entry segment: C0 and C1 control characters and the Unicode bidi
// embedding/override and isolate controls. Unicode letters otherwise survive.
func stripUnsafeZipRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isUnsafeZipRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isUnsafeZipRune reports whether r must not appear in a zip entry name.
func isUnsafeZipRune(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f: // C0 controls and DEL
		return true
	case r >= 0x80 && r <= 0x9f: // C1 controls
		return true
	case r == 0x200e || r == 0x200f: // LRM / RLM
		return true
	case r == 0x061c: // ARABIC LETTER MARK
		return true
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	}
	return false
}

// isASCIILetter reports whether b is an ASCII letter (a-z, A-Z).
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// albumZipFilename builds the suggested download filename
// "<Artist> - <Album>.zip", degrading gracefully when either part is missing.
// Path-unsafe characters are stripped before the disposition helper turns the
// result into ASCII + RFC 5987 parameters.
func albumZipFilename(artist, album string) string {
	artist = sanitizeZipComponent(artist)
	album = sanitizeZipComponent(album)
	switch {
	case artist != "" && album != "":
		return artist + " - " + album + ".zip"
	case album != "":
		return album + ".zip"
	case artist != "":
		return artist + ".zip"
	default:
		return "album.zip"
	}
}
