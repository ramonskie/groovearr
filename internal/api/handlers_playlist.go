package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ramonskie/groovearr/internal/domain"
	"github.com/ramonskie/groovearr/internal/playlist"
)

// ─── Playlist handlers ────────────────────────────────────────────────

func (s *Server) handlePlaylistSources(w http.ResponseWriter, r *http.Request) {
	if s.playlistSvc == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	srcs := s.playlistSvc.Sources()
	var out []map[string]string
	for _, src := range srcs {
		out = append(out, map[string]string{
			"name":    src.Name(),
			"display": src.DisplayName(),
		})
	}
	if out == nil {
		out = []map[string]string{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePlaylistSourceBrowse(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	if s.playlistSvc == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	ctx := r.Context()
	items, err := s.playlistSvc.BrowseSource(ctx, source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if items == nil {
		items = []playlist.SourcePlaylistItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleListPlaylists(w http.ResponseWriter, r *http.Request) {
	if s.playlistSvc == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	ctx := r.Context()
	playlists, err := s.store.ListPlaylists(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if playlists == nil {
		playlists = []domain.Playlist{}
	}
	writeJSON(w, http.StatusOK, playlists)
}

func (s *Server) handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	ctx := r.Context()
	p, err := s.store.GetPlaylist(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("playlist not found"))
		return
	}
	tracks, _ := s.store.GetPlaylistTracks(ctx, id)
	if tracks == nil {
		tracks = []domain.PlaylistTrack{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"playlist": p,
		"tracks":   tracks,
	})
}

func (s *Server) handleImportPlaylist(w http.ResponseWriter, r *http.Request) {
	if s.playlistSvc == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("playlist service not available"))
		return
	}
	var req struct {
		Source     string `json:"source"`
		PlaylistID string `json:"playlist_id"`
		SyncMode   string `json:"sync_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Source == "" || req.PlaylistID == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("source and playlist_id required"))
		return
	}
	if req.SyncMode != "" && req.SyncMode != "mirror" && req.SyncMode != "append" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("sync_mode must be 'mirror' or 'append'"))
		return
	}

	result, err := s.playlistSvc.ImportPlaylist(r.Context(), req.Source, req.PlaylistID, req.SyncMode)
	if err != nil {
		s.log.Error("playlist import failed", "source", req.Source, "playlist_id", req.PlaylistID, "error", err, "component", "api")
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"playlist":  result.Playlist,
		"tracks":    result.Tracks,
		"linked":    result.Linked,
		"unmatched": result.Unmatched,
	})
}

func (s *Server) handleDownloadMissing(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	if s.playlistSvc == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("playlist service not available"))
		return
	}
	queued, err := s.playlistSvc.DownloadMissing(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued})
}

func (s *Server) handleSyncPlaylist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	if s.playlistSvc == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("playlist service not available"))
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.bgCtx, 10*time.Minute)
		defer cancel()
		if err := s.playlistSvc.SyncPlaylist(ctx, id); err != nil {
			s.log.Error("playlist sync failed", "playlist_id", id, "error", err, "component", "api")
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
}

func (s *Server) handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	var req struct {
		AutoSync *bool   `json:"auto_sync"`
		SyncMode *string `json:"sync_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.SyncMode != nil && *req.SyncMode != "mirror" && *req.SyncMode != "append" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("sync_mode must be 'mirror' or 'append'"))
		return
	}

	ctx := r.Context()
	p, err := s.store.GetPlaylist(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("playlist not found"))
		return
	}
	if req.AutoSync != nil {
		p.AutoSync = *req.AutoSync
	}
	if req.SyncMode != nil {
		p.SyncMode = domain.SyncMode(*req.SyncMode)
	}
	// Skip upsert if nothing changed — avoids needless UpdatedAt bump.
	if req.AutoSync == nil && req.SyncMode == nil {
		writeJSON(w, http.StatusOK, p)
		return
	}
	if _, err := s.store.UpsertPlaylist(ctx, p); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Re-read to get fresh UpdatedAt set by the store.
	if updated, err := s.store.GetPlaylist(ctx, id); err == nil && updated != nil {
		p = updated
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid playlist ID"))
		return
	}
	ctx := r.Context()
	if err := s.store.DeletePlaylist(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
