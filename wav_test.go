package tag

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func createTestWAV(sampleRate uint32, channels uint16, bitsPerSample uint16, durationSeconds float64) []byte {
	var buf bytes.Buffer

	// Calculate data size
	bytesPerSample := (bitsPerSample + 7) / 8
	totalSamples := uint32(float64(sampleRate) * durationSeconds)
	dataSize := totalSamples * uint32(channels) * uint32(bytesPerSample)

	// RIFF header
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize)) // file size - 8
	buf.WriteString("WAVE")

	// fmt chunk
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16)) // fmt chunk size
	binary.Write(&buf, binary.LittleEndian, uint16(1))  // audio format (PCM)
	binary.Write(&buf, binary.LittleEndian, channels)
	binary.Write(&buf, binary.LittleEndian, sampleRate)
	binary.Write(&buf, binary.LittleEndian, sampleRate*uint32(channels)*uint32(bytesPerSample)) // byte rate
	binary.Write(&buf, binary.LittleEndian, uint16(channels)*uint16(bytesPerSample))            // block align
	binary.Write(&buf, binary.LittleEndian, bitsPerSample)

	// data chunk
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, dataSize)

	// Write dummy audio data (silence)
	silence := make([]byte, dataSize)
	buf.Write(silence)

	return buf.Bytes()
}

func createTestWAVWithID3(opts WriteOptions) []byte {
	var body bytes.Buffer

	// fmt chunk
	body.WriteString("fmt ")
	binary.Write(&body, binary.LittleEndian, uint32(16))
	binary.Write(&body, binary.LittleEndian, uint16(1))
	binary.Write(&body, binary.LittleEndian, uint16(2))
	binary.Write(&body, binary.LittleEndian, uint32(44100))
	binary.Write(&body, binary.LittleEndian, uint32(44100*2*2))
	binary.Write(&body, binary.LittleEndian, uint16(2*2))
	binary.Write(&body, binary.LittleEndian, uint16(16))

	frames, _ := buildID3v2Frames(opts)
	id3 := []byte{'I', 'D', '3', 0x03, 0x00, 0x00}
	size := encodeSyncSafe(uint32(len(frames)))
	id3 = append(id3, size[:]...)
	id3 = append(id3, frames...)
	body.WriteString("ID3 ")
	binary.Write(&body, binary.LittleEndian, uint32(len(id3)))
	body.Write(id3)
	if len(id3)%2 == 1 {
		body.WriteByte(0)
	}

	// data chunk
	body.WriteString("data")
	binary.Write(&body, binary.LittleEndian, uint32(4))
	body.Write([]byte{0, 0, 0, 0})

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(4+body.Len()))
	buf.WriteString("WAVE")
	buf.Write(body.Bytes())

	return buf.Bytes()
}

func TestWAVDuration(t *testing.T) {
	tests := []struct {
		name          string
		sampleRate    uint32
		channels      uint16
		bitsPerSample uint16
		durationSecs  float64
		expectedDur   time.Duration
	}{
		{
			name:          "44.1kHz 16-bit stereo 5 seconds",
			sampleRate:    44100,
			channels:      2,
			bitsPerSample: 16,
			durationSecs:  5.0,
			expectedDur:   5 * time.Second,
		},
		{
			name:          "48kHz 24-bit mono 3 seconds",
			sampleRate:    48000,
			channels:      1,
			bitsPerSample: 24,
			durationSecs:  3.0,
			expectedDur:   3 * time.Second,
		},
		{
			name:          "22.05kHz 8-bit stereo 2.5 seconds",
			sampleRate:    22050,
			channels:      2,
			bitsPerSample: 8,
			durationSecs:  2.5,
			expectedDur:   2500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wavData := createTestWAV(tt.sampleRate, tt.channels, tt.bitsPerSample, tt.durationSecs)
			reader := bytes.NewReader(wavData)

			meta, err := ReadWAVMeta(reader)
			if err != nil {
				t.Fatalf("ReadWAVMeta failed: %v", err)
			}

			if meta.FileType() != WAV {
				t.Errorf("Expected file type WAV, got %v", meta.FileType())
			}

			duration := meta.Duration()

			// Allow for small rounding differences (within 1ms)
			diff := duration - tt.expectedDur
			if diff < 0 {
				diff = -diff
			}
			if diff > time.Millisecond {
				t.Errorf("Expected duration %v, got %v (diff: %v)", tt.expectedDur, duration, diff)
			}

			// Test Raw() method returns expected values
			raw := meta.Raw()
			if raw["sample_rate"] != tt.sampleRate {
				t.Errorf("Expected sample rate %d, got %v", tt.sampleRate, raw["sample_rate"])
			}
			if raw["channels"] != tt.channels {
				t.Errorf("Expected channels %d, got %v", tt.channels, raw["channels"])
			}
			if raw["bits_per_sample"] != tt.bitsPerSample {
				t.Errorf("Expected bits per sample %d, got %v", tt.bitsPerSample, raw["bits_per_sample"])
			}
		})
	}
}

func TestWAVMetadataInterface(t *testing.T) {
	wavData := createTestWAV(44100, 2, 16, 1.0)
	reader := bytes.NewReader(wavData)

	meta, err := ReadWAVMeta(reader)
	if err != nil {
		t.Fatalf("ReadWAVMeta failed: %v", err)
	}

	// Test that all metadata interface methods work (should return empty/zero values for WAV)
	if meta.Title() != "" {
		t.Errorf("Expected empty title, got %q", meta.Title())
	}
	if meta.Album() != "" {
		t.Errorf("Expected empty album, got %q", meta.Album())
	}
	if meta.Artist() != "" {
		t.Errorf("Expected empty artist, got %q", meta.Artist())
	}
	if meta.Year() != 0 {
		t.Errorf("Expected year 0, got %d", meta.Year())
	}
	if track, total := meta.Track(); track != 0 || total != 0 {
		t.Errorf("Expected track (0, 0), got (%d, %d)", track, total)
	}
	if meta.Picture() != nil {
		t.Errorf("Expected nil picture, got %v", meta.Picture())
	}
}

func TestWAVID3Chunk(t *testing.T) {
	pictureData := []byte{0xff, 0xd8, 0xff, 0xd9}
	wavData := createTestWAVWithID3(WriteOptions{
		Title:       "Song Title",
		Artist:      "Song Artist",
		Album:       "Song Album",
		AlbumArtist: "Album Artist",
		Year:        2026,
		Genre:       "Pop",
		Lyrics:      "la la",
		Picture: &Picture{
			MIMEType: "image/jpeg",
			Data:     pictureData,
		},
	})

	meta, err := ReadWAVMeta(bytes.NewReader(wavData))
	if err != nil {
		t.Fatalf("ReadWAVMeta failed: %v", err)
	}

	if meta.FileType() != WAV {
		t.Fatalf("Expected file type WAV, got %v", meta.FileType())
	}
	if meta.Format() != ID3v2_3 {
		t.Errorf("Expected format %v, got %v", ID3v2_3, meta.Format())
	}
	if meta.Title() != "Song Title" {
		t.Errorf("Expected title from ID3 chunk, got %q", meta.Title())
	}
	if meta.Artist() != "Song Artist" {
		t.Errorf("Expected artist from ID3 chunk, got %q", meta.Artist())
	}
	if meta.Album() != "Song Album" {
		t.Errorf("Expected album from ID3 chunk, got %q", meta.Album())
	}
	if meta.AlbumArtist() != "Album Artist" {
		t.Errorf("Expected album artist from ID3 chunk, got %q", meta.AlbumArtist())
	}
	if meta.Year() != 2026 {
		t.Errorf("Expected year 2026, got %d", meta.Year())
	}
	if meta.Genre() != "Pop" {
		t.Errorf("Expected genre Pop, got %q", meta.Genre())
	}
	if meta.Lyrics() != "la la" {
		t.Errorf("Expected lyrics from ID3 chunk, got %q", meta.Lyrics())
	}
	pic := meta.Picture()
	if pic == nil {
		t.Fatal("Expected picture from ID3 chunk")
	}
	if pic.MIMEType != "image/jpeg" {
		t.Errorf("Expected image/jpeg picture, got %q", pic.MIMEType)
	}
	if !bytes.Equal(pic.Data, pictureData) {
		t.Errorf("Expected picture data %v, got %v", pictureData, pic.Data)
	}
}

// toGBK encodes a UTF-8 string to GBK bytes for building test fixtures.
func toGBK(t *testing.T, s string) []byte {
	t.Helper()
	b, err := io.ReadAll(transform.NewReader(bytes.NewReader([]byte(s)), simplifiedchinese.GBK.NewEncoder()))
	if err != nil {
		t.Fatalf("GBK encode %q failed: %v", s, err)
	}
	return b
}

// createTestWAVExtensibleGBK builds a WAVE_FORMAT_EXTENSIBLE (0xFFFE) WAV whose
// LIST/INFO tags are GBK-encoded, mirroring real-world files exported by some
// Chinese tools (songloft-org/songloft#319).
func createTestWAVExtensibleGBK(t *testing.T, title, artist, album string) []byte {
	t.Helper()

	writeInfoTag := func(w *bytes.Buffer, id string, data []byte) {
		w.WriteString(id)
		binary.Write(w, binary.LittleEndian, uint32(len(data)))
		w.Write(data)
		if len(data)%2 == 1 {
			w.WriteByte(0)
		}
	}

	var body bytes.Buffer

	// fmt chunk (40 bytes) — WAVE_FORMAT_EXTENSIBLE, 2ch / 192kHz / 24-bit
	const channels, sampleRate, bits = uint16(2), uint32(192000), uint16(24)
	blockAlign := channels * (bits / 8)
	byteRate := sampleRate * uint32(blockAlign)
	body.WriteString("fmt ")
	binary.Write(&body, binary.LittleEndian, uint32(40))
	binary.Write(&body, binary.LittleEndian, uint16(0xFFFE)) // WAVE_FORMAT_EXTENSIBLE
	binary.Write(&body, binary.LittleEndian, channels)
	binary.Write(&body, binary.LittleEndian, sampleRate)
	binary.Write(&body, binary.LittleEndian, byteRate)
	binary.Write(&body, binary.LittleEndian, blockAlign)
	binary.Write(&body, binary.LittleEndian, bits)
	binary.Write(&body, binary.LittleEndian, uint16(22)) // cbSize
	binary.Write(&body, binary.LittleEndian, bits)       // valid bits per sample
	binary.Write(&body, binary.LittleEndian, uint32(3))  // channel mask
	// SubFormat GUID: first 2 bytes = 0x0001 (PCM)
	body.Write([]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71})

	// data chunk — one second worth of silence
	dataSize := byteRate
	body.WriteString("data")
	binary.Write(&body, binary.LittleEndian, dataSize)
	body.Write(make([]byte, dataSize))

	// LIST/INFO chunk with GBK-encoded tags
	var info bytes.Buffer
	info.WriteString("INFO")
	writeInfoTag(&info, "INAM", toGBK(t, title))
	writeInfoTag(&info, "IART", toGBK(t, artist))
	writeInfoTag(&info, "IPRD", toGBK(t, album))
	body.WriteString("LIST")
	binary.Write(&body, binary.LittleEndian, uint32(info.Len()))
	body.Write(info.Bytes())

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(4+body.Len()))
	buf.WriteString("WAVE")
	buf.Write(body.Bytes())
	return buf.Bytes()
}

// TestWAVExtensibleGBKInfo verifies that WAVE_FORMAT_EXTENSIBLE files are parsed
// (not rejected) and their GBK RIFF INFO tags are decoded to UTF-8.
func TestWAVExtensibleGBKInfo(t *testing.T) {
	wavData := createTestWAVExtensibleGBK(t, "单身情歌", "林志炫", "1987年到1999年华语100首经典")

	meta, err := ReadWAVMeta(bytes.NewReader(wavData))
	if err != nil {
		t.Fatalf("ReadWAVMeta failed for WAVE_FORMAT_EXTENSIBLE: %v", err)
	}

	if got := meta.Title(); got != "单身情歌" {
		t.Errorf("Title = %q, want %q", got, "单身情歌")
	}
	if got := meta.Artist(); got != "林志炫" {
		t.Errorf("Artist = %q, want %q", got, "林志炫")
	}
	if got := meta.Album(); got != "1987年到1999年华语100首经典" {
		t.Errorf("Album = %q, want %q", got, "1987年到1999年华语100首经典")
	}

	// byteRate-based duration: 1s of 192kHz/24-bit/stereo data
	if diff := meta.Duration() - time.Second; diff < -time.Millisecond || diff > time.Millisecond {
		t.Errorf("Duration = %v, want ~1s", meta.Duration())
	}
}
