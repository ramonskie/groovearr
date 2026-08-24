package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/ramonskie/groovearr/internal/config"
	"github.com/ramonskie/groovearr/internal/library"
)

// Organize moves library tracks into the folder-template layout. When dryRun
// is true it only counts what would move. Sequential: tracks of the same album
// move into the same target dirs, so parallel workers could race on the
// filesystem.
func (r *Runners) Organize(dryRun bool) Runner {
	return func(ctx context.Context, report func(Report)) error {
		cfg := r.deps.Config()
		root := cfg.Library.LibraryPath
		if root == "" {
			root = config.DefaultLibraryPath
		}
		org := library.NewOrganizer(cfg.Library.FolderTemplate, cfg.Library.CompilationTemplate, root, r.deps.Store, r.deps.Log)

		// Heal a killed organize's moved-but-DB-stale paths before re-running,
		// so tracks orphaned by the kill aren't re-imported as duplicates and
		// don't show up as "target exists" skips below. Repair mutates the DB,
		// so it runs only in repair mode, never dry-run.
		if !dryRun && r.OrganizeDivergencePossible() {
			if n, err := r.reconcileDivergedPaths(ctx, report, cfg); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.deps.Log.Warn("reconcile diverged paths failed", "error", err, "component", "jobs")
			} else if n > 0 {
				report(Report{Message: fmt.Sprintf("Reconciled %d moved track paths", n)})
			}
		}

		// Pre-count for progress estimation.
		tracks, err := r.deps.Store.ListTracksWithQuality(ctx)
		if err != nil {
			return err
		}
		total := len(tracks)
		if total == 0 {
			// Persist an empty report so the UI doesn't keep showing the
			// previous run's stale result.
			rep := &OrganizeReport{Mode: "repair", RanAt: time.Now().UTC()}
			if dryRun {
				rep.Mode = "dry run"
			}
			r.setOrganizeReport(rep)
			return nil
		}

		var (
			done, moved, wouldMove, inPlace, skipped, failed, errorEntries int
		)
		report(Report{Message: "Enumerating artists…"})

		rep := &OrganizeReport{
			Mode:  "repair",
			RanAt: time.Now().UTC(),
		}
		if dryRun {
			rep.Mode = "dry run"
		}
		addEntry := func(trackID int64, from, to, reason string) {
			if len(rep.Entries) < maxOrganizeReportEntries {
				rep.Entries = append(rep.Entries, OrganizeEntry{TrackID: trackID, From: from, To: to, Reason: reason})
			} else {
				rep.Truncated = true
			}
		}
		// Persist whatever was computed, even on a mid-run failure, so the UI
		// never shows a stale previous report.
		setReport := func() {
			rep.Summary = OrganizeSummary{Moved: moved, WouldMove: wouldMove, InPlace: inPlace, Skipped: skipped, Errors: failed}
			r.setOrganizeReport(rep)
		}
		for off := 0; ; off += 200 {
			artists, err := r.deps.Store.ListArtists(ctx, off, 200)
			if err != nil {
				setReport()
				return err
			}
			if len(artists) == 0 {
				break
			}
			for _, a := range artists {
				if ctx.Err() != nil {
					setReport()
					return ctx.Err()
				}
				albums, err := r.deps.Store.GetAlbumsByArtist(ctx, a.ID)
				if err != nil {
					failed++
					r.deps.Log.Warn("organize: list albums failed", "artist_id", a.ID, "error", err, "component", "jobs")
					continue
				}
				for _, al := range albums {
					if ctx.Err() != nil {
						setReport()
						return ctx.Err()
					}
					ts, err := r.deps.Store.GetTracksByAlbum(ctx, al.ID)
					if err != nil {
						failed++
						r.deps.Log.Warn("organize: list tracks failed", "album_id", al.ID, "error", err, "component", "jobs")
						continue
					}
					for i := range ts {
						res, err := org.Organize(ctx, &ts[i], a.Name, al.Title, al.Year, string(al.AlbumType), dryRun)
						if err != nil {
							failed++
							if errorEntries < maxOrganizeErrorEntries {
								errorEntries++
								addEntry(ts[i].ID, ts[i].FilePath, err.Error(), "errors")
							}
							r.deps.Log.Warn("organize: track failed", "track_id", ts[i].ID, "error", err, "component", "jobs")
							continue
						}
						done++
						switch {
						case res.Moved:
							moved++
							addEntry(ts[i].ID, res.From, res.To, "moved")
						case res.WouldMove:
							wouldMove++
							addEntry(ts[i].ID, res.From, res.To, "would move")
						case res.Skipped == "in place":
							inPlace++
						case res.Skipped != "":
							skipped++
							addEntry(ts[i].ID, res.From, res.To, res.Skipped)
						}
						report(Report{Done: done, Total: total, Message: ts[i].Title})
					}
				}
			}
		}
		if err := ctx.Err(); err != nil {
			setReport()
			return err
		}

		setReport()

		mode := "repair"
		verb := "moved"
		if dryRun {
			mode = "dry run"
			verb = "would move"
		}
		report(Report{
			Done:    done,
			Total:   total,
			Message: fmt.Sprintf("organize (%s): %d %s, %d would-move, %d in place, %d skipped, %d errors", mode, moved, verb, wouldMove, inPlace, skipped, failed),
		})
		return nil
	}
}

// OrganizeArtist moves one artist's tracks into the configured folder layout.
// Started automatically after an artist merge: the merged tracks still sit
// under the removed artist's folders, and a canonical rename may have left the
// keeper's own folder name stale, so every keeper track is re-validated and
// moved when out of place. In-place tracks are no-ops.
func (r *Runners) OrganizeArtist(artistID int64) Runner {
	return func(ctx context.Context, report func(Report)) error {
		cfg := r.deps.Config()
		root := cfg.Library.LibraryPath
		if root == "" {
			root = config.DefaultLibraryPath
		}
		// The report is scoped to one artist; use a distinct mode so the UI
		// doesn't read the outcome as a full-library organize run.
		rep := &OrganizeReport{Mode: "repair (artist)", RanAt: time.Now().UTC()}
		var (
			done, moved, inPlace, skipped, failed, errorEntries int
		)
		addEntry := func(trackID int64, from, to, reason string) {
			if len(rep.Entries) < maxOrganizeReportEntries {
				rep.Entries = append(rep.Entries, OrganizeEntry{TrackID: trackID, From: from, To: to, Reason: reason})
			} else {
				rep.Truncated = true
			}
		}
		// Persist whatever was computed, even on a failure, so the UI never
		// shows a stale report from a previous run.
		setReport := func() {
			rep.Summary = OrganizeSummary{Moved: moved, InPlace: inPlace, Skipped: skipped, Errors: failed}
			r.setOrganizeReport(rep)
		}
		artist, err := r.deps.Store.GetArtist(ctx, artistID)
		if err != nil || artist == nil {
			setReport()
			return err
		}
		tracks, err := r.deps.Store.GetTracksByArtist(ctx, artistID)
		if err != nil {
			setReport()
			return err
		}
		total := len(tracks)
		if total == 0 {
			setReport()
			return nil
		}
		org := library.NewOrganizer(cfg.Library.FolderTemplate, cfg.Library.CompilationTemplate, root, r.deps.Store, r.deps.Log)
		for i := range tracks {
			if ctx.Err() != nil {
				setReport()
				return ctx.Err()
			}
			album, err := r.deps.Store.GetAlbum(ctx, tracks[i].AlbumID)
			if err != nil || album == nil {
				failed++
				if errorEntries < maxOrganizeErrorEntries {
					errorEntries++
					addEntry(tracks[i].ID, tracks[i].FilePath, "album lookup failed", "errors")
				}
				r.deps.Log.Warn("merge organize: album lookup failed", "track_id", tracks[i].ID, "error", err, "component", "jobs")
				done++
				report(Report{Done: done, Total: total, Message: tracks[i].Title})
				continue
			}
			res, err := org.Organize(ctx, &tracks[i], artist.Name, album.Title, album.Year, string(album.AlbumType), false)
			if err != nil {
				failed++
				if errorEntries < maxOrganizeErrorEntries {
					errorEntries++
					addEntry(tracks[i].ID, tracks[i].FilePath, err.Error(), "errors")
				}
				r.deps.Log.Warn("merge organize: track failed", "track_id", tracks[i].ID, "error", err, "component", "jobs")
			} else {
				switch {
				case res.Moved:
					moved++
					addEntry(tracks[i].ID, res.From, res.To, "moved")
				case res.Skipped == "in place":
					inPlace++
				case res.Skipped != "":
					skipped++
					addEntry(tracks[i].ID, res.From, res.To, res.Skipped)
				}
			}
			done++
			report(Report{Done: done, Total: total, Message: tracks[i].Title})
		}
		setReport()
		report(Report{
			Done:    total,
			Total:   total,
			Message: fmt.Sprintf("moved %d tracks for %s, %d in place, %d skipped, %d errors", moved, artist.Name, inPlace, skipped, failed),
		})
		return nil
	}
}
