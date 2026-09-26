package download

import (
	"context"
	"fmt"
)

// QueueMode describes which album-acquisition path produced a result.
type QueueMode string

const (
	QueueModeAlbum  QueueMode = "album" // whole-album download queued
	QueueModeTracks QueueMode = "track" // per-track fallback queued
)

// TrackQueue carries one track's metadata for the per-track fallback.
type TrackQueue struct {
	Artist      string
	Album       string
	Title       string
	TrackNumber int
	DiscNumber  int
	ISRC        string
}

// AlbumQueueResult reports the outcome of QueueAlbumWithFallback.
//
// DownloadID is set only when Mode == QueueModeAlbum and the queue succeeded.
// Total is the number of tracks considered by the per-track path (0 in album
// mode). Errors holds per-track queue failures and never aborts the loop.
type AlbumQueueResult struct {
	Mode       QueueMode
	DownloadID string
	Queued     int
	Total      int
	Errors     []string
}

// QueueAlbumWithFallback is THE canonical album-acquisition policy:
// whole-album first when an album source is configured and a download client
// is available, otherwise per-track fill. Callers must not re-implement it.
//
// Album-first requires ALL of: non-empty artist, non-empty album, at least one
// configured album source, a non-empty download client, and a wired album
// searcher. If any is missing the per-track path is used directly.
//
// A search error or zero releases falls through to per-track (the search error
// is logged, never returned). A QueueAlbum failure is a real error and is
// returned wrapped, with Mode still QueueModeAlbum, so callers can surface a
// 500 — matching the discover handler's existing behavior. Dedup/idempotency
// is whatever QueueAlbum/QueuePending already do; no new dedup is added here.
func (s *Service) QueueAlbumWithFallback(ctx context.Context, artist, album string, tracks []TrackQueue, downloadClient string, albumSources []string) (AlbumQueueResult, error) {
	if s.albumFirstEligible(artist, album, downloadClient, albumSources) {
		res, handled, err := s.tryAlbumQueue(ctx, artist, album, downloadClient)
		if err != nil {
			return res, err
		}
		if handled {
			return res, nil
		}
	}
	return s.queueTrackFallback(ctx, tracks), nil
}

// albumFirstEligible reports whether the whole-album leg can be attempted.
func (s *Service) albumFirstEligible(artist, album, downloadClient string, albumSources []string) bool {
	if artist == "" || album == "" || downloadClient == "" || len(albumSources) == 0 {
		return false
	}
	return s.albumSearcherSnapshot() != nil
}

// tryAlbumQueue runs the whole-album leg. handled is false when the caller
// should fall through to per-track (search error or no releases); err is
// non-nil only for a genuine QueueAlbum failure.
func (s *Service) tryAlbumQueue(ctx context.Context, artist, album, downloadClient string) (AlbumQueueResult, bool, error) {
	searcher := s.albumSearcherSnapshot()
	releases, searchErr := searcher.SearchAlbums(ctx, artist+" "+album)
	if searchErr != nil {
		s.log.Warn("album search failed, falling back to per-track",
			"artist", artist, "album", album, "error", searchErr, "component", "download")
		return AlbumQueueResult{}, false, nil
	}
	if len(releases) == 0 {
		s.log.Info("no album releases found, falling back to per-track",
			"artist", artist, "album", album, "component", "download")
		return AlbumQueueResult{}, false, nil
	}

	release := releases[0]
	id, queueErr := s.QueueAlbum(ctx, release, nil, downloadClient)
	if queueErr != nil {
		// Real failure — surface it (wrapped) so callers can 500. By contrast,
		// the search error above falls through to per-track.
		return AlbumQueueResult{Mode: QueueModeAlbum}, true, fmt.Errorf("queue album: %w", queueErr)
	}

	s.log.Info("album download queued via album source",
		"download_id", id, "artist", release.Artist, "album", release.Album, "component", "download")
	return AlbumQueueResult{Mode: QueueModeAlbum, DownloadID: id, Queued: 1}, true, nil
}

// queueTrackFallback queues one pending download per eligible track. Tracks
// missing an artist or title are skipped; a per-track failure is collected in
// Errors and never aborts the loop.
func (s *Service) queueTrackFallback(ctx context.Context, tracks []TrackQueue) AlbumQueueResult {
	res := AlbumQueueResult{Mode: QueueModeTracks, Total: len(tracks)}
	for _, t := range tracks {
		if t.Artist == "" || t.Title == "" {
			continue
		}
		_, err := s.QueuePending(ctx, Meta{
			Artist:      t.Artist,
			Album:       t.Album,
			Title:       t.Title,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.DiscNumber,
			ISRC:        t.ISRC,
		})
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s - %s: queue: %v", t.Artist, t.Title, err))
			continue
		}
		res.Queued++
	}
	return res
}
