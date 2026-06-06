package tag

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// ReadAPEMeta reads Monkey's Audio metadata from the io.ReadSeeker.
//
// APE files start with "MAC " magic at offset 0 (version ≥ 3.98).
// Text tags are stored in an APEv2 footer at the end of the file.
func ReadAPEMeta(r io.ReadSeeker) (Metadata, error) {
	// Verify MAC magic
	magic, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if magic != "MAC " {
		return nil, errors.New("expected 'MAC ' magic for Monkey's Audio")
	}

	// Read APE descriptor: version(2B) + compression(2B) + flags(2B) + channels(2B)
	// + sampleRate(4B) + wavHeaderBytes(4B) + wavTailBytes(4B) + wavDataBytes(4B)
	// + frameBytes(4B) + terminatingBytes(4B) = 28 bytes
	// Full descriptor from MAC is 32 bytes: 4B magic + 2B version + 2B compression
	// + 2B formatFlags + 2B channels + 4B sampleRate + 4B wavHeaderBytes
	// + 4B wavTailBytes + 4B totalFrames_lo + 4B totalFrames_hi + 4B finalFrameBlocks
	// + 4B terminatingDataBytes
	// Total after magic: 28 bytes (but older APE descriptors are 24 or 20)
	version, err := readUint16LittleEndian(r)
	if err != nil {
		return nil, fmt.Errorf("read ape version: %w", err)
	}

	_, err = readUint16LittleEndian(r) // compression level
	if err != nil {
		return nil, fmt.Errorf("read compression: %w", err)
	}

	var formatFlags, channels uint16
	if version >= 3990 { // format flags added in 3.99
		formatFlags, err = readUint16LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read format flags: %w", err)
		}
	} else {
		formatFlags = 0
	}

	channels, err = readUint16LittleEndian(r)
	if err != nil {
		return nil, fmt.Errorf("read channels: %w", err)
	}

	sampleRate, err := readUint32LittleEndian(r)
	if err != nil {
		return nil, fmt.Errorf("read sample rate: %w", err)
	}

	// skip wavHeaderBytes(4) + wavTailBytes(4)
	_, err = r.Seek(8, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("seek wav header/tail: %w", err)
	}

	totalFrames, err := readUint32LittleEndian(r)
	if err != nil {
		return nil, fmt.Errorf("read total frames lo: %w", err)
	}
	totalFramesHi, err := readUint32LittleEndian(r)
	if err != nil {
		return nil, fmt.Errorf("read total frames hi: %w", err)
	}
	finalFrameBlocks, err := readUint32LittleEndian(r)
	if err != nil {
		return nil, fmt.Errorf("read final frame blocks: %w", err)
	}

	// Calculate duration
	_ = formatFlags
	totalFrames64 := uint64(totalFramesHi)<<32 | uint64(totalFrames)
	var duration time.Duration
	if sampleRate > 0 && totalFrames64 > 0 {
		totalSamples := (totalFrames64-1)*uint64(finalFrameBlocks)
		duration = time.Duration(totalSamples) * time.Second / time.Duration(sampleRate)
	}

	m := &metadataAPE{
		sampleRate: int(sampleRate),
		channels:   int(channels),
		duration:   duration,
	}

	// Seek to end and look for APETAGEX footer
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("seek to end: %w", err)
	}
	if size < 32 {
		return m, nil // too small for APEv2 footer
	}

	// Read last 32 bytes as potential APEv2 footer
	footerPos := size - 32
	if _, err := r.Seek(footerPos, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to ape footer: %w", err)
	}

	footerID, err := readString(r, 8)
	if err != nil {
		return m, nil
	}
	if footerID != "APETAGEX" {
		return m, nil // no APEv2 tag
	}

	footerVersion, err := readUint32LittleEndian(r)
	if err != nil {
		return m, nil
	}
	tagSize, err := readUint32LittleEndian(r)
	if err != nil {
		return m, nil
	}
	itemCount, err := readUint32LittleEndian(r)
	if err != nil {
		return m, nil
	}
	tagFlags, err := readUint32LittleEndian(r)
	if err != nil {
		return m, nil
	}
	_ = footerVersion
	_ = tagFlags

	// Seek to start of tag items: footerPos - tagSize
	tagStart := footerPos - int64(tagSize)
	if tagStart < 0 {
		return m, nil
	}
	if _, err := r.Seek(tagStart, io.SeekStart); err != nil {
		return m, nil
	}

	// Parse APEv2 items
	for i := uint32(0); i < itemCount; i++ {
		valueSize, err := readUint32LittleEndian(r)
		if err != nil {
			break
		}
		itemFlags, err := readUint32LittleEndian(r)
		if err != nil {
			break
		}
		_ = itemFlags

		// Read null-terminated key
		var keyBytes []byte
		for {
			b := make([]byte, 1)
			if _, err := io.ReadFull(r, b); err != nil {
				break
			}
			if b[0] == 0 {
				break
			}
			keyBytes = append(keyBytes, b[0])
		}
		key := string(keyBytes)
		if key == "" {
			break
		}

		value := make([]byte, valueSize)
		if _, err := io.ReadFull(r, value); err != nil {
			break
		}
		valueStr := string(value)

		switch key {
		case "Title":
			m.title = valueStr
		case "Artist":
			m.artist = valueStr
		case "Album":
			m.album = valueStr
		case "Year":
			if y, err := strconv.Atoi(valueStr); err == nil {
				m.year = y
			}
		case "Genre":
			m.genre = valueStr
		case "Comment":
			m.comment = valueStr
		}
	}

	return m, nil
}

type metadataAPE struct {
	title      string
	artist     string
	album      string
	year       int
	genre      string
	comment    string
	sampleRate int
	channels   int
	duration   time.Duration
}

func (m *metadataAPE) Format() Format     { return APEv2 }
func (m *metadataAPE) FileType() FileType  { return APE }
func (m *metadataAPE) Title() string        { return m.title }
func (m *metadataAPE) Album() string        { return m.album }
func (m *metadataAPE) Artist() string       { return m.artist }
func (m *metadataAPE) AlbumArtist() string  { return m.artist }
func (m *metadataAPE) Composer() string     { return "" }
func (m *metadataAPE) Year() int            { return m.year }
func (m *metadataAPE) Genre() string        { return m.genre }
func (m *metadataAPE) Track() (int, int)    { return 0, 0 }
func (m *metadataAPE) Disc() (int, int)     { return 0, 0 }
func (m *metadataAPE) Picture() *Picture    { return nil }
func (m *metadataAPE) Lyrics() string       { return "" }
func (m *metadataAPE) Comment() string      { return m.comment }
func (m *metadataAPE) Duration() time.Duration { return m.duration }
func (m *metadataAPE) SampleRate() int      { return m.sampleRate }
func (m *metadataAPE) BitRate() int {
	if m.sampleRate == 0 || m.channels == 0 {
		return 0
	}
	return m.sampleRate * m.channels * 16 / 1000
}
func (m *metadataAPE) Raw() map[string]interface{} {
	return map[string]interface{}{
		"sample_rate": m.sampleRate,
		"channels":    m.channels,
	}
}
