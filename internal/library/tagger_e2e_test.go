package library

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	flac "github.com/go-flac/go-flac/v2"
	"github.com/ramonskie/groovearr/internal/tagging"
)

// TestWriteTagsRoundTripFLAC writes tags through the real go-flac writer and
// the atomic temp path, then re-parses the result to confirm the file is still
// a valid FLAC with the new comments. This exercises the no-copy temp path
// (go-flac Save to a fresh output path) end to end.
func TestWriteTagsRoundTripFLAC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.flac")
	if err := os.WriteFile(path, minimalFLAC("Old Artist", "Old Album", "Old Title", 1999, 1, 1), 0o644); err != nil {
		t.Fatal(err)
	}

	tagger := tagging.New(testLogger())
	if err := tagger.WriteTags(path, "New Artist", "New Album", "New Title", ""); err != nil {
		t.Fatalf("WriteTags: %v", err)
	}

	// Re-parse with the same parser the tagger uses.
	f, err := flac.ParseFile(path)
	if err != nil {
		t.Fatalf("re-parse after tag write: %v", err)
	}
	defer f.Close()

	comments := map[string]string{}
	for _, meta := range f.Meta {
		if meta.Type != flac.VorbisComment {
			continue
		}
		for k, v := range vorbisComments(meta.Data) {
			comments[k] = v
		}
	}
	if comments["ARTIST"] != "New Artist" || comments["ALBUM"] != "New Album" || comments["TITLE"] != "New Title" {
		t.Fatalf("comments not updated: %+v", comments)
	}

	// No temp files left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".groovearr-tag-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// vorbisComments decodes a FLAC Vorbis comment block payload into a key→value
// map. Format: [vendor length u32le][vendor bytes][count u32le][len u32le +
// "K=V" bytes]*.
func vorbisComments(data []byte) map[string]string {
	out := map[string]string{}
	pos := 0
	readU32 := func() uint32 {
		v := binary.LittleEndian.Uint32(data[pos:])
		pos += 4
		return v
	}
	// Vendor string: [length u32le][bytes].
	pos += int(readU32())
	// Comment count.
	count := int(readU32())
	for i := 0; i < count && pos < len(data); i++ {
		entryLen := int(readU32())
		if pos+entryLen > len(data) {
			break
		}
		entry := string(data[pos : pos+entryLen])
		pos += entryLen
		if k, v, ok := strings.Cut(entry, "="); ok {
			out[k] = v
		}
	}
	return out
}
