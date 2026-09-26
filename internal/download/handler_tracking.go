package download

import (
	"context"
	"log/slog"
)

// TrackingLinker promotes a matching tracked album to "downloaded". It is
// declared here (dependency inversion, mirroring the playlist linker) so the
// download import chain does not depend on internal/tracking; *tracking.Service
// satisfies it structurally.
type TrackingLinker interface {
	LinkImportedAlbum(ctx context.Context, artistName, albumTitle string) error
}

// TrackingLinkHandler is a post-import step that promotes a tracked album to
// "downloaded" as soon as its download is imported, instead of waiting for the
// next reconcile/refresh. It runs inside the standard import chain, so it
// covers both single-track downloads and per-track records produced by the
// album import handler.
type TrackingLinkHandler struct {
	log    *slog.Logger
	linker TrackingLinker
}

// NewTrackingLinkHandler creates a post-import handler that links a completed
// import back onto artist tracking. linker may be nil (tracking disabled), in
// which case the handler is a no-op.
func NewTrackingLinkHandler(linker TrackingLinker, logger *slog.Logger) *TrackingLinkHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &TrackingLinkHandler{log: logger, linker: linker}
}

// Handle promotes the tracked album matching record.Artist|record.Album to
// "downloaded". It is best-effort by design: tracking is a side ledger that
// must never fail an import (unlike PlaylistLinkerHandler, which participates
// in import success). A missing linker, missing artist/album metadata, or a
// LinkImportedAlbum error is logged and swallowed; Handle always returns nil.
func (h *TrackingLinkHandler) Handle(ctx context.Context, record *Record) error {
	if h.linker == nil {
		h.log.Debug("skipped - tracking linker not configured",
			"download_id", record.ID, "component", "tracking_linker")
		return nil
	}
	if record.Artist == "" || record.Album == "" {
		h.log.Debug("skipped - missing artist or album",
			"download_id", record.ID, "component", "tracking_linker")
		return nil
	}
	if err := h.linker.LinkImportedAlbum(ctx, record.Artist, record.Album); err != nil {
		// Best-effort: tracking is a side ledger, so the import must not fail
		// because tracking could not be updated. The next reconcile/refresh
		// will promote the album anyway.
		h.log.Warn("tracking link failed (best-effort, import unaffected)",
			"download_id", record.ID,
			"artist", record.Artist,
			"album", record.Album,
			"error", err,
			"component", "tracking_linker")
	}
	return nil
}
