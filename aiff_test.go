package tag

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// encodeIEEE754Extended converts a float64 to 10-byte IEEE 754 extended precision.
func encodeIEEE754Extended(val float64) []byte {
	b := make([]byte, 10)
	if val == 0 {
		return b
	}

	sign := 0
	if val < 0 {
		sign = 1
		val = -val
	}

	// Get exponent and mantissa
	frac, exp := math.Frexp(val)
	// Frexp returns 0.5 <= frac < 1.0, exponent such that val = frac * 2^exp
	// Extended format: 1.mantissa * 2^(exponent-16383), with explicit integer bit
	exponent := exp + 16383 - 1
	mantissa := uint64(frac * (1 << 64))

	b[0] = byte(sign<<7) | byte(exponent>>8)
	b[1] = byte(exponent)
	binary.BigEndian.PutUint64(b[2:], mantissa)
	return b
}

func createTestAIFF(sampleRate uint32, channels uint16, bitsPerSample uint16, numFrames uint32) []byte {
	var buf bytes.Buffer

	// COMM chunk
	var comm bytes.Buffer
	binary.Write(&comm, binary.BigEndian, channels)
	binary.Write(&comm, binary.BigEndian, numFrames)
	binary.Write(&comm, binary.BigEndian, bitsPerSample)
	comm.Write(encodeIEEE754Extended(float64(sampleRate)))

	// SSND chunk (minimal: offset + blockSize + silence)
	bytesPerSample := (bitsPerSample + 7) / 8
	audioDataSize := numFrames * uint32(channels) * uint32(bytesPerSample)
	ssndSize := 8 + audioDataSize // offset(4) + blockSize(4) + data

	// NAME chunk
	nameData := []byte("Test Song Title")
	// AUTH chunk
	authData := []byte("Test Artist")

	// Calculate total FORM size
	formSize := uint32(4) // "AIFF"
	formSize += 8 + uint32(comm.Len())
	formSize += 8 + uint32(len(nameData))
	if len(nameData)%2 == 1 {
		formSize++
	}
	formSize += 8 + uint32(len(authData))
	if len(authData)%2 == 1 {
		formSize++
	}
	formSize += 8 + ssndSize

	// Write FORM header
	buf.WriteString("FORM")
	binary.Write(&buf, binary.BigEndian, formSize)
	buf.WriteString("AIFF")

	// Write COMM chunk
	buf.WriteString("COMM")
	binary.Write(&buf, binary.BigEndian, uint32(comm.Len()))
	buf.Write(comm.Bytes())

	// Write NAME chunk
	buf.WriteString("NAME")
	binary.Write(&buf, binary.BigEndian, uint32(len(nameData)))
	buf.Write(nameData)
	if len(nameData)%2 == 1 {
		buf.WriteByte(0)
	}

	// Write AUTH chunk
	buf.WriteString("AUTH")
	binary.Write(&buf, binary.BigEndian, uint32(len(authData)))
	buf.Write(authData)
	if len(authData)%2 == 1 {
		buf.WriteByte(0)
	}

	// Write SSND chunk
	buf.WriteString("SSND")
	binary.Write(&buf, binary.BigEndian, ssndSize)
	binary.Write(&buf, binary.BigEndian, uint32(0)) // offset
	binary.Write(&buf, binary.BigEndian, uint32(0)) // blockSize
	buf.Write(make([]byte, audioDataSize))

	return buf.Bytes()
}

func TestAIFFDuration(t *testing.T) {
	tests := []struct {
		name        string
		sampleRate  uint32
		channels    uint16
		bitsPerSample uint16
		numFrames   uint32
		expectedDur time.Duration
	}{
		{
			name:          "44.1kHz 16-bit stereo 5 seconds",
			sampleRate:    44100,
			channels:      2,
			bitsPerSample: 16,
			numFrames:     44100 * 5,
			expectedDur:   5 * time.Second,
		},
		{
			name:          "48kHz 24-bit mono 3 seconds",
			sampleRate:    48000,
			channels:      1,
			bitsPerSample: 24,
			numFrames:     48000 * 3,
			expectedDur:   3 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := createTestAIFF(tt.sampleRate, tt.channels, tt.bitsPerSample, tt.numFrames)
			reader := bytes.NewReader(data)

			meta, err := ReadAIFFMeta(reader)
			if err != nil {
				t.Fatalf("ReadAIFFMeta failed: %v", err)
			}

			if meta.FileType() != AIFF {
				t.Errorf("Expected file type AIFF, got %v", meta.FileType())
			}

			duration := meta.Duration()
			diff := duration - tt.expectedDur
			if diff < 0 {
				diff = -diff
			}
			if diff > time.Millisecond {
				t.Errorf("Expected duration %v, got %v (diff: %v)", tt.expectedDur, duration, diff)
			}

			if meta.SampleRate() != int(tt.sampleRate) {
				t.Errorf("Expected sample rate %d, got %d", tt.sampleRate, meta.SampleRate())
			}
		})
	}
}

func TestAIFFNativeMetadata(t *testing.T) {
	data := createTestAIFF(44100, 2, 16, 44100)
	reader := bytes.NewReader(data)

	meta, err := ReadAIFFMeta(reader)
	if err != nil {
		t.Fatalf("ReadAIFFMeta failed: %v", err)
	}

	if meta.Title() != "Test Song Title" {
		t.Errorf("Expected title 'Test Song Title', got %q", meta.Title())
	}
	if meta.Artist() != "Test Artist" {
		t.Errorf("Expected artist 'Test Artist', got %q", meta.Artist())
	}
}

func TestAIFFIdentify(t *testing.T) {
	data := createTestAIFF(44100, 2, 16, 44100)
	reader := bytes.NewReader(data)

	_, fileType, err := Identify(reader)
	if err != nil {
		t.Fatalf("Identify failed: %v", err)
	}
	if fileType != AIFF {
		t.Errorf("Expected AIFF, got %v", fileType)
	}
}

func TestAIFFReadFrom(t *testing.T) {
	data := createTestAIFF(44100, 2, 16, 44100)
	reader := bytes.NewReader(data)

	meta, err := ReadFrom(reader)
	if err != nil {
		t.Fatalf("ReadFrom failed: %v", err)
	}
	if meta.FileType() != AIFF {
		t.Errorf("Expected AIFF, got %v", meta.FileType())
	}
	if meta.Title() != "Test Song Title" {
		t.Errorf("Expected title 'Test Song Title', got %q", meta.Title())
	}
}

func TestAIFFBitRate(t *testing.T) {
	data := createTestAIFF(44100, 2, 16, 44100)
	reader := bytes.NewReader(data)

	meta, err := ReadAIFFMeta(reader)
	if err != nil {
		t.Fatalf("ReadAIFFMeta failed: %v", err)
	}

	// 44100 * 16 * 2 / 1000 = 1411
	expectedBitRate := 44100 * 16 * 2 / 1000
	if meta.BitRate() != expectedBitRate {
		t.Errorf("Expected bitrate %d, got %d", expectedBitRate, meta.BitRate())
	}
}

func TestParseIEEE754Extended(t *testing.T) {
	tests := []struct {
		name     string
		rate     float64
	}{
		{"44100", 44100},
		{"48000", 48000},
		{"96000", 96000},
		{"22050", 22050},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := encodeIEEE754Extended(tt.rate)
			decoded := parseIEEE754Extended(encoded)
			if math.Abs(decoded-tt.rate) > 0.5 {
				t.Errorf("Expected %v, got %v", tt.rate, decoded)
			}
		})
	}
}

func createTestAIFFFile(t *testing.T) string {
	t.Helper()
	data := createTestAIFF(44100, 2, 16, 44100)
	dir := t.TempDir()
	path := filepath.Join(dir, "test.aif")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return path
}

func TestWriteAIFF_RoundTrip(t *testing.T) {
	path := createTestAIFFFile(t)

	opts := WriteOptions{
		Title:       "AIFF Title",
		Artist:      "AIFF Artist",
		AlbumArtist: "AIFF Album Artist",
		Album:       "AIFF Album",
		Year:        2026,
		Genre:       "Electronic",
		Lyrics:      "Line 1\nLine 2 中文歌词",
		Picture: &Picture{
			MIMEType: "image/jpeg",
			Data:     []byte{0xff, 0xd8, 0xff, 0xe0, 0xde, 0xad, 0xbe, 0xef},
		},
	}

	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	m, err := ReadFrom(f)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
	if got := m.Album(); got != opts.Album {
		t.Errorf("Album: got %q, want %q", got, opts.Album)
	}
	if got := m.AlbumArtist(); got != opts.AlbumArtist {
		t.Errorf("AlbumArtist: got %q, want %q", got, opts.AlbumArtist)
	}
	if got := m.Year(); got != opts.Year {
		t.Errorf("Year: got %d, want %d", got, opts.Year)
	}
	if got := m.Genre(); got != opts.Genre {
		t.Errorf("Genre: got %q, want %q", got, opts.Genre)
	}
	if got := m.Lyrics(); got != opts.Lyrics {
		t.Errorf("Lyrics: got %q, want %q", got, opts.Lyrics)
	}
	if pic := m.Picture(); pic == nil {
		t.Error("Picture: got nil, want non-nil")
	} else if string(pic.Data) != string(opts.Picture.Data) {
		t.Errorf("Picture data mismatch: got %d bytes, want %d", len(pic.Data), len(opts.Picture.Data))
	}
}

func TestWriteAIFF_PreservesAudio(t *testing.T) {
	path := createTestAIFFFile(t)

	// Read original duration
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	origMeta, err := ReadAIFFMeta(f)
	if err != nil {
		f.Close()
		t.Fatalf("ReadAIFFMeta: %v", err)
	}
	origDuration := origMeta.Duration()
	origSampleRate := origMeta.SampleRate()
	f.Close()

	// Write metadata
	opts := WriteOptions{
		Title:  "Test",
		Artist: "Artist",
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	// Read back and verify audio params are preserved
	f, err = os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	m, err := ReadAIFFMeta(f)
	if err != nil {
		t.Fatalf("ReadAIFFMeta: %v", err)
	}

	if m.Duration() != origDuration {
		t.Errorf("Duration changed: got %v, want %v", m.Duration(), origDuration)
	}
	if m.SampleRate() != origSampleRate {
		t.Errorf("SampleRate changed: got %d, want %d", m.SampleRate(), origSampleRate)
	}
}

func TestWriteAIFF_OverwriteExisting(t *testing.T) {
	path := createTestAIFFFile(t)

	// First write
	opts1 := WriteOptions{
		Title:  "First Title",
		Artist: "First Artist",
	}
	if err := WriteTag(path, opts1); err != nil {
		t.Fatalf("first WriteTag: %v", err)
	}

	// Second write (overwrite)
	opts2 := WriteOptions{
		Title:  "Second Title",
		Artist: "Second Artist",
		Album:  "New Album",
	}
	if err := WriteTag(path, opts2); err != nil {
		t.Fatalf("second WriteTag: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	m, err := ReadFrom(f)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	if got := m.Title(); got != opts2.Title {
		t.Errorf("Title: got %q, want %q", got, opts2.Title)
	}
	if got := m.Artist(); got != opts2.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts2.Artist)
	}
	if got := m.Album(); got != opts2.Album {
		t.Errorf("Album: got %q, want %q", got, opts2.Album)
	}
}
