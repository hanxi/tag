package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteAIFF writes metadata into an AIFF/AIFF-C file using an ID3v2.3 tag
// stored in an "ID3 " chunk. Also writes NAME/AUTH native text chunks for
// basic compatibility with players that don't read ID3.
//
// Strategy: read existing file, preserve all audio/non-metadata chunks,
// replace ID3/NAME/AUTH/ANNO chunks with new ones, write atomically.
func WriteAIFF(filePath string, opts WriteOptions) error {
	src, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open aiff: %w", err)
	}
	defer src.Close()

	chunks, formType, err := parseAIFFChunks(src)
	if err != nil {
		return fmt.Errorf("parse aiff: %w", err)
	}

	id3Payload, err := buildAIFFID3Tag(opts)
	if err != nil {
		return fmt.Errorf("build id3: %w", err)
	}

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

	// Placeholder FORM header (will be rewritten with correct size at the end)
	if _, err := tmp.Write(make([]byte, 12)); err != nil {
		cleanup()
		return fmt.Errorf("write placeholder: %w", err)
	}

	// Write non-metadata chunks from original file
	for _, c := range chunks {
		if isAIFFMetadataChunk(c.id) {
			continue
		}
		if err := writeAIFFChunk(tmp, src, c); err != nil {
			cleanup()
			return fmt.Errorf("write chunk %s: %w", c.id, err)
		}
	}

	// Write native text chunks
	if opts.Title != "" {
		if err := writeAIFFTextChunk(tmp, "NAME", opts.Title); err != nil {
			cleanup()
			return fmt.Errorf("write NAME: %w", err)
		}
	}
	if opts.Artist != "" {
		if err := writeAIFFTextChunk(tmp, "AUTH", opts.Artist); err != nil {
			cleanup()
			return fmt.Errorf("write AUTH: %w", err)
		}
	}

	// Write ID3 chunk
	if len(id3Payload) > 0 {
		if err := writeAIFFRawChunk(tmp, "ID3 ", id3Payload); err != nil {
			cleanup()
			return fmt.Errorf("write ID3: %w", err)
		}
	}

	// Rewrite FORM header with correct size
	endPos, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		cleanup()
		return fmt.Errorf("seek end: %w", err)
	}
	formSize := uint32(endPos - 8) // FORM size excludes "FORM" + size field itself
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return fmt.Errorf("seek start: %w", err)
	}
	var header [12]byte
	copy(header[0:4], "FORM")
	binary.BigEndian.PutUint32(header[4:8], formSize)
	copy(header[8:12], formType)
	if _, err := tmp.Write(header[:]); err != nil {
		cleanup()
		return fmt.Errorf("rewrite header: %w", err)
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

type aiffChunk struct {
	id     string
	offset int64  // offset of chunk data (after id + size)
	size   uint32 // data size (not including pad byte)
}

func isAIFFMetadataChunk(id string) bool {
	switch id {
	case "ID3 ", "NAME", "AUTH", "ANNO":
		return true
	}
	return false
}

// parseAIFFChunks reads the FORM header and catalogs all chunks.
func parseAIFFChunks(r io.ReadSeeker) ([]aiffChunk, string, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}

	magic, err := readString(r, 4)
	if err != nil {
		return nil, "", err
	}
	if magic != "FORM" {
		return nil, "", fmt.Errorf("not an AIFF file: %s", magic)
	}

	// skip FORM size
	if _, err := r.Seek(4, io.SeekCurrent); err != nil {
		return nil, "", err
	}

	formType, err := readString(r, 4)
	if err != nil {
		return nil, "", err
	}
	if formType != "AIFF" && formType != "AIFC" {
		return nil, "", fmt.Errorf("not AIFF/AIFC: %s", formType)
	}

	var chunks []aiffChunk
	for {
		chunkID, err := readString(r, 4)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, "", err
		}

		chunkSize, err := readUint32BigEndian(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, "", err
		}

		dataOffset, _ := r.Seek(0, io.SeekCurrent)
		chunks = append(chunks, aiffChunk{
			id:     chunkID,
			offset: dataOffset,
			size:   chunkSize,
		})

		// Skip chunk data + pad byte
		skip := int64(chunkSize)
		if chunkSize%2 == 1 {
			skip++
		}
		if _, err := r.Seek(skip, io.SeekCurrent); err != nil {
			if err == io.EOF {
				break
			}
			return nil, "", err
		}
	}

	return chunks, formType, nil
}

// writeAIFFChunk copies a chunk from source to destination.
func writeAIFFChunk(dst io.Writer, src io.ReadSeeker, c aiffChunk) error {
	// Write chunk header
	var header [8]byte
	copy(header[0:4], c.id)
	binary.BigEndian.PutUint32(header[4:8], c.size)
	if _, err := dst.Write(header[:]); err != nil {
		return err
	}

	// Copy chunk data
	if _, err := src.Seek(c.offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.CopyN(dst, src, int64(c.size)); err != nil {
		return err
	}

	// Pad byte
	if c.size%2 == 1 {
		if _, err := dst.Write([]byte{0}); err != nil {
			return err
		}
	}
	return nil
}

// writeAIFFTextChunk writes a text chunk (NAME/AUTH/ANNO).
func writeAIFFTextChunk(w io.Writer, id string, text string) error {
	data := []byte(text)
	var header [8]byte
	copy(header[0:4], id)
	binary.BigEndian.PutUint32(header[4:8], uint32(len(data)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if len(data)%2 == 1 {
		if _, err := w.Write([]byte{0}); err != nil {
			return err
		}
	}
	return nil
}

// writeAIFFRawChunk writes a chunk with pre-built payload.
func writeAIFFRawChunk(w io.Writer, id string, payload []byte) error {
	var header [8]byte
	copy(header[0:4], id)
	binary.BigEndian.PutUint32(header[4:8], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if len(payload)%2 == 1 {
		if _, err := w.Write([]byte{0}); err != nil {
			return err
		}
	}
	return nil
}

// buildAIFFID3Tag builds a complete ID3v2.3 tag (header + frames) for embedding
// in an AIFF "ID3 " chunk.
func buildAIFFID3Tag(opts WriteOptions) ([]byte, error) {
	frames, err := buildID3v2Frames(opts)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, nil
	}

	var buf bytes.Buffer
	// ID3v2.3 header
	header := [10]byte{'I', 'D', '3', 0x03, 0x00, 0x00}
	sz := encodeSyncSafe(uint32(len(frames)))
	copy(header[6:], sz[:])
	buf.Write(header[:])
	buf.Write(frames)
	return buf.Bytes(), nil
}
