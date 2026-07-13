package tag

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"time"
)

// ReadAIFFMeta reads AIFF/AIFF-C metadata from the io.ReadSeeker.
func ReadAIFFMeta(r io.ReadSeeker) (Metadata, error) {
	str, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if str != "FORM" {
		return nil, fmt.Errorf("chunk header %v does not match expected 'FORM'", str)
	}

	// file size (big-endian, 4 bytes) — skip
	_, err = r.Seek(4, io.SeekCurrent)
	if err != nil {
		return nil, err
	}

	formType, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if formType != "AIFF" && formType != "AIFC" {
		return nil, fmt.Errorf("form type %v does not match expected 'AIFF' or 'AIFC'", formType)
	}

	m := &metadataAIFF{}

	for {
		chunkID, err := readString(r, 4)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		chunkSize, err := readUint32BigEndian(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		switch chunkID {
		case "COMM":
			if err := m.readCOMMChunk(r, chunkSize); err != nil {
				return nil, err
			}
		case "ID3 ":
			if err := m.readID3Chunk(r, chunkSize); err != nil {
				return nil, err
			}
		case "NAME":
			s, err := readChunkString(r, chunkSize)
			if err != nil {
				return nil, err
			}
			if m.title == "" {
				m.title = s
			}
		case "AUTH":
			s, err := readChunkString(r, chunkSize)
			if err != nil {
				return nil, err
			}
			if m.artist == "" {
				m.artist = s
			}
		case "ANNO":
			s, err := readChunkString(r, chunkSize)
			if err != nil {
				return nil, err
			}
			if m.comment == "" {
				m.comment = s
			}
		default:
			_, err = r.Seek(int64(chunkSize), io.SeekCurrent)
			if err != nil {
				return nil, err
			}
		}

		// IFF chunks are word-aligned (2-byte)
		if chunkSize%2 == 1 {
			_, err = r.Seek(1, io.SeekCurrent)
			if err != nil {
				if err == io.EOF {
					break
				}
				return nil, err
			}
		}
	}

	return m, nil
}

func readChunkString(r io.ReadSeeker, size uint32) (string, error) {
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return fixEncoding(bytes.TrimRight(b, "\x00")), nil
}

type metadataAIFF struct {
	channels        uint16
	sampleRate      uint32
	bitsPerSample   uint16
	numSampleFrames uint32
	duration        time.Duration
	title           string
	artist          string
	album           string
	albumArtist     string
	composer        string
	year            int
	genre           string
	language        string
	style           string
	track           int
	trackTotal      int
	disc            int
	discTotal       int
	picture         *Picture
	lyrics          string
	comment         string
}

func (m *metadataAIFF) readCOMMChunk(r io.ReadSeeker, chunkSize uint32) error {
	var err error
	m.channels, err = readUint16BigEndian(r)
	if err != nil {
		return err
	}

	m.numSampleFrames, err = readUint32BigEndian(r)
	if err != nil {
		return err
	}

	m.bitsPerSample, err = readUint16BigEndian(r)
	if err != nil {
		return err
	}

	// Sample rate is stored as 80-bit IEEE 754 extended precision
	extBytes := make([]byte, 10)
	if _, err := io.ReadFull(r, extBytes); err != nil {
		return err
	}
	m.sampleRate = uint32(parseIEEE754Extended(extBytes))

	if m.sampleRate > 0 && m.numSampleFrames > 0 {
		m.duration = time.Duration(m.numSampleFrames) * time.Second / time.Duration(m.sampleRate)
	}

	// Skip remaining bytes in COMM (AIFF-C has compression type + name after)
	remaining := int64(chunkSize) - 18
	if remaining > 0 {
		_, err = r.Seek(remaining, io.SeekCurrent)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *metadataAIFF) readID3Chunk(r io.ReadSeeker, chunkSize uint32) error {
	startPos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}

	id3Meta, err := ReadID3v2Tags(r)
	if err != nil {
		_, _ = r.Seek(startPos+int64(chunkSize), io.SeekStart)
		return nil
	}

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
		m.year = y
	}
	if g := id3Meta.Genre(); g != "" {
		m.genre = g
	}
	if l := id3Meta.Language(); l != "" {
		m.language = l
	}
	if s := id3Meta.Style(); s != "" {
		m.style = s
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

	_, err = r.Seek(startPos+int64(chunkSize), io.SeekStart)
	return err
}

// parseIEEE754Extended converts a 10-byte IEEE 754 extended precision float to float64.
func parseIEEE754Extended(b []byte) float64 {
	sign := int(b[0]) >> 7
	exponent := (int(b[0])&0x7F)<<8 | int(b[1])
	mantissa := uint64(0)
	for i := 2; i < 10; i++ {
		mantissa = mantissa<<8 | uint64(b[i])
	}

	if exponent == 0 && mantissa == 0 {
		return 0
	}
	if exponent == 0x7FFF {
		return math.Inf(1 - 2*sign)
	}

	// Bias for 80-bit extended is 16383
	f := float64(mantissa) / (1 << 63) * math.Pow(2, float64(exponent-16383))
	if sign == 1 {
		f = -f
	}
	return f
}

func (m *metadataAIFF) Format() Format {
	if m.title != "" || m.artist != "" || m.album != "" {
		return ID3v2_3
	}
	return UnknownFormat
}

func (m *metadataAIFF) FileType() FileType { return AIFF }
func (m *metadataAIFF) Title() string       { return m.title }
func (m *metadataAIFF) Album() string       { return m.album }
func (m *metadataAIFF) Artist() string      { return m.artist }
func (m *metadataAIFF) AlbumArtist() string { return m.albumArtist }
func (m *metadataAIFF) Composer() string    { return m.composer }
func (m *metadataAIFF) Year() int           { return m.year }
func (m *metadataAIFF) Genre() string       { return m.genre }
func (m *metadataAIFF) Language() string    { return m.language }
func (m *metadataAIFF) Style() string       { return m.style }
func (m *metadataAIFF) Track() (int, int)   { return m.track, m.trackTotal }
func (m *metadataAIFF) Disc() (int, int)    { return m.disc, m.discTotal }
func (m *metadataAIFF) Picture() *Picture   { return m.picture }
func (m *metadataAIFF) Lyrics() string      { return m.lyrics }
func (m *metadataAIFF) Comment() string     { return m.comment }

func (m *metadataAIFF) Raw() map[string]interface{} {
	raw := map[string]interface{}{
		"channels":         m.channels,
		"sample_rate":      m.sampleRate,
		"bits_per_sample":  m.bitsPerSample,
		"num_sample_frames": m.numSampleFrames,
	}
	return raw
}

func (m *metadataAIFF) Duration() time.Duration { return m.duration }

func (m *metadataAIFF) SampleRate() int { return int(m.sampleRate) }

func (m *metadataAIFF) BitRate() int {
	if m.sampleRate == 0 || m.bitsPerSample == 0 || m.channels == 0 {
		return 0
	}
	return int(m.sampleRate) * int(m.bitsPerSample) * int(m.channels) / 1000
}

