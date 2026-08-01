package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ramonskie/groovearr/internal/discovery"
	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/download"
	"github.com/ramonskie/groovearr/internal/library"
)

// ─── Library handlers ────────────────────────────────────────────────

func (s *Server) handleLibraryTracks(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	tracks, err := s.store.SearchTracks(ctx, q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if tracks == nil {
		tracks = []domain.Track{}
	}

	// Exclude tracks that live under the playlist path — playlist files
	// are managed by buildPlaylistFolder and shouldn't appear in the library view.
	cfg := s.cfg.Get()
	if cfg.Library.PlaylistPath != "" {
		absPlaylist, _ := filepath.Abs(cfg.Library.PlaylistPath)
		tracks = filterByPath(tracks, absPlaylist)
	}

	_ = offset
	writeJSON(w, http.StatusOK, tracks)
}

// filterByPath removes tracks whose FilePath starts with excludeRoot.
func filterByPath(tracks []domain.Track, excludeRoot string) []domain.Track {
	out := tracks[:0]
	for _, t := range tracks {
		if t.FilePath == "" || !strings.HasPrefix(t.FilePath, excludeRoot) {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) handleLibraryArtists(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	var artists []domain.Artist
	var err error
	if q != "" {
		artists, err = s.store.SearchArtists(ctx, q, limit)
	} else {
		artists, err = s.store.ListArtists(ctx, offset, limit)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artists == nil {
		artists = []domain.Artist{}
	}

	// Transform local image paths to API URLs for the frontend.
	for i := range artists {
		if artists[i].ThumbURL == "artist.jpg" {
			artists[i].ThumbURL = fmt.Sprintf("/api/artist-image/%d", artists[i].ID)
		}
	}

	writeJSON(w, http.StatusOK, artists)
}

func (s *Server) handleLibraryAlbums(w http.ResponseWriter, r *http.Request) {
	q, offset, limit := parsePagination(r)
	ctx := r.Context()
	albums, err := s.store.SearchAlbums(ctx, q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.Album{}
	}
	_ = offset
	writeJSON(w, http.StatusOK, albums)
}

// sqlDBProvider is satisfied by types that expose a *sql.DB (e.g. *sqlite.Store).
type sqlDBProvider interface {
	DB() *sql.DB
}

// DiscoveryTrackEntry is a track from discovery merged with library download status.
type discoveryTrackEntry struct {
	Title          string `json:"title"`
	TrackNumber    int    `json:"track_number"`
	DurationMs     int64  `json:"duration_ms"`
	Downloaded     bool   `json:"downloaded"`
	LibraryTrackID int64  `json:"library_track_id,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	FileSize       int64  `json:"file_size,omitempty"`
	Bitrate        int    `json:"bitrate,omitempty"`
	Format         string `json:"format,omitempty"`
}

// albumDiscoveryResponse is the JSON payload for the discovery endpoint.
type albumDiscoveryResponse struct {
	Provider        string                `json:"provider"`
	ProviderAlbumID string                `json:"provider_album_id"`
	Tracks          []discoveryTrackEntry `json:"tracks"`
}

// handleLibraryAlbumDiscovery searches discovery providers for an album's full
// track list, caches the result, and returns tracks merged with library download status.
func (s *Server) handleLibraryAlbumDiscovery(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()

	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if album == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	// Try the cache first (requires concrete DB access).
	var cachedProvider, cachedAlbumID, cachedTracksJSON, cachedAt string
	if dbp, ok := s.store.(sqlDBProvider); ok {
		err := dbp.DB().QueryRowContext(ctx,
			`SELECT provider_name, provider_album_id, tracks_json, cached_at FROM album_discovery_cache WHERE album_id = ?`, albumID,
		).Scan(&cachedProvider, &cachedAlbumID, &cachedTracksJSON, &cachedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("album discovery cache read failed", "album_id", albumID, "error", err, "component", "api")
		}
	}

	var discoveryTracks []discovery.TrackInfo
	if cachedTracksJSON != "" {
		// Check cache TTL (24h).
		if cachedAt != "" {
			if t, parseErr := time.Parse(time.RFC3339, cachedAt); parseErr == nil {
				if time.Since(t) > 24*time.Hour {
					cachedTracksJSON = "" // expired
				}
			} else {
				cachedTracksJSON = "" // unparseable timestamp = treat as expired
			}
		}
	}
	if cachedTracksJSON != "" {
		// Cache hit — unmarshal cached tracks.
		if uerr := json.Unmarshal([]byte(cachedTracksJSON), &discoveryTracks); uerr != nil {
			s.log.Warn("album discovery cache corrupt, refetching", "album_id", albumID, "error", uerr, "component", "api")
			discoveryTracks = nil
			cachedProvider = ""
			cachedAlbumID = ""
		}
	}

	// Cache miss — search discovery providers.
	if discoveryTracks == nil && s.discoveryReg != nil {
		providers := s.discoveryReg.Any()
		query := artist.Name + " " + album.Title
		for _, p := range providers {
			albums, serr := p.SearchAlbums(ctx, query, 5)
			if serr != nil || len(albums) == 0 {
				continue
			}
			// Find the album that best matches our artist + title.
			var matched *discovery.AlbumResult
			for i := range albums {
				a := &albums[i]
				if strings.EqualFold(a.ArtistName, artist.Name) && strings.EqualFold(a.Title, album.Title) {
					matched = a
					break
				}
			}
			// Fallback: use first result if title matches but artist differs slightly.
			if matched == nil {
				for i := range albums {
					a := &albums[i]
					if strings.EqualFold(a.Title, album.Title) {
						matched = a
						break
					}
				}
			}
			if matched == nil {
				continue
			}

			tracks, terr := p.GetAlbumTracks(ctx, matched.ProviderID)
			if terr != nil || len(tracks) == 0 {
				continue
			}

			discoveryTracks = tracks
			cachedProvider = matched.ProviderName
			cachedAlbumID = matched.ProviderID

			// Persist to cache.
			if dbp, ok := s.store.(sqlDBProvider); ok {
				tracksJSON, jerr := json.Marshal(tracks)
				if jerr == nil {
					_, err := dbp.DB().ExecContext(ctx,
						`INSERT OR REPLACE INTO album_discovery_cache (album_id, provider_name, provider_album_id, tracks_json, cached_at)
						 VALUES (?, ?, ?, ?, ?)`,
						albumID, matched.ProviderName, matched.ProviderID, string(tracksJSON),
						time.Now().UTC().Format(time.RFC3339),
					)
					if err != nil {
						s.log.Warn("album discovery cache write failed", "album_id", albumID, "error", err, "component", "api")
					}
				} else {
					s.log.Warn("album discovery cache marshal failed", "album_id", albumID, "error", jerr, "component", "api")
				}
			}
			break
		}
	}

	if discoveryTracks == nil {
		// No discovery data — return just library tracks as "downloaded".
		libTracks, err := s.store.GetTracksByAlbum(ctx, albumID)
		if err != nil {
			s.log.Warn("get tracks by album failed", "album_id", albumID, "error", err, "component", "api")
		}
		entries := make([]discoveryTrackEntry, 0)
		for _, t := range libTracks {
			entries = append(entries, discoveryTrackEntry{
				Title:          t.Title,
				TrackNumber:    t.TrackNumber,
				DurationMs:     int64(t.Duration),
				Downloaded:     true,
				LibraryTrackID: t.ID,
				FilePath:       t.FilePath,
				FileSize:       t.FileSize,
				Bitrate:        t.Bitrate,
				Format:         formatFromPath(t.FilePath),
			})
		}
		writeJSON(w, http.StatusOK, albumDiscoveryResponse{Tracks: entries})
		return
	}

	// Build library track index for merge.
	libTracks, err := s.store.GetTracksByAlbum(ctx, albumID)
	if err != nil {
		s.log.Warn("get tracks by album failed", "album_id", albumID, "error", err, "component", "api")
	}
	byTitle := make(map[string]*domain.Track, len(libTracks))
	byISRC := make(map[string]*domain.Track, len(libTracks))
	for i := range libTracks {
		t := &libTracks[i]
		byTitle[normalizeKey(t.Title)] = t
		if t.ISRC != "" {
			byISRC[t.ISRC] = t
		}
	}
	// Also index all artist tracks for cross-album matching (title + ISRC).
	artistTracks, err := s.store.GetTracksByArtist(ctx, album.ArtistID)
	if err != nil {
		s.log.Warn("get tracks by artist failed", "artist_id", album.ArtistID, "error", err, "component", "api")
	}
	for i := range artistTracks {
		t := &artistTracks[i]
		// Only add to title index if not already present (album tracks take precedence).
		titleKey := normalizeKey(t.Title)
		if _, exists := byTitle[titleKey]; !exists {
			byTitle[titleKey] = t
		}
		if t.ISRC != "" {
			if _, exists := byISRC[t.ISRC]; !exists {
				byISRC[t.ISRC] = t
			}
		}
	}

	// Merge discovery tracks with library status.
	var entries []discoveryTrackEntry
	for _, dt := range discoveryTracks {
		entry := discoveryTrackEntry{
			Title:       dt.Title,
			TrackNumber: dt.TrackNumber,
			DurationMs:  dt.DurationMs,
		}

		// Match by ISRC first (most reliable).
		if libTrack, ok := byISRC[dt.ISRC]; ok && dt.ISRC != "" {
			entry.Downloaded = true
			entry.LibraryTrackID = libTrack.ID
			entry.FilePath = libTrack.FilePath
			entry.FileSize = libTrack.FileSize
			entry.Bitrate = libTrack.Bitrate
			entry.Format = formatFromPath(libTrack.FilePath)
		} else if libTrack, ok := byTitle[normalizeKey(dt.Title)]; ok {
			// Fallback to normalized title matching.
			entry.Downloaded = true
			entry.LibraryTrackID = libTrack.ID
			entry.FilePath = libTrack.FilePath
			entry.FileSize = libTrack.FileSize
			entry.Bitrate = libTrack.Bitrate
			entry.Format = formatFromPath(libTrack.FilePath)
		}

		entries = append(entries, entry)
	}

	resp := albumDiscoveryResponse{
		Provider:        cachedProvider,
		ProviderAlbumID: cachedAlbumID,
		Tracks:          entries,
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLibraryAlbumDownloadMissing queues all undownloaded tracks for an album
// as pending downloads. Source resolution happens in the background via the
// monitor's resolvePendingSources — same pattern as playlist download-missing.
func (s *Server) handleLibraryAlbumDownloadMissing(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()

	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get album: %w", err))
		return
	}
	if album == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("get artist: %w", err))
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	// Get discovery tracks to find which ones are missing.
	discovery, err := getLibraryAlbumDiscoveryData(ctx, s, albumID, artist.Name, album.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to load discovery data: %w", err))
		return
	}

	queued := 0
	var errors []string
	for _, dt := range discovery {
		if dt.Downloaded {
			continue
		}
		_, err := s.downloadSvc.QueuePending(ctx, download.Meta{
			Artist:      artist.Name,
			Album:       album.Title,
			Title:       dt.Title,
			TrackNumber: dt.TrackNumber,
		})
		if err != nil {
			s.log.Warn("queue pending failed", "artist", artist.Name, "title", dt.Title, "error", err, "component", "api")
			errors = append(errors, fmt.Sprintf("%s: %v", dt.Title, err))
			continue
		}
		queued++
	}

	resp := map[string]any{"queued": queued}
	if len(errors) > 0 {
		resp["errors"] = errors
	}
	writeJSON(w, http.StatusOK, resp)
}

// discoveryTrackData is the internal representation used by handleLibraryAlbumDownloadMissing.
type discoveryTrackData struct {
	Title       string
	TrackNumber int
	Downloaded  bool
}

// getLibraryAlbumDiscoveryData returns discovery tracks for an album, reusing
// the same logic as handleLibraryAlbumDiscovery but returning a simple struct.
func getLibraryAlbumDiscoveryData(ctx context.Context, s *Server, albumID int64, artistName, albumTitle string) ([]discoveryTrackData, error) {
	// Try cache first.
	if dbp, ok := s.store.(sqlDBProvider); ok {
		var tracksJSON, cachedAt string
		err := dbp.DB().QueryRowContext(ctx,
			`SELECT tracks_json, cached_at FROM album_discovery_cache WHERE album_id = ?`, albumID,
		).Scan(&tracksJSON, &cachedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("album discovery cache read failed", "album_id", albumID, "error", err, "component", "api")
		}
		if err == nil && tracksJSON != "" {
			// Check 24h TTL.
			if t, parseErr := time.Parse(time.RFC3339, cachedAt); parseErr == nil {
				if time.Since(t) > 24*time.Hour {
					tracksJSON = "" // expired
				}
			}
		}
		if tracksJSON != "" {
			var cached []struct {
				Title       string `json:"title"`
				TrackNumber int    `json:"track_number"`
			}
			if json.Unmarshal([]byte(tracksJSON), &cached) == nil {
				// Get library tracks for merge.
				libTracks, _ := s.store.GetTracksByAlbum(ctx, albumID)
				byTitle := make(map[string]bool, len(libTracks))
				for _, t := range libTracks {
					byTitle[normalizeKey(t.Title)] = true
				}
				var out []discoveryTrackData
				for _, c := range cached {
					out = append(out, discoveryTrackData{
						Title:       c.Title,
						TrackNumber: c.TrackNumber,
						Downloaded:  byTitle[normalizeKey(c.Title)],
					})
				}
				return out, nil
			}
		}
	}

	// Cache miss — do the full discovery.
	if s.discoveryReg == nil {
		return nil, fmt.Errorf("no discovery providers available")
	}
	providers := s.discoveryReg.Any()
	query := artistName + " " + albumTitle
	for _, p := range providers {
		albums, serr := p.SearchAlbums(ctx, query, 5)
		if serr != nil || len(albums) == 0 {
			continue
		}
		var matched *discovery.AlbumResult
		for i := range albums {
			a := &albums[i]
			if strings.EqualFold(a.ArtistName, artistName) && strings.EqualFold(a.Title, albumTitle) {
				matched = a
				break
			}
		}
		if matched == nil {
			// Fallback: title-only match when artist name differs slightly.
			for i := range albums {
				a := &albums[i]
				if strings.EqualFold(a.Title, albumTitle) {
					matched = a
					break
				}
			}
		}
		if matched == nil {
			continue
		}
		tracks, terr := p.GetAlbumTracks(ctx, matched.ProviderID)
		if terr != nil || len(tracks) == 0 {
			continue
		}
		libTracks, _ := s.store.GetTracksByAlbum(ctx, albumID)
		byTitle := make(map[string]bool, len(libTracks))
		for _, t := range libTracks {
			byTitle[normalizeKey(t.Title)] = true
		}
		var out []discoveryTrackData
		for _, t := range tracks {
			out = append(out, discoveryTrackData{
				Title:       t.Title,
				TrackNumber: t.TrackNumber,
				Downloaded:  byTitle[normalizeKey(t.Title)],
			})
		}

		// Persist to cache so future calls hit cache.
		if dbp, ok := s.store.(sqlDBProvider); ok {
			tracksJSON, jerr := json.Marshal(tracks)
			if jerr == nil {
				_, _ = dbp.DB().ExecContext(ctx,
					`INSERT OR REPLACE INTO album_discovery_cache (album_id, provider_name, provider_album_id, tracks_json, cached_at)
					 VALUES (?, ?, ?, ?, ?)`,
					albumID, matched.ProviderName, matched.ProviderID, string(tracksJSON),
					time.Now().UTC().Format(time.RFC3339),
				)
			}
		}

		return out, nil
	}
	return nil, fmt.Errorf("no discovery data found for album %d", albumID)
}

func (s *Server) handleLibraryScan(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()

	// Scan the library root only — download path is staging, not scanned.
	paths := []string{cfg.Library.LibraryPath}
	if paths[0] == "" {
		paths[0] = "./music"
	}

	ctx := r.Context()
	var agg library.ScanStats
	for _, p := range paths {
		stats, err := s.scanner.ScanPath(ctx, p)
		if err != nil {
			s.log.Error("scan error", "path", p, "error", err, "component", "scanner")
			agg.Errors++
			continue
		}
		agg.Imported += stats.Imported
		agg.Scanned += stats.Scanned
		agg.Skipped += stats.Skipped
		agg.Errors += stats.Errors
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scanned":  agg.Scanned,
		"imported": agg.Imported,
		"skipped":  agg.Skipped,
		"errors":   agg.Errors,
		"paths":    paths,
	})
}

func (s *Server) handleLibraryArtist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	artist, err := s.store.GetArtist(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}
	if artist.ThumbURL == "artist.jpg" {
		artist.ThumbURL = fmt.Sprintf("/api/artist-image/%d", artist.ID)
	}
	writeJSON(w, http.StatusOK, artist)
}

func (s *Server) handleLibraryArtistAlbums(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	albums, err := s.store.GetAlbumsByArtist(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if albums == nil {
		albums = []domain.Album{}
	}
	writeJSON(w, http.StatusOK, albums)
}

func (s *Server) handleLibraryArtistTracks(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("artistID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}
	tracks, err := s.store.GetTracksByArtist(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if tracks == nil {
		tracks = []domain.Track{}
	}
	writeJSON(w, http.StatusOK, tracks)
}

// handleCoverArt serves the cover.jpg image for an album.
func (s *Server) handleCoverArt(w http.ResponseWriter, r *http.Request) {
	albumIDStr := r.PathValue("albumID")
	albumID, err := strconv.ParseInt(albumIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid album ID"))
		return
	}

	ctx := r.Context()
	album, err := s.store.GetAlbum(ctx, albumID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("album not found"))
		return
	}

	artist, err := s.store.GetArtist(ctx, album.ArtistID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	// Try to find the album directory from existing tracks.
	cfg := s.cfg.Get()
	var albumDir string
	tracks, _ := s.store.GetTracksByAlbum(ctx, albumID)
	if len(tracks) > 0 && tracks[0].FilePath != "" {
		albumDir = filepath.Dir(tracks[0].FilePath)
	} else {
		// Fall back to constructing the path from the folder template.
		resolver := library.NewPathResolver(cfg.Library.FolderTemplate)
		resolved := resolver.Resolve(library.ResolveArgs{
			Artist:    artist.Name,
			Album:     album.Title,
			Year:      album.Year,
			TrackNum:  1,
			Title:     "dummy",
			Ext:       "mp3",
			AlbumType: "Album",
		})
		if resolved == "" {
			writeError(w, http.StatusNotFound, fmt.Errorf("cannot resolve album path"))
			return
		}
		albumDir = filepath.Join(cfg.Library.LibraryPath, resolved)
	}

	// Defense-in-depth: ensure resolved path stays within library root.
	if cleanDir, cleanRoot := filepath.Clean(albumDir), filepath.Clean(cfg.Library.LibraryPath); !strings.HasPrefix(cleanDir, cleanRoot+string(os.PathSeparator)) && cleanDir != cleanRoot {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	coverPath := filepath.Join(albumDir, "cover.jpg")
	f, err := os.Open(coverPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Errorf("cover not found"))
		} else {
			s.log.Error("cover open failed", "path", coverPath, "error", err, "component", "api")
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.log.Error("cover stat failed", "path", coverPath, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	http.ServeContent(w, r, "cover.jpg", fi.ModTime(), f)
}

// handleArtistImage serves the artist.jpg image from the artist's library directory.
func (s *Server) handleArtistImage(w http.ResponseWriter, r *http.Request) {
	artistIDStr := r.PathValue("artistID")
	artistID, err := strconv.ParseInt(artistIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid artist ID"))
		return
	}

	ctx := r.Context()
	artist, err := s.store.GetArtist(ctx, artistID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if artist == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("artist not found"))
		return
	}

	cfg := s.cfg.Get()
	var artistDir string

	// Try to find the artist directory from existing tracks.
	tracks, _ := s.store.GetTracksByArtist(ctx, artistID)
	if len(tracks) > 0 && tracks[0].FilePath != "" {
		// Tracks are stored as {artist}/{album}/track.ext, so parent-of-parent = artist dir.
		artistDir = filepath.Dir(filepath.Dir(tracks[0].FilePath))
	} else {
		// Fallback: construct from library root + artist name.
		artistDir = filepath.Join(cfg.Library.LibraryPath, artist.Name)
	}

	// Defense-in-depth: ensure resolved path stays within library root.
	if cleanDir, cleanRoot := filepath.Clean(artistDir), filepath.Clean(cfg.Library.LibraryPath); !strings.HasPrefix(cleanDir, cleanRoot+string(os.PathSeparator)) && cleanDir != cleanRoot {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	imagePath := filepath.Join(artistDir, "artist.jpg")
	f, err := os.Open(imagePath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, fmt.Errorf("artist image not found"))
		} else {
			s.log.Error("artist image open failed", "path", imagePath, "error", err, "component", "api")
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.log.Error("artist image stat failed", "path", imagePath, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	http.ServeContent(w, r, "artist.jpg", fi.ModTime(), f)
}
