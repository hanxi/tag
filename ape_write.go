package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

	stripped := stripAPETagEX(original)
	items, itemCount := buildAPEv2Items(opts)

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

	if _, err := tmp.Write(stripped); err != nil {
		cleanup()
		return fmt.Errorf("write audio data: %w", err)
	}

	if _, err := tmp.Write(items); err != nil {
		cleanup()
		return fmt.Errorf("write tag items: %w", err)
	}

	// APEv2 spec: tagSize = items + footer (32 bytes)
	footer := buildAPETagEXFooter(uint32(len(items))+32, itemCount)
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
	// APEv2 spec: tagSize includes footer (32 bytes) + all items
	tagSize := binary.LittleEndian.Uint32(data[footerStart+12 : footerStart+16])
	audioEnd := int64(len(data)) - int64(tagSize)
	if audioEnd < 0 || audioEnd > int64(len(data)) {
		return data
	}
	return data[:audioEnd]
}

// buildAPEv2Items encodes APEv2 tag items from WriteOptions.
// Returns the encoded bytes and the number of items.
func buildAPEv2Items(opts WriteOptions) ([]byte, uint32) {
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
	if opts.Track != "" {
		entries = append(entries, item{"Track", opts.Track})
	}
	if opts.Lyrics != "" {
		entries = append(entries, item{"Lyrics", opts.Lyrics})
	}

	var buf bytes.Buffer
	var totalItems uint32
	for _, e := range entries {
		var sizeBuf [4]byte
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(e.value)))
		buf.Write(sizeBuf[:])
		binary.LittleEndian.PutUint32(sizeBuf[:], 0) // flags = 0 (UTF-8 text)
		buf.Write(sizeBuf[:])
		buf.WriteString(e.key)
		buf.WriteByte(0) // null terminator
		buf.WriteString(e.value)
		totalItems++
	}

	// 封面：APEv2 binary item, key="Cover Art (Front)", value=[filename\0][image data]
	if opts.Picture != nil && len(opts.Picture.Data) > 0 {
		ext := "jpg"
		if strings.Contains(opts.Picture.MIMEType, "png") {
			ext = "png"
		}
		filename := "cover." + ext
		valueLen := len(filename) + 1 + len(opts.Picture.Data)
		var sizeBuf [4]byte
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(valueLen))
		buf.Write(sizeBuf[:])
		binary.LittleEndian.PutUint32(sizeBuf[:], 0x02) // flags: bits 1-2 = 1 (binary)
		buf.Write(sizeBuf[:])
		buf.WriteString("Cover Art (Front)")
		buf.WriteByte(0) // key null terminator
		buf.WriteString(filename)
		buf.WriteByte(0) // filename null terminator
		buf.Write(opts.Picture.Data)
		totalItems++
	}

	return buf.Bytes(), totalItems
}

// buildAPETagEXFooter builds a 32-byte APETAGEX footer block.
// tagSize must include items + footer (32 bytes) per APEv2 spec.
func buildAPETagEXFooter(tagSize, itemCount uint32) []byte {
	var buf [32]byte
	copy(buf[0:8], "APETAGEX")
	binary.LittleEndian.PutUint32(buf[8:12], 2000) // APEv2
	binary.LittleEndian.PutUint32(buf[12:16], tagSize)
	binary.LittleEndian.PutUint32(buf[16:20], itemCount)
	binary.LittleEndian.PutUint32(buf[20:24], 0) // flags: this is a footer, no header
	return buf[:]
}
