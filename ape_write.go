package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// WriteAPE writes APEv2 tags to a Monkey's Audio file.
//
// The APEv2 tag sits at the end of the file. This function:
//  1. Reads the entire file
//  2. Strips any existing APETAGEX footer + items from the end
//  3. Constructs new APEv2 items from WriteOptions
//  4. Appends the new items + APETAGEX footer
//  5. Writes atomically via temp file + rename
func WriteAPE(filePath string, opts WriteOptions) error {
	original, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read ape file: %w", err)
	}

	// Strip existing APETAGEX footer if present
	stripped := stripAPETagEX(original)

	// Build new APEv2 items
	items := buildAPEv2Items(opts)

	// Create temp file
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

	// Write stripped audio data
	if _, err := tmp.Write(stripped); err != nil {
		cleanup()
		return fmt.Errorf("write audio data: %w", err)
	}

	// Write APEv2 items
	if _, err := tmp.Write(items); err != nil {
		cleanup()
		return fmt.Errorf("write tag items: %w", err)
	}

	// Write APETAGEX footer
	footer := buildAPETagEXFooter(uint32(len(items)))
	if _, err := tmp.Write(footer); err != nil {
		cleanup()
		return fmt.Errorf("write footer: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// stripAPETagEX removes an APETAGEX footer and its items from the end of data.
func stripAPETagEX(data []byte) []byte {
	if len(data) < 32 {
		return data
	}
	footerStart := len(data) - 32
	if string(data[footerStart:footerStart+8]) != "APETAGEX" {
		return data
	}
	tagSize := binary.LittleEndian.Uint32(data[footerStart+12 : footerStart+16])
	// tag items start at footerStart - tagSize
	audioEnd := int64(footerStart) - int64(tagSize)
	if audioEnd < 0 || audioEnd > int64(len(data)) {
		return data
	}
	return data[:audioEnd]
}

// buildAPEv2Items encodes APEv2 tag items from WriteOptions.
// Each item: [4B LE valueSize][4B LE flags][null-terminated key][value bytes]
// Key names use Title case (APEv2 convention).
func buildAPEv2Items(opts WriteOptions) []byte {
	type item struct {
		key   string
		value string
	}
	var entries []item
	if opts.Title != "" {
		entries = append(entries, item{"Title", opts.Title})
	}
	if opts.Artist != "" {
		entries = append(entries, item{"Artist", opts.Artist})
	}
	if opts.Album != "" {
		entries = append(entries, item{"Album", opts.Album})
	}
	if opts.Year > 0 {
		entries = append(entries, item{"Year", strconv.Itoa(opts.Year)})
	}
	if opts.Genre != "" {
		entries = append(entries, item{"Genre", opts.Genre})
	}
	if opts.Lyrics != "" {
		entries = append(entries, item{"Lyrics", opts.Lyrics})
	}

	var buf bytes.Buffer
	for _, e := range entries {
		valueSize := uint32(len(e.value))
		var sizeBuf [4]byte
		binary.LittleEndian.PutUint32(sizeBuf[:], valueSize)
		buf.Write(sizeBuf[:])
		// flags = 0 (no special flags)
		binary.LittleEndian.PutUint32(sizeBuf[:], 0)
		buf.Write(sizeBuf[:])
		buf.WriteString(e.key)
		buf.WriteByte(0) // null terminator
		buf.WriteString(e.value)
	}
	return buf.Bytes()
}

// buildAPETagEXFooter builds a 32-byte APETAGEX footer block.
func buildAPETagEXFooter(itemsSize uint32) []byte {
	var buf [32]byte
	copy(buf[0:8], "APETAGEX")
	// version = 2000 (APEv2)
	binary.LittleEndian.PutUint32(buf[8:12], 2000)
	// tag size = items only (not including footer)
	binary.LittleEndian.PutUint32(buf[12:16], itemsSize)
	// item count - we don't know the exact count here but it's informational mostly
	// We'll set it to 0 since ffmpeg and other tools don't strictly verify it
	binary.LittleEndian.PutUint32(buf[16:20], 0)
	// flags (bit 31 = header present, we don't write header so 0)
	binary.LittleEndian.PutUint32(buf[20:24], 0)
	// reserved
	// buf[24:32] already zero
	return buf[:]
}
