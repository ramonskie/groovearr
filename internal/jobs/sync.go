package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// syncTimeout bounds a playlist sync so it can't hold the single job slot
// forever. Mirrors the background-sync timeout in playlist.Service.
const syncTimeout = 15 * time.Minute

// SyncPlaylist returns a Runner that syncs a single imported playlist with
// its upstream source. Runs through the job Manager so it is single-flight,
// cancellable, SSE-broadcast, and persisted/restored like every other
// background job. The sync runs under the service's per-playlist lock so it
// can't race the auto-sync worker or the download-missing rebuild.
func (r *Runners) SyncPlaylist(playlistID int64) Runner {
	return func(ctx context.Context, report func(Report)) error {
		if r.deps.Playlist == nil {
			return errors.New("playlist service not available")
		}
		ctx, cancel := context.WithTimeout(ctx, syncTimeout)
		defer cancel()
		report(Report{Message: fmt.Sprintf("syncing playlist %d", playlistID)})
		started, err := r.deps.Playlist.SyncPlaylistGuarded(ctx, playlistID)
		if err != nil {
			return err
		}
		if !started {
			return errors.New("sync already in progress for this playlist")
		}
		return nil
	}
}
