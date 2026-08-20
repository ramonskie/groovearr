// Package library provides file scanning and path resolution.
package library

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ramonskie/groovearr/internal/domain"
)

// OrganizeResult reports the outcome of organizing a single track.
type OrganizeResult struct {
	TrackID   int64
	From      string // original file path
	To        string // template path (empty when the track is in place)
	Moved     bool   // the file (and any images) were moved
	WouldMove bool   // dry-run: the file is out of place and would move
	Skipped   string // reason when the file was not moved
}

// Organizer moves library tracks into the folder-template layout and keeps the
// store's file paths in sync. Compilation tracks (VA albums) are routed through
// the compilation template (see Renamer), so they stay under "Various Artists/"
// rather than being scattered into each performer's folder. Dry-run mode only
// reports what would move.
type Organizer struct {
	renamer *Renamer
	root    string
	store   Store
	log     *slog.Logger
}

// NewOrganizer creates an Organizer for the given folder and compilation
// templates, library root, and store. An empty compilation template falls back
// to the folder template.
func NewOrganizer(folderTemplate, compilationTemplate, root string, store Store, logger *slog.Logger) *Organizer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Organizer{
		renamer: NewRenamerWithCompilation(folderTemplate, compilationTemplate, root, logger),
		root:    root,
		store:   store,
		log:     logger,
	}
}

// IsCompilationDir reports whether a directory is a compilation grouping
// (e.g. "Various Artists") rather than a single artist's folder. Tracks under
// such a directory belong to a VA album even when the DB AlbumType says
// otherwise. Shared with the HTTP layer so image serving applies the same rule.
// The "various..." prefix is a strong signal for directories.
func IsCompilationDir(dir string) bool {
	return isCompilationName(filepath.Base(dir), true)
}

// IsCompilationArtist reports whether an artist name represents a compilation
// grouping ("Various Artists", "VA", ...) rather than a real performer. Such
// artists are shown with their placeholder avatar instead of a portrait. Only
// exact grouping names count — a real performer named "Various Grooves" is
// not a grouping.
func IsCompilationArtist(name string) bool {
	return isCompilationName(name, false)
}

// isCompilationName matches a folder or artist name against the known
// compilation-grouping names. allowVariousPrefix additionally treats any name
// starting with "various" as a grouping (safe for directories, too aggressive
// for stored artist names).
func isCompilationName(base string, allowVariousPrefix bool) bool {
	b := strings.ToLower(base)
	switch b {
	case "va", "compilation", "compilations", "various artists":
		return true
	}
	return allowVariousPrefix && strings.HasPrefix(b, "various")
}

// Organize computes the folder-template path for track and, unless dryRun,
// moves the file (plus any cover/artist images) there and updates the stored
// file path. Artist/album names come from the store (the template's authority).
// albumType selects the compilation template when the album is a VA
// compilation; a track currently sitting under a compilation grouping folder
// (e.g. "Various Artists") is treated as one regardless.
func (o *Organizer) Organize(ctx context.Context, track *domain.Track, artist, album string, year int, albumType string, dryRun bool) (OrganizeResult, error) {
	res := OrganizeResult{TrackID: track.ID, From: track.FilePath}

	if artist == "" || album == "" || track.Title == "" {
		res.Skipped = "missing metadata"
		return res, nil
	}
	if track.FilePath == "" {
		res.Skipped = "no file path"
		return res, nil
	}

	renamer := o.renamer
	compilation := strings.EqualFold(albumType, string(domain.AlbumTypeCompilation)) || IsCompilationDir(ArtistDirFromTrack(track.FilePath))

	meta := FileMeta{
		Artist:   artist,
		Album:    album,
		Title:    track.Title,
		TrackNum: track.TrackNumber,
		DiscNum:  track.DiscNumber,
		Year:     year,
	}
	target := renamer.target(track.FilePath, meta, compilation)
	if target == "" {
		res.Skipped = "cannot resolve"
		return res, nil
	}
	if filepath.Clean(target) == filepath.Clean(track.FilePath) {
		res.Skipped = "in place"
		return res, nil
	}
	// Only reorganize files inside the library root.
	if !pathWithinRoot(track.FilePath, o.root) {
		res.Skipped = "outside library"
		return res, nil
	}

	res.To = target
	// Never clobber an existing file (e.g. two tracks resolving to the same
	// target). os.Rename overwrites silently on Unix. Read-only, so it applies
	// in dry-run too: a real run would skip these, not move them.
	if _, err := os.Stat(target); err == nil {
		res.Skipped = "target exists"
		return res, nil
	}

	res.WouldMove = true
	if dryRun {
		return res, nil
	}

	newPath, err := renamer.RenameFor(track.FilePath, meta, compilation)
	if err != nil {
		return res, err
	}
	if newPath == track.FilePath {
		res.Skipped = "move skipped"
		return res, nil
	}

	movedImages := o.moveImages(track.FilePath, newPath)

	oldPath := track.FilePath
	track.FilePath = newPath
	if _, err := o.store.UpsertTrack(ctx, track); err != nil {
		// Roll the file and any relocated images back so disk and DB stay
		// consistent. Otherwise the next organize run sees the target occupied
		// ("target exists") and never fixes the path, and a scan re-imports the
		// moved file as a duplicate track.
		track.FilePath = oldPath
		o.moveFileBack(newPath, oldPath)
		o.moveImagesBack(movedImages)
		return res, fmt.Errorf("update track %d path: %w", track.ID, err)
	}

	res.Moved = true
	res.Skipped = ""
	return res, nil
}

// moveFileBack moves a file back to its original location after a failed DB
// update. Best-effort: a rollback that fails leaves the file at the target
// path, but the error is already surfaced by the caller.
func (o *Organizer) moveFileBack(newPath, oldPath string) {
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		o.log.Warn("organize: rollback mkdir failed", "old", oldPath, "error", err, "component", "organizer")
		return
	}
	if err := os.Rename(newPath, oldPath); err != nil {
		if strings.Contains(err.Error(), "cross-device") {
			if copyErr := o.renamer.copyFile(newPath, oldPath); copyErr != nil {
				o.log.Warn("organize: rollback copy failed", "new", newPath, "old", oldPath, "error", copyErr, "component", "organizer")
				return
			}
			os.Remove(newPath)
		} else {
			o.log.Warn("organize: rollback move failed", "new", newPath, "old", oldPath, "error", err, "component", "organizer")
		}
	}
}

// imageMove records a single relocated image so a failed DB update can roll it
// back.
type imageMove struct{ src, dst string }

// moveImages relocates the album's cover and the artist's portrait to the
// track's new location, removes now-empty source directories, and returns what
// moved so the caller can undo it on rollback. Best-effort: failures are logged
// and never fail the organize.
func (o *Organizer) moveImages(oldPath, newPath string) []imageMove {
	oldAlbum, newAlbum := AlbumDirFromTrack(oldPath), AlbumDirFromTrack(newPath)
	oldArtist, newArtist := ArtistDirFromTrack(oldPath), ArtistDirFromTrack(newPath)
	rootClean := filepath.Clean(o.root)
	var moved []imageMove

	if filepath.Clean(oldAlbum) != rootClean && filepath.Clean(oldAlbum) != filepath.Clean(newAlbum) {
		for _, name := range CoverCandidates {
			if m, ok := o.moveFileIfPresent(filepath.Join(oldAlbum, name), filepath.Join(newAlbum, name)); ok {
				moved = append(moved, m)
			}
		}
	}
	// Artist portraits live one level up. Never move one into or out of a
	// compilation grouping — a flat-layout track would otherwise drag a stray
	// root artist.jpg into an unrelated artist folder, and a compilation-typed
	// album under a performer would strip the performer's portrait into the
	// grouping folder (which never displays one).
	if filepath.Clean(oldArtist) != rootClean && filepath.Clean(oldArtist) != filepath.Clean(newArtist) &&
		!IsCompilationDir(oldArtist) && !IsCompilationDir(newArtist) {
		for _, name := range ArtistImageNames {
			if m, ok := o.moveFileIfPresent(filepath.Join(oldArtist, name), filepath.Join(newArtist, name)); ok {
				moved = append(moved, m)
			}
		}
	}

	// Best-effort cleanup of now-empty source directories.
	_ = os.Remove(oldAlbum)
	_ = os.Remove(oldArtist)
	return moved
}

// moveImagesBack restores images moved by moveImages, in reverse order.
func (o *Organizer) moveImagesBack(moved []imageMove) {
	for i := len(moved) - 1; i >= 0; i-- {
		o.moveFileIfPresent(moved[i].dst, moved[i].src)
	}
}

// moveFileIfPresent moves src to dst when src exists and dst does not, with a
// cross-device copy+delete fallback. Returns the move when it happened.
func (o *Organizer) moveFileIfPresent(src, dst string) (imageMove, bool) {
	if _, err := os.Stat(src); err != nil {
		return imageMove{}, false
	}
	if _, err := os.Stat(dst); err == nil {
		return imageMove{}, false // never clobber an existing image
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		o.log.Warn("organize: mkdir failed", "dst", dst, "error", err, "component", "organizer")
		return imageMove{}, false
	}
	if err := os.Rename(src, dst); err != nil {
		if strings.Contains(err.Error(), "cross-device") {
			if copyErr := o.renamer.copyFile(src, dst); copyErr != nil {
				o.log.Warn("organize: copy image failed", "src", src, "dst", dst, "error", copyErr, "component", "organizer")
				return imageMove{}, false
			}
			os.Remove(src)
		} else {
			o.log.Warn("organize: move image failed", "src", src, "dst", dst, "error", err, "component", "organizer")
			return imageMove{}, false
		}
	}
	return imageMove{src: src, dst: dst}, true
}

// pathWithinRoot reports whether p is inside (or equal to) root.
func pathWithinRoot(p, root string) bool {
	cleanRoot := filepath.Clean(root)
	clean := filepath.Clean(p)
	return clean == cleanRoot || strings.HasPrefix(clean, cleanRoot+string(os.PathSeparator))
}
