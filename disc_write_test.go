package tag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDiscRoundTrip(t *testing.T) {
	for _, format := range []string{"mp3", "flac", "m4a", "ogg", "ape", "aiff"} {
		t.Run(format, func(t *testing.T) {
			var path string
			switch format {
			case "ape":
				path = createMinimalAPE(t)
			case "aiff":
				path = filepath.Join(t.TempDir(), "song.aiff")
				if err := os.WriteFile(path, createTestAIFF(44100, 2, 16, 100), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				path = copyFixture(t, "testdata/without_tags/sample."+format)
			}
			for _, disc := range []string{"2/3", "1"} {
				if err := WriteTag(path, WriteOptions{Title: "Title", Track: "10/12", Disc: disc}); err != nil {
					t.Fatal(err)
				}
				meta := readBackMetadata(t, path)
				if n, total := meta.Track(); n != 10 || total != 12 {
					t.Errorf("track: %d/%d", n, total)
				}
				want, wantTotal := parseXofN(disc)
				if n, total := meta.Disc(); n != want || total != wantTotal {
					t.Errorf("disc: %d/%d want %s", n, total, disc)
				}
				// A caller predating disc metadata must preserve the file's disc.
				if err := WriteTag(path, WriteOptions{Title: "Edited", Track: "2"}); err != nil {
					t.Fatal(err)
				}
				meta = readBackMetadata(t, path)
				if n, total := meta.Disc(); n != want || total != wantTotal {
					t.Errorf("preserved disc: %d/%d", n, total)
				}
			}
		})
	}
}
