package tag

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func fileMetadata(t *testing.T, path string) Metadata {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := ReadFrom(f)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDSFIncrementalWritesAndExternalChange(t *testing.T) {
	source, err := os.ReadFile("testdata/with_tags/sample.dsf")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pretend.mp3")
	if err := os.WriteFile(path, source, 0640); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := parseDSF(old)
	old.Close()
	if err != nil {
		t.Fatal(err)
	}
	const headline = "新标题"
	const artist = "New artist"
	var group sync.WaitGroup
	for _, options := range []WriteOptions{{Title: headline}, {Artist: artist}} {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := WriteTag(path, options); err != nil {
				t.Errorf("concurrent write: %v", err)
			}
		}()
	}
	group.Wait()
	m := fileMetadata(t, path)
	if m.Title() != headline || m.Artist() != artist || m.FileType() != DSF {
		t.Fatalf("metadata is %q, %q, %s", m.Title(), m.Artist(), m.FileType())
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source[28:layout.audioEnd], edited[28:layout.audioEnd]) {
		t.Fatal("DSF audio changed")
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0640 {
		t.Fatalf("file permissions changed: %v", err)
	}

	err = atomicRewrite(path, DSF, func(_, dst *os.File, _ int64) error {
		if _, err := dst.Write(edited); err != nil {
			return err
		}
		return os.WriteFile(path, append(bytes.Clone(edited), 0), 0640)
	})
	if err == nil {
		t.Fatal("replacement accepted a changed source")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, append(bytes.Clone(edited), 0)) {
		t.Fatal("replacement overwrote the external change")
	}
}

func TestMediaContainerPacketRoundTrip(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg fixture generator is unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("Independent FFprobe is unavailable")
	}
	demuxers, err := exec.Command(ffmpeg, "-hide_banner", "-demuxers").Output()
	if err != nil || !strings.Contains(string(demuxers), " lavfi ") {
		t.Skip("FFmpeg lavfi fixture generation is unavailable")
	}
	for _, tc := range []struct {
		ext, codec, muxer string
		kind              FileType
		writable          bool
	}{
		{"aac", "aac", "adts", AAC, true},
		{"wv", "wavpack", "wv", WV, true},
		{"wav", "pcm_s16le", "wav", WAV, true},
		{"mov", "aac", "mov", MOV, true},
		{"mka", "flac", "matroska", MKA, true},
		{"mkv", "flac", "matroska", MKA, true},
		{"webm", "libopus", "webm", WEBM, true},
		{"wma", "wmav2", "asf", ASF, true},
		{"wmv", "wmav2", "asf", ASF, true},
		{"avi", "pcm_s16le", "avi", AVI, true},
		{"flv", "libmp3lame", "flv", FLV, true},
		{"rm", "ac3", "rm", RM, true},
		{"rmvb", "ac3", "rm", RM, true},
		{"dts", "dca", "dts", DTS, false},
		{"ts", "mp2", "mpegts", MPEGTS, false},
		{"mpg", "mp2", "mpeg", MPEGPS, false},
		{"mpeg", "mp2", "mpeg", MPEGPS, false},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			const originalTitle, newTitle = "Before tag", "After  tag"
			input := filepath.Join(t.TempDir(), "track."+tc.ext)
			cmd := exec.Command(ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i",
				"sine=frequency=440:sample_rate=44100:duration=0.7",
				"-c:a", tc.codec, "-strict", "experimental",
				"-metadata", "title="+originalTitle, "-f", tc.muxer, input)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate sample: %s: %v", out, err)
			}
			original, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(filepath.Dir(input), "disguised.flac")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if m := fileMetadata(t, path); m.FileType() != tc.kind {
				t.Fatalf("file type %s, want %s", m.FileType(), tc.kind)
			}
			before, _ := independentPackets(t, ffprobe, input)
			pic := &Picture{MIMEType: "image/png", Ext: "png", Data: pngHeader}
			err = WriteTag(path, WriteOptions{Title: newTitle, Picture: pic})
			if !tc.writable {
				if !errors.Is(err, ErrUnsupportedWrite) {
					t.Fatalf("raw stream write returned %v", err)
				}
				out, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(original, out) {
					t.Fatal("database-only write modified media")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m := fileMetadata(t, path); m.Title() != newTitle || tc.kind == WEBM && m.Picture() != nil {
				t.Fatalf("rewritten metadata: title=%q, picture=%v", m.Title(), m.Picture() != nil)
			}
			after, probeTitle := independentPackets(t, ffprobe, path)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("audio packet hashes changed (%d before, %d after)", len(before), len(after))
			}
			if probeTitle != newTitle {
				t.Fatalf("FFprobe title=%q, want %q", probeTitle, newTitle)
			}
			truncated := original[:len(original)/2]
			if err := os.WriteFile(path, truncated, 0600); err != nil {
				t.Fatal(err)
			}
			if err := WriteTag(path, WriteOptions{Title: newTitle}); err == nil {
				t.Fatal("accepted a truncated source")
			}
			out, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(truncated, out) {
				t.Fatal("failed write modified truncated source")
			}
		})
	}
}

func independentPackets(t *testing.T, ffprobe, path string) ([]string, string) {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "a",
		"-show_packets", "-show_streams", "-show_format", "-show_data_hash", "sha256",
		"-show_entries", "packet=data_hash:stream_tags=title:format_tags=title",
		"-of", "json", path).Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("FFprobe: %s", e.Stderr)
		}
		t.Fatal(err)
	}
	var result struct {
		Packets []struct {
			Hash string `json:"data_hash"`
		}
		Streams []struct{ Tags struct{ Title string } }
		Format  struct{ Tags struct{ Title string } }
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, p := range result.Packets {
		if !strings.HasPrefix(p.Hash, "SHA256:") {
			t.Fatalf("missing packet digest in %s", path)
		}
		hashes = append(hashes, p.Hash)
	}
	if len(hashes) == 0 {
		t.Fatal(io.ErrUnexpectedEOF)
	}
	title := result.Format.Tags.Title
	for _, stream := range result.Streams {
		if stream.Tags.Title != "" {
			title = stream.Tags.Title
			break
		}
	}
	return hashes, title
}

func TestDSDIFFNativeAndDatabaseFields(t *testing.T) {
	chunk := func(id string, data []byte) []byte {
		result := append([]byte(id), binary.BigEndian.AppendUint64(nil, uint64(len(data)))...)
		result = append(result, data...)
		if len(data)%2 != 0 {
			result = append(result, 0)
		}
		return result
	}
	properties := append([]byte("SND "), chunk("FS  ", binary.BigEndian.AppendUint32(nil, 2822400))...)
	properties = append(properties, chunk("CHNL", []byte{0, 2, 'S', 'L', 'F', 'T', 'S', 'R', 'G', 'T'})...)
	properties = append(properties, chunk("CMPR", []byte("DSD \x0enot compressed"))...)
	body := append([]byte("DSD "), chunk("FVER", []byte{1, 5, 0, 0})...)
	body = append(body, chunk("PROP", properties)...)
	audio := chunk("DSD ", bytes.Repeat([]byte{0x69, 0x96}, 2048))
	source := chunk("FRM8", append(bytes.Clone(body), audio...))
	path := filepath.Join(t.TempDir(), "sample.bin")
	put := func(b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(expected []byte) {
		t.Helper()
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, expected) {
			t.Fatalf("unexpected file bytes: %v", err)
		}
	}
	put(source)
	if err := WriteTag(path, WriteOptions{Album: "DB only"}); !errors.Is(err, ErrUnsupportedWrite) {
		t.Fatal(err)
	}
	check(source)
	if err := WriteTag(path, WriteOptions{Title: "New", Artist: "Artist", Album: "DB only"}); err != nil {
		t.Fatal(err)
	}
	m := fileMetadata(t, path)
	if m.FileType() != DFF || m.Title() != "New" || m.Artist() != "Artist" || m.Album() != "" {
		t.Fatalf("unexpected DSDIFF tags: %v", m.Raw())
	}
	edited, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source[12:], edited[12:len(source)]) {
		t.Fatalf("DSDIFF audio or header properties changed: %v", err)
	}

	// A DIIN before later chunks cannot grow safely while preserving positions.
	early := append(bytes.Clone(body), edited[len(source):]...)
	early = chunk("FRM8", append(early, audio...))
	put(early)
	if err := WriteTag(path, WriteOptions{Title: "The title is too large for this slot"}); !errors.Is(err, ErrUnsupportedWrite) {
		t.Fatal(err)
	}
	check(early)
}

func TestMatroskaExpandsIndexedMetadataWithoutMovingMedia(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg fixture generator is unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("Independent FFprobe is unavailable")
	}
	demuxers, err := exec.Command(ffmpeg, "-hide_banner", "-demuxers").Output()
	if err != nil || !strings.Contains(string(demuxers), " lavfi ") {
		t.Skip("FFmpeg lavfi fixture generation is unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "indexed.mkv")
	attachment := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(attachment, []byte("Preserve this attachment."), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i",
		"sine=frequency=440:sample_rate=44100:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=44100:duration=1",
		"-map", "0:a", "-map", "1:a", "-c:a", "pcm_s16le",
		"-metadata", "title=Original",
		"-metadata:s:a:1", "title=Second track",
		"-attach", attachment, "-metadata:s:t", "mimetype=text/plain", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate sample: %s: %v", out, err)
	}
	before, _ := independentPackets(t, ffprobe, path)
	if err := WriteTag(path, WriteOptions{Title: "New title with more space"}); err != nil {
		t.Fatal(err)
	}
	after, title := independentPackets(t, ffprobe, path)
	if !reflect.DeepEqual(after, before) || title != "New title with more space" {
		t.Fatalf("retained packets=%v, title=%q", reflect.DeepEqual(after, before), title)
	}
	probe, err := exec.Command(ffprobe, "-v", "error", "-show_entries",
		"stream_tags=title,filename", "-of", "json", path).Output()
	if err != nil || !bytes.Contains(probe, []byte("Second track")) || !bytes.Contains(probe, []byte("notes.txt")) {
		t.Fatalf("stream metadata changed: %s, %v", probe, err)
	}
}
