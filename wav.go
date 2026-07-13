package tag

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"time"
)

// ReadWAVMeta reads WAV metadata from the io.ReadSeeker, returning the resulting
// metadata in a Metadata implementation, or non-nil error if there was a problem.
func ReadWAVMeta(r io.ReadSeeker) (Metadata, error) {
	// verify RIFF chunk
	str, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if str != "RIFF" {
		return nil, fmt.Errorf("chunk header %v does not match expected 'RIFF'", str)
	}

	// skip file size (4 bytes)
	_, err = r.Seek(4, io.SeekCurrent)
	if err != nil {
		return nil, err
	}

	// verify WAVE filetype
	str, err = readString(r, 4)
	if err != nil {
		return nil, err
	}
	if str != "WAVE" {
		return nil, fmt.Errorf("filetype %v does not match expected 'WAVE'", str)
	}

	m := &metadataWAV{}

	// Parse chunks to find fmt and data chunks
	for {
		chunkID, err := readString(r, 4)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		chunkSize, err := readUint32LittleEndian(r)
		if err != nil {
			return nil, err
		}

		switch chunkID {
		case "LIST":
			listType, err := readString(r, 4)
			if err != nil {
				return nil, err
			}
			if listType == "INFO" {
				if err := m.readLISTInfo(r, int64(chunkSize)-4); err != nil {
					return nil, err
				}
				continue
			}
			// Other LIST types: skip
			_, err = r.Seek(int64(chunkSize)-4, io.SeekCurrent)
			if err != nil {
				return nil, err
			}
			continue

		case "ID3 ":
			if err := m.readID3Chunk(r, chunkSize); err != nil {
				return nil, err
			}
		case "fmt ":
			err = m.readFmtChunk(r, chunkSize)
			if err != nil {
				return nil, err
			}
		case "data":
			m.dataSize = chunkSize
			// Calculate duration now that we have both fmt and data info
			if m.sampleRate > 0 && m.bitsPerSample > 0 && m.channels > 0 {
				bytesPerSample := (m.bitsPerSample + 7) / 8 // Round up to nearest byte
				bytesPerSecond := m.sampleRate * uint32(m.channels) * uint32(bytesPerSample)
				if bytesPerSecond > 0 {
					m.duration = time.Duration(m.dataSize) * time.Second / time.Duration(bytesPerSecond)
				}
			}
			// Skip the data chunk content
			_, err = r.Seek(int64(chunkSize), io.SeekCurrent)
			if err != nil {
				return nil, err
			}
		default:
			// Skip unknown chunks
			_, err = r.Seek(int64(chunkSize), io.SeekCurrent)
			if err != nil {
				return nil, err
			}
		}

		// Ensure we're aligned to even byte boundary (WAV chunks are word-aligned)
		if chunkSize%2 == 1 {
			_, err = r.Seek(1, io.SeekCurrent)
			if err != nil {
				return nil, err
			}
		}
	}

	return m, nil
}

type metadataWAV struct {
	sampleRate    uint32
	bitsPerSample uint16
	channels      uint16
	dataSize      uint32
	duration      time.Duration
	format        Format
	title         string
	artist        string
	album         string
	albumArtist   string
	composer      string
	year          string
	genre         string
	track         int
	trackTotal    int
	disc          int
	discTotal     int
	picture       *Picture
	lyrics        string
	comment       string
}

func (m *metadataWAV) readLISTInfo(r io.ReadSeeker, size int64) error {
	endPos, _ := r.Seek(0, io.SeekCurrent)
	endPos += size
	for {
		cur, _ := r.Seek(0, io.SeekCurrent)
		if cur >= endPos {
			break
		}
		id, err := readString(r, 4)
		if err != nil {
			return nil
		}
		subSize, err := readUint32LittleEndian(r)
		if err != nil {
			return nil
		}
		data := make([]byte, subSize)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil
		}
		str := fixEncoding(bytes.TrimRight(data, "\x00"))
		switch id {
		case "INAM":
			m.title = str
		case "IART":
			m.artist = str
		case "IPRD":
			m.album = str
		case "ICRD":
			m.year = str
		case "IGNR":
			m.genre = str
		case "ICMT":
			m.comment = str
		}
		// Word-aligned padding
		if subSize%2 == 1 {
			if _, err := r.Seek(1, io.SeekCurrent); err != nil {
				return nil
			}
		}
	}
	return nil
}

func (m *metadataWAV) readID3Chunk(r io.ReadSeeker, chunkSize uint32) error {
	startPos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}

	id3Meta, err := ReadID3v2Tags(r)
	if err == nil {
		m.mergeID3(id3Meta)
	}

	_, err = r.Seek(startPos+int64(chunkSize), io.SeekStart)
	return err
}

func (m *metadataWAV) mergeID3(id3Meta *metadataID3v2) {
	m.format = id3Meta.Format()
	if t := id3Meta.Title(); t != "" {
		m.title = t
	}
	if a := id3Meta.Artist(); a != "" {
		m.artist = a
	}
	if a := id3Meta.Album(); a != "" {
		m.album = a
	}
	if a := id3Meta.AlbumArtist(); a != "" {
		m.albumArtist = a
	}
	if c := id3Meta.Composer(); c != "" {
		m.composer = c
	}
	if y := id3Meta.Year(); y != 0 {
		m.year = strconv.Itoa(y)
	}
	if g := id3Meta.Genre(); g != "" {
		m.genre = g
	}
	if t, tt := id3Meta.Track(); t != 0 {
		m.track = t
		m.trackTotal = tt
	}
	if d, dt := id3Meta.Disc(); d != 0 {
		m.disc = d
		m.discTotal = dt
	}
	if p := id3Meta.Picture(); p != nil {
		m.picture = p
	}
	if l := id3Meta.Lyrics(); l != "" {
		m.lyrics = l
	}
	if c := id3Meta.Comment(); c != "" {
		m.comment = c
	}
}

func (m *metadataWAV) readFmtChunk(r io.ReadSeeker, chunkSize uint32) error {
	// Read audio format (2 bytes) - should be 1 for PCM
	audioFormat, err := readUint16LittleEndian(r)
	if err != nil {
		return err
	}

	// Read number of channels (2 bytes)
	m.channels, err = readUint16LittleEndian(r)
	if err != nil {
		return err
	}

	// Read sample rate (4 bytes)
	m.sampleRate, err = readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	// Skip byte rate (4 bytes) and block align (2 bytes)
	_, err = r.Seek(6, io.SeekCurrent)
	if err != nil {
		return err
	}

	// Read bits per sample (2 bytes)
	m.bitsPerSample, err = readUint16LittleEndian(r)
	if err != nil {
		return err
	}

	// Skip any remaining bytes in the fmt chunk (for non-PCM formats)
	remainingBytes := int64(chunkSize) - 16
	if remainingBytes > 0 {
		_, err = r.Seek(remainingBytes, io.SeekCurrent)
		if err != nil {
			return err
		}
	}

	// Basic validation
	if audioFormat != 1 {
		return fmt.Errorf("unsupported audio format: %d (only PCM format 1 is supported)", audioFormat)
	}

	return nil
}

func (m *metadataWAV) Format() Format {
	if m.format != "" {
		return m.format
	}
	return UnknownFormat // WAV files don't have a standard metadata format
}

func (m *metadataWAV) FileType() FileType {
	return WAV
}

func (m *metadataWAV) Title() string { return m.title }

func (m *metadataWAV) Album() string { return m.album }

func (m *metadataWAV) Artist() string { return m.artist }

func (m *metadataWAV) AlbumArtist() string { return m.albumArtist }

func (m *metadataWAV) Composer() string { return m.composer }

func (m *metadataWAV) Year() int {
	if y, err := strconv.Atoi(m.year); err == nil {
		return y
	}
	return 0
}

func (m *metadataWAV) Genre() string { return m.genre }

// RIFF INFO has no standard language/style key, so these are always empty.
func (m *metadataWAV) Language() string { return "" }
func (m *metadataWAV) Style() string    { return "" }

func (m *metadataWAV) Track() (int, int) {
	return m.track, m.trackTotal
}

func (m *metadataWAV) Disc() (int, int) {
	return m.disc, m.discTotal
}

func (m *metadataWAV) Picture() *Picture {
	return m.picture
}

func (m *metadataWAV) Lyrics() string { return m.lyrics }

func (m *metadataWAV) Comment() string { return m.comment }

func (m *metadataWAV) Raw() map[string]interface{} {
	return map[string]interface{}{
		"sample_rate":     m.sampleRate,
		"bits_per_sample": m.bitsPerSample,
		"channels":        m.channels,
		"data_size":       m.dataSize,
	}
}

func (m *metadataWAV) Duration() time.Duration {
	return m.duration
}

func (m *metadataWAV) SampleRate() int {
	return int(m.sampleRate)
}

// BitRate 返回 PCM WAV 的恒定 bitrate(kbps)。
func (m *metadataWAV) BitRate() int {
	if m.sampleRate == 0 || m.bitsPerSample == 0 || m.channels == 0 {
		return 0
	}
	return int(m.sampleRate) * int(m.bitsPerSample) * int(m.channels) / 1000
}

func setWavOffset(r io.ReadSeeker) error {
	// verify RIFF chunk
	str, err := readString(r, 4)
	if err != nil {
		return err
	}
	if str != "RIFF" {
		return fmt.Errorf("chunk header %v does not match expected 'RIFF'", str)
	}

	// verify WAVE filetype
	_, err = r.Seek(4, io.SeekCurrent)
	if err != nil {
		return err
	}
	str, err = readString(r, 4)
	if err != nil {
		return err
	}
	if str != "WAVE" {
		return fmt.Errorf("filetype %v does not match exptected 'WAVE'", str)
	}

	// identify chunk length
	_, err = r.Seek(24, io.SeekCurrent) // 24-byte data format chunk is unneeded
	if err != nil {
		return err
	}
	str, err = readString(r, 4)
	if err != nil {
		return err
	}
	if str != "data" {
		return fmt.Errorf("identifier %v does not match expected 'data'", err)
	}
	dataSize, err := readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	_, err = r.Seek(int64(dataSize), io.SeekCurrent)
	if err != nil {
		return err
	}

	// skip unneeded 8-byte RIFF chunk header (4-byte ASCII identifier
	// and 4-byte little-endian uint32 chunk size), more info:
	// https://en.wikipedia.org/wiki/Resource_Interchange_File_Format#Explanation
	_, err = r.Seek(8, io.SeekCurrent)
	if err != nil {
		return err
	}

	return nil
}
