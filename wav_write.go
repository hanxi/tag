package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// WriteWAV writes RIFF LIST INFO metadata to a WAV file.
//
// WAV files are RIFF containers. Metadata goes in a "LIST" chunk
// with subtype "INFO", containing sub-chunks like INAM (title),
// IART (artist), IPRD (album), ICRD (year), IGNR (genre), ICMT (lyrics).
//
// This function:
//  1. Validates the RIFF/WAVE header
//  2. Scans existing chunks, keeping all non-tag chunks
//  3. Builds a new LIST INFO chunk from WriteOptions
//  4. Rewrites the file atomically via temp file + rename
func WriteWAV(filePath string, opts WriteOptions) error {
	src, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open wav: %w", err)
	}
	defer src.Close()

	// Verify RIFF header
	var riffID [4]byte
	if _, err := io.ReadFull(src, riffID[:]); err != nil {
		return fmt.Errorf("read riff id: %w", err)
	}
	if string(riffID[:]) != "RIFF" {
		return fmt.Errorf("not a RIFF file")
	}

	// Skip RIFF size (4 bytes) — we'll recalculate later
	var riffSize uint32
	if err := binary.Read(src, binary.LittleEndian, &riffSize); err != nil {
		return fmt.Errorf("read riff size: %w", err)
	}
	_ = riffSize

	var waveID [4]byte
	if _, err := io.ReadFull(src, waveID[:]); err != nil {
		return fmt.Errorf("read wave id: %w", err)
	}
	if string(waveID[:]) != "WAVE" {
		return fmt.Errorf("not a WAVE file")
	}

	// Parse chunks
	type savedChunk struct {
		id   [4]byte
		data []byte
	}
	var chunks []savedChunk
	for {
		var chunkID [4]byte
		if _, err := io.ReadFull(src, chunkID[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return fmt.Errorf("read chunk id: %w", err)
		}
		var chunkSize uint32
		if err := binary.Read(src, binary.LittleEndian, &chunkSize); err != nil {
			return fmt.Errorf("read chunk size: %w", err)
		}

		// Skip old LIST INFO chunks (we'll write a new one)
		if string(chunkID[:]) == "LIST" {
			var listType [4]byte
			dataStart, _ := src.Seek(0, io.SeekCurrent)
			if _, err := io.ReadFull(src, listType[:]); err != nil {
				return fmt.Errorf("read list type: %w", err)
			}
			if string(listType[:]) == "INFO" {
				// Skip this chunk entirely
				if _, err := src.Seek(int64(chunkSize)-4, io.SeekCurrent); err != nil {
					return fmt.Errorf("skip list info: %w", err)
				}
				continue
			}
			// Not INFO — seek back to data start and read the full chunk payload
			_, _ = src.Seek(dataStart, io.SeekStart)
		}

		data := make([]byte, chunkSize)
		if _, err := io.ReadFull(src, data); err != nil {
			return fmt.Errorf("read chunk data: %w", err)
		}
		chunks = append(chunks, savedChunk{chunkID, data})

		// Skip padding byte (WAV chunks are word-aligned)
		if chunkSize%2 == 1 {
			if _, err := src.Seek(1, io.SeekCurrent); err != nil {
				break
			}
		}
	}

	// Build the new LIST INFO chunk
	infoData := buildWAVLISTInfo(opts)

	// Calculate total size: RIFF header(12) + all chunks + padding
	var totalSize uint32 = 4 // "WAVE"
	for _, c := range chunks {
		totalSize += 8 + uint32(len(c.data)) // 4B id + 4B size + data
		if len(c.data)%2 == 1 {
			totalSize++ // padding
		}
	}
	totalSize += 8 + uint32(len(infoData)) // LIST INFO (id + size)
	if len(infoData)%2 == 1 {
		totalSize++
	}
	// RIFF size = totalSize (everything after "RIFF" and the 4B size field itself)
	// The RIFF size field contains the size of everything after it = totalSize

	// Write temp file
	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".songloft-tag-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	var buf bytes.Buffer
	// RIFF header
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, totalSize)
	buf.WriteString("WAVE")

	// Existing chunks
	for _, c := range chunks {
		buf.Write(c.id[:])
		writeWAVChunk(&buf, c.data)
	}

	// New LIST INFO
	buf.WriteString("LIST")
	writeWAVChunk(&buf, infoData)

	// Write to temp file
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := src.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close src: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// writeWAVChunk writes a WAV chunk header + data with word-alignment padding.
func writeWAVChunk(buf *bytes.Buffer, data []byte) {
	binary.Write(buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
	if len(data)%2 == 1 {
		buf.WriteByte(0) // padding
	}
}

// buildWAVLISTInfo builds the INFO sub-chunk data for a LIST chunk.
// Format: "INFO" + [INAM][IART][IPRD][ICRD][IGNR][ICMT]...
func buildWAVLISTInfo(opts WriteOptions) []byte {
	type wavTag struct {
		id  string
		val string
	}
	var tags []wavTag
	if opts.Title != "" {
		tags = append(tags, wavTag{"INAM", opts.Title})
	}
	if opts.Artist != "" {
		tags = append(tags, wavTag{"IART", opts.Artist})
	}
	if opts.Album != "" {
		tags = append(tags, wavTag{"IPRD", opts.Album})
	}
	if opts.Year > 0 {
		tags = append(tags, wavTag{"ICRD", strconv.Itoa(opts.Year)})
	}
	if opts.Genre != "" {
		tags = append(tags, wavTag{"IGNR", opts.Genre})
	}
	if opts.Lyrics != "" {
		tags = append(tags, wavTag{"ICMT", opts.Lyrics})
	}

	var buf bytes.Buffer
	buf.WriteString("INFO") // LIST subtype
	for _, t := range tags {
		buf.WriteString(t.id)
		binary.Write(&buf, binary.LittleEndian, uint32(len(t.val)))
		buf.WriteString(t.val)
		if len(t.val)%2 == 1 {
			buf.WriteByte(0) // padding
		}
	}
	return buf.Bytes()
}
