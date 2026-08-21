// Package tagging provides shared audio metadata tag writing for MP3 (ID3v2)
// and FLAC (Vorbis comments) formats.
package tagging

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/bogem/id3v2/v2"
	flac "github.com/go-flac/go-flac/v2"
)

// Tagger writes ID3v2 (MP3) and Vorbis comment (FLAC) tags to audio files.
type Tagger struct {
	log *slog.Logger
}

// New creates a Tagger with the given logger.
func New(logger *slog.Logger) *Tagger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tagger{log: logger}
}

// WriteTags writes ID3v2 or Vorbis comment tags to the file at path based on
// the file extension. Cover art is embedded from coverPath if the file exists.
// Returns nil on success, or an error if the file format is unrecognized or
// the tag write fails.
func (t *Tagger) WriteTags(path, artist, album, title, coverPath string) error {
	ext := strings.ToLower(strings.TrimPrefix(path[strings.LastIndex(path, "."):], ""))
	// Fallback: extract proper extension.
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		ext = strings.ToLower(path[idx:])
	}

	switch ext {
	case ".mp3":
		return t.writeID3v2(path, artist, album, title, coverPath)
	case ".flac":
		return t.writeFLACTags(path, artist, album, title, coverPath)
	default:
		return nil // non-audio or unsupported — no-op
	}
}

func (t *Tagger) writeID3v2(path, artist, album, title, coverPath string) error {
	// copySrc: ID3v2's Save() writes to the file it was opened from, so the
	// temp must start as a copy of the original.
	return t.writeTagsAtomic(path, true, func(tmpPath string) error {
		tag, err := id3v2.Open(tmpPath, id3v2.Options{Parse: true})
		if err != nil {
			t.log.Error("id3v2 open failed", "path", path, "error", err, "component", "tagger")
			return fmt.Errorf("id3v2 open: %w", err)
		}
		defer tag.Close()

		tag.DeleteAllFrames()
		tag.SetArtist(artist)
		tag.SetAlbum(album)
		tag.SetTitle(title)

		if data, err := os.ReadFile(coverPath); err == nil {
			tag.AddAttachedPicture(id3v2.PictureFrame{
				Encoding:    id3v2.EncodingUTF8,
				MimeType:    "image/jpeg",
				PictureType: id3v2.PTFrontCover,
				Description: "Front cover",
				Picture:     data,
			})
		}

		if err := tag.Save(); err != nil {
			return fmt.Errorf("id3v2 save: %w", err)
		}
		return nil
	})
}

// writeTagsAtomic stages a tag rewrite through a temp file in the same
// directory and atomically renames it over the original. A kill mid-write
// leaves the original intact (plus a leftover temp file) instead of a
// truncated or corrupt audio file. The temp's mode and ownership are restored
// to the original's after the write.
//
// Tradeoffs of temp+rename (vs the old in-place write): the file's inode is
// replaced, so symlinked tracks become regular files and hard links to the
// track stop sharing the rewritten content; and writing requires permission on
// the containing directory, not just the file. Both are acceptable for the
// Docker/mounted-volume deployment model this targets.
//
// copySrc controls whether the original is copied into the temp first: ID3v2
// must open the target file it writes to, so it needs the copy; FLAC's Save
// accepts any output path, so it parses the original and writes the temp
// directly with no copy paid.
func (t *Tagger) writeTagsAtomic(path string, copySrc bool, write func(tmpPath string) error) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".groovearr-tag-*.tmp")
	if err != nil {
		t.log.Error("create temp tag file failed", "path", path, "error", err, "component", "tagger")
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Clean up the temp on every path except a successful rename (where it no
	// longer exists).
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if copySrc {
		src, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open source: %w", err)
		}
		if _, err := io.Copy(tmp, src); err != nil {
			src.Close()
			return fmt.Errorf("copy to temp: %w", err)
		}
		if err := src.Close(); err != nil {
			return fmt.Errorf("close source: %w", err)
		}
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("close temp: %w", err)
		}
	} else {
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("close temp: %w", err)
		}
		// go-flac's Save leaks an FD when the output already exists as a
		// different file (it opens it, misses the SameFile branch, and falls
		// through to os.Create without closing the first handle). Remove the
		// placeholder so the writer creates the output fresh.
		if err := os.Remove(tmpName); err != nil {
			return fmt.Errorf("remove temp: %w", err)
		}
	}

	if err := write(tmpName); err != nil {
		return err
	}

	// Preserve ownership: temp+rename replaces the inode, which would otherwise
	// leave the rewritten file owned by the process UID — a problem for mounted
	// libraries owned by a different UID. Best-effort: chown needs privileges
	// the process may not have.
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(tmpName, int(st.Uid), int(st.Gid)); err != nil {
			t.log.Debug("chown temp failed", "path", path, "error", err, "component", "tagger")
		}
	}

	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		t.log.Error("rename temp tag file failed", "path", path, "error", err, "component", "tagger")
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

func (t *Tagger) writeFLACTags(path, artist, album, title, coverPath string) error {
	// copySrc=false: go-flac's Save(fn) writes to any path, so the original is
	// parsed directly and the temp is written from it — no file copy.
	return t.writeTagsAtomic(path, false, func(tmpPath string) error {
		f, err := flac.ParseFile(path)
		if err != nil {
			t.log.Error("flac parse failed", "path", path, "error", err, "component", "tagger")
			return fmt.Errorf("flac parse: %w", err)
		}
		defer f.Close()

		tags := map[string]string{
			"ARTIST": artist,
			"ALBUM":  album,
			"TITLE":  title,
		}
		setVorbisComments(f, tags)

		if data, err := os.ReadFile(coverPath); err == nil {
			_ = setFLACCover(f, data) // non-fatal
		}

		if err := f.Save(tmpPath); err != nil {
			return fmt.Errorf("flac save: %w", err)
		}
		return nil
	})
}

func setVorbisComments(f *flac.File, tags map[string]string) {
	blockData := marshalVorbisComment("Groovearr", tags)
	newBlock := &flac.MetaDataBlock{Type: flac.VorbisComment, Data: blockData}

	for i, meta := range f.Meta {
		if meta.Type == flac.VorbisComment {
			f.Meta[i] = newBlock
			return
		}
	}
	f.Meta = append(f.Meta[:1], append([]*flac.MetaDataBlock{newBlock}, f.Meta[1:]...)...)
}

func marshalVorbisComment(vendor string, tags map[string]string) []byte {
	var buf []byte

	vendorB := []byte(vendor)
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(vendorB)))
	buf = append(buf, lenBuf...)
	buf = append(buf, vendorB...)

	binary.LittleEndian.PutUint32(lenBuf, uint32(len(tags)))
	buf = append(buf, lenBuf...)

	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tag := []byte(k + "=" + tags[k])
		binary.LittleEndian.PutUint32(lenBuf, uint32(len(tag)))
		buf = append(buf, lenBuf...)
		buf = append(buf, tag...)
	}

	return buf
}

// setFLACCover replaces any existing picture blocks and embeds a front cover.
// Implements FLAC METADATA_BLOCK_PICTURE per the FLAC spec (section 7).
func setFLACCover(f *flac.File, imgData []byte) error {
	// Build picture block: type(4) + mime_len(4) + mime + desc_len(4) + desc +
	// width(4) + height(4) + color_depth(4) + colors_used(4) + data_len(4) + data.
	picBuf := make([]byte, 4+4+len("image/jpeg")+4+len("Front cover")+4+4+4+4+4+len(imgData))
	off := 0
	binary.BigEndian.PutUint32(picBuf[off:], 3) // Front cover
	off += 4
	mime := "image/jpeg"
	binary.BigEndian.PutUint32(picBuf[off:], uint32(len(mime)))
	off += 4
	copy(picBuf[off:], mime)
	off += len(mime)
	desc := "Front cover"
	binary.BigEndian.PutUint32(picBuf[off:], uint32(len(desc)))
	off += 4
	copy(picBuf[off:], desc)
	off += len(desc)
	// width, height, color depth, colors used — 0 means "not specified"
	binary.BigEndian.PutUint32(picBuf[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(picBuf[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(picBuf[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(picBuf[off:], 0)
	off += 4
	binary.BigEndian.PutUint32(picBuf[off:], uint32(len(imgData)))
	off += 4
	copy(picBuf[off:], imgData)

	picBlock := &flac.MetaDataBlock{Type: flac.Picture, Data: picBuf}

	// Remove existing picture blocks.
	meta := f.Meta[:0]
	for _, m := range f.Meta {
		if m.Type != flac.Picture {
			meta = append(meta, m)
		}
	}
	f.Meta = meta
	f.Meta = append(f.Meta, picBlock)
	return nil
}
