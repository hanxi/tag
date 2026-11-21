package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"time"
)

// ReadWAVMeta reads WAV metadata from the io.ReadSeeker, returning the resulting
// metadata in a Metadata implementation, or non-nil error if there was a problem.
func ReadWAVMeta(r io.ReadSeeker) (Metadata, error) {
	size, err := getFileSize(r)
	if err != nil {
		return nil, err
	}
	h, err := readAt(r, 0, 12)
	if err != nil {
		return nil, err
	}
	if string(h[:4]) != "RIFF" || string(h[8:12]) != "WAVE" || int64(binary.LittleEndian.Uint32(h[4:8])) != size-8 {
		return nil, fmt.Errorf("invalid WAVE RIFF header")
	}
	m := &metadataWAV{}
	var hasFormat, hasData bool
	var id3 []*metadataID3v2
	err = walkChunks(r, 12, size, false, func(c fileChunk) error {
		switch c.id {
		case "LIST":
			if c.size < 4 {
				return io.ErrUnexpectedEOF
			}
			kind, err := readAt(r, c.data, 4)
			if err != nil {
				return err
			}
			if string(kind) == "INFO" {
				return m.readLISTInfo(r, c.size-4)
			}
			if string(kind) == "wavl" {
				return walkChunks(r, c.data+4, c.data+c.size, false, func(child fileChunk) error {
					if child.id == "data" {
						hasData = true
						m.dataSize += uint32(child.size)
					}
					return nil
				})
			}
		case "ID3 ", "id3 ", "id3\x00":
			tags, err := readOptionalID3(r, c.data, c.size)
			if err != nil {
				return err
			}
			id3 = append(id3, tags)
		case "fmt ":
			if c.size < 16 {
				return io.ErrUnexpectedEOF
			}
			hasFormat = true
			if _, err := r.Seek(c.data, io.SeekStart); err != nil {
				return err
			}
			return m.readFmtChunk(r, uint32(c.size))
		case "data":
			hasData = true
			m.dataSize += uint32(c.size)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !hasFormat || !hasData {
		return nil, fmt.Errorf("missing WAVE format or data")
	}
	if m.byteRate > 0 {
		m.duration = sampleDuration(uint64(m.dataSize), int(m.byteRate))
	} else if m.sampleRate > 0 && m.bitsPerSample > 0 && m.channels > 0 {
		rate := uint64(m.sampleRate) * uint64(m.channels) * uint64((m.bitsPerSample+7)/8)
		if rate < uint64(1<<31) {
			m.duration = sampleDuration(uint64(m.dataSize), int(rate))
		}
	}
	for _, tags := range id3 {
		m.mergeID3(tags)
	}
	return m, nil
}

type metadataWAV struct {
	language, style string
	sampleRate      uint32
	byteRate        uint32
	bitsPerSample   uint16
	channels        uint16
	dataSize        uint32
	duration        time.Duration
	format          Format
	title           string
	artist          string
	artists         []string
	album           string
	albumArtist     string
	albumArtists    []string
	composer        string
	year            string
	genre           string
	track           int
	trackTotal      int
	disc            int
	discTotal       int
	picture         *Picture
	lyrics          string
	comment         string
}

func (m *metadataWAV) readLISTInfo(r io.ReadSeeker, size int64) error {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	return walkChunks(r, start, start+size, false, func(c fileChunk) error {
		switch c.id {
		case "INAM", "IART", "IPRD", "ICRD", "IGNR", "ITRK", "ICMT":
			data, err := chunkData(r, c)
			if err != nil {
				return err
			}
			value := fixEncoding(bytes.TrimRight(data, "\x00"))
			switch c.id {
			case "INAM":
				m.title = value
			case "IART":
				m.artist = value
			case "IPRD":
				m.album = value
			case "ICRD":
				m.year = value
			case "IGNR":
				m.genre = value
			case "ITRK":
				m.track, m.trackTotal = parseXofN(value)
			case "ICMT":
				m.comment = value
			}
		}
		return nil
	})
}

func (m *metadataWAV) mergeID3(id3Meta *metadataID3v2) {
	m.format = id3Meta.Format()
	m.language = id3Meta.Language()
	m.style = id3Meta.Style()
	if t := id3Meta.Title(); t != "" {
		m.title = t
	}
	if a := id3Meta.Artist(); a != "" {
		m.artist = a
	}
	if as := id3Meta.Artists(); len(as) > 0 {
		m.artists = as
	}
	if a := id3Meta.Album(); a != "" {
		m.album = a
	}
	if a := id3Meta.AlbumArtist(); a != "" {
		m.albumArtist = a
	}
	if as := id3Meta.AlbumArtists(); len(as) > 0 {
		m.albumArtists = as
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
	// Read audio format (2 bytes). Common values: 1 = PCM, 3 = IEEE float,
	// 0xFFFE = WAVE_FORMAT_EXTENSIBLE. We do NOT reject non-PCM formats here:
	// the fmt chunk only provides sampleRate/channels/bits/byteRate used for
	// duration, while textual tags live in a separate LIST/ID3 chunk. Failing
	// here would abort the whole read and lose those tags (e.g. GBK RIFF INFO
	// on 24-bit/192kHz WAVE_FORMAT_EXTENSIBLE files), forcing a garbled ffprobe
	// fallback. See songloft-org/songloft#319.
	_, err := readUint16LittleEndian(r)
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

	// Read byte rate / nAvgBytesPerSec (4 bytes) — authoritative for duration
	m.byteRate, err = readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	// Skip block align (2 bytes)
	_, err = r.Seek(2, io.SeekCurrent)
	if err != nil {
		return err
	}

	// Read bits per sample (2 bytes)
	m.bitsPerSample, err = readUint16LittleEndian(r)
	if err != nil {
		return err
	}

	// Skip any remaining bytes in the fmt chunk (extension/GUID for non-PCM formats)
	remainingBytes := int64(chunkSize) - 16
	if remainingBytes > 0 {
		_, err = r.Seek(remainingBytes, io.SeekCurrent)
		if err != nil {
			return err
		}
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
func (m *metadataWAV) Artists() []string {
	if len(m.artists) > 0 {
		return m.artists
	}
	return singleArtistList(m.artist)
}

func (m *metadataWAV) AlbumArtist() string { return m.albumArtist }
func (m *metadataWAV) AlbumArtists() []string {
	if len(m.albumArtists) > 0 {
		return m.albumArtists
	}
	return singleArtistList(m.albumArtist)
}

func (m *metadataWAV) Composer() string { return m.composer }

func (m *metadataWAV) Year() int {
	if y, err := strconv.Atoi(m.year); err == nil {
		return y
	}
	return 0
}

func (m *metadataWAV) Genre() string { return m.genre }

// RIFF INFO has no standard language/style key, so these are always empty.
func (m *metadataWAV) Language() string { return m.language }
func (m *metadataWAV) Style() string    { return m.style }

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
