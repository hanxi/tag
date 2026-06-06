package tag

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ReadAPEMeta reads Monkey's Audio metadata from the io.ReadSeeker.
//
// APE files start with "MAC " magic at offset 0.
// For version >= 3.98 (3980) the file has a 52-byte APE_DESCRIPTOR
// followed by a separate APE_HEADER; older versions use a flat header.
// Text tags are stored in an APEv2 footer at the end of the file.
func ReadAPEMeta(r io.ReadSeeker) (Metadata, error) {
	magic, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if magic != "MAC " {
		return nil, errors.New("expected 'MAC ' magic for Monkey's Audio")
	}

	version, err := readUint16LittleEndian(r)
	if err != nil {
		return nil, fmt.Errorf("read ape version: %w", err)
	}

	var channels, bitsPerSample uint16
	var sampleRate, blocksPerFrame, finalFrameBlocks, totalFrames uint32
	var fileSize int64

	if version >= 3980 {
		// --- APE_DESCRIPTOR (52 bytes total, 46 remaining after magic+version) ---
		// padding(2) + descriptorBytes(4) + headerBytes(4) + seekTableBytes(4)
		// + wavHeaderBytes(4) + apeFrameDataBytes(4) + apeFrameDataBytesHigh(4)
		// + terminatingDataBytes(4) + fileMD5(16) = 46 bytes
		if _, err := r.Seek(46, io.SeekCurrent); err != nil {
			return nil, fmt.Errorf("skip ape descriptor: %w", err)
		}

		// --- APE_HEADER ---
		// compressionType(2) + formatFlags(2) + blocksPerFrame(4) + finalFrameBlocks(4)
		// + totalFrames(4) + bitsPerSample(2) + channels(2) + sampleRate(4)
		if _, err := readUint16LittleEndian(r); err != nil { // compressionType
			return nil, fmt.Errorf("read compression type: %w", err)
		}
		if _, err := readUint16LittleEndian(r); err != nil { // formatFlags
			return nil, fmt.Errorf("read format flags: %w", err)
		}
		blocksPerFrame, err = readUint32LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read blocks per frame: %w", err)
		}
		finalFrameBlocks, err = readUint32LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read final frame blocks: %w", err)
		}
		totalFrames, err = readUint32LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read total frames: %w", err)
		}
		bitsPerSample, err = readUint16LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read bits per sample: %w", err)
		}
		channels, err = readUint16LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read channels: %w", err)
		}
		sampleRate, err = readUint32LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read sample rate: %w", err)
		}
	} else {
		// --- Old header (version < 3.98) ---
		// compressionType(2) + formatFlags(2) + channels(2) + sampleRate(4)
		// + wavHeaderBytes(4) + wavTailBytes(4) + totalFrames(4) + finalFrameBlocks(4)
		if _, err := readUint16LittleEndian(r); err != nil { // compressionType
			return nil, fmt.Errorf("read compression type: %w", err)
		}
		var formatFlags uint16
		formatFlags, err = readUint16LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read format flags: %w", err)
		}
		channels, err = readUint16LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read channels: %w", err)
		}
		sampleRate, err = readUint32LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read sample rate: %w", err)
		}
		// skip wavHeaderBytes(4) + wavTailBytes(4)
		if _, err := r.Seek(8, io.SeekCurrent); err != nil {
			return nil, fmt.Errorf("skip wav header/tail: %w", err)
		}
		totalFrames, err = readUint32LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read total frames: %w", err)
		}
		finalFrameBlocks, err = readUint32LittleEndian(r)
		if err != nil {
			return nil, fmt.Errorf("read final frame blocks: %w", err)
		}
		// blocksPerFrame depends on version and compression for old formats
		if version >= 3950 {
			blocksPerFrame = 73728 * 4
		} else if version >= 3900 || (version >= 3800 && formatFlags&8 /* MAC_FORMAT_FLAG_CREATE_WAV_HEADER */ == 0) {
			blocksPerFrame = 73728
		} else {
			blocksPerFrame = 9216
		}
		bitsPerSample = 16
	}

	// Calculate duration: totalSamples = (totalFrames-1)*blocksPerFrame + finalFrameBlocks
	var duration time.Duration
	if sampleRate > 0 && totalFrames > 0 {
		totalSamples := uint64(totalFrames-1)*uint64(blocksPerFrame) + uint64(finalFrameBlocks)
		duration = time.Duration(totalSamples) * time.Second / time.Duration(sampleRate)
	}

	fileSize, err = r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("seek to end: %w", err)
	}

	m := &metadataAPE{
		fileSize:      fileSize,
		bitsPerSample: int(bitsPerSample),
		sampleRate:    int(sampleRate),
		channels:      int(channels),
		duration:      duration,
	}

	// --- Parse APEv2 footer ---
	if fileSize < 32 {
		return m, nil
	}

	footerPos := fileSize - 32
	if _, err := r.Seek(footerPos, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to ape footer: %w", err)
	}

	footerID, err := readString(r, 8)
	if err != nil {
		return m, nil
	}
	if footerID != "APETAGEX" {
		return m, nil
	}

	if _, err := readUint32LittleEndian(r); err != nil { // version
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
	if _, err := readUint32LittleEndian(r); err != nil { // flags
		return m, nil
	}

	// APEv2 spec: tagSize includes footer (32 bytes) + all items, excluding header.
	// Items start at: fileSize - tagSize
	tagStart := fileSize - int64(tagSize)
	if tagStart < 0 {
		return m, nil
	}
	if _, err := r.Seek(tagStart, io.SeekStart); err != nil {
		return m, nil
	}

	for i := uint32(0); i < itemCount; i++ {
		valueSize, err := readUint32LittleEndian(r)
		if err != nil {
			break
		}
		if _, err := readUint32LittleEndian(r); err != nil { // itemFlags
			break
		}

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

		switch strings.ToLower(key) {
		case "title":
			m.title = valueStr
		case "artist":
			m.artist = valueStr
		case "album artist":
			m.albumArtist = valueStr
		case "album":
			m.album = valueStr
		case "year":
			if y, err := strconv.Atoi(valueStr); err == nil {
				m.year = y
			}
		case "genre":
			m.genre = valueStr
		case "lyrics":
			m.lyrics = valueStr
		case "comment":
			m.comment = valueStr
		}
	}

	return m, nil
}

type metadataAPE struct {
	title         string
	artist        string
	albumArtist   string
	album         string
	year          int
	genre         string
	lyrics        string
	comment       string
	fileSize      int64
	bitsPerSample int
	sampleRate    int
	channels      int
	duration      time.Duration
}

func (m *metadataAPE) Format() Format        { return APEv2 }
func (m *metadataAPE) FileType() FileType     { return APE }
func (m *metadataAPE) Title() string          { return m.title }
func (m *metadataAPE) Album() string          { return m.album }
func (m *metadataAPE) Artist() string         { return m.artist }
func (m *metadataAPE) AlbumArtist() string    { return m.albumArtist }
func (m *metadataAPE) Composer() string       { return "" }
func (m *metadataAPE) Year() int              { return m.year }
func (m *metadataAPE) Genre() string          { return m.genre }
func (m *metadataAPE) Track() (int, int)      { return 0, 0 }
func (m *metadataAPE) Disc() (int, int)       { return 0, 0 }
func (m *metadataAPE) Picture() *Picture      { return nil }
func (m *metadataAPE) Lyrics() string         { return m.lyrics }
func (m *metadataAPE) Comment() string        { return m.comment }
func (m *metadataAPE) Duration() time.Duration { return m.duration }
func (m *metadataAPE) SampleRate() int        { return m.sampleRate }

func (m *metadataAPE) BitRate() int {
	if m.duration == 0 || m.fileSize == 0 {
		return 0
	}
	return int(m.fileSize * 8 / 1000 / int64(m.duration.Seconds()))
}

func (m *metadataAPE) Raw() map[string]interface{} {
	return map[string]interface{}{
		"sample_rate":    m.sampleRate,
		"channels":       m.channels,
		"bits_per_sample": m.bitsPerSample,
	}
}
