// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

var atomTypes = map[int]string{
	0:  "implicit", // automatic based on atom name
	1:  "text",
	13: "jpeg",
	14: "png",
	21: "uint8",
}

// NB: atoms does not include "----", this is handled separately
var atoms = atomNames(map[string]string{
	"\xa9alb": "album",
	"\xa9art": "artist",
	"\xa9ART": "artist",
	"aART":    "album_artist",
	"\xa9day": "year",
	"\xa9nam": "title",
	"\xa9gen": "genre",
	"trkn":    "track",
	"\xa9wrt": "composer",
	"\xa9too": "encoder",
	"cprt":    "copyright",
	"covr":    "picture",
	"\xa9grp": "grouping",
	"keyw":    "keyword",
	"\xa9lyr": "lyrics",
	"\xa9cmt": "comment",
	"tmpo":    "tempo",
	"cpil":    "compilation",
	"disk":    "disc",
})

var means = map[string]bool{
	"com.apple.iTunes":          true,
	"com.mixedinkey.mixedinkey": true,
	"com.serato.dj":             true,
}

// Detect PNG image if "implicit" class is used
var pngHeader = []byte{137, 80, 78, 71, 13, 10, 26, 10}

type atomNames map[string]string

func (f atomNames) Name(n string) []string {
	res := make([]string, 1)
	for k, v := range f {
		if v == n {
			res = append(res, k)
		}
	}
	return res
}

// metadataMP4 is the implementation of Metadata for MP4 tag (atom) data.
type metadataMP4 struct {
	fileType FileType
	data     map[string]interface{}
	duration time.Duration
}

// ReadAtoms reads MP4 metadata atoms from the io.ReadSeeker into a Metadata, returning
// non-nil error if there was a problem.
func ReadAtoms(r io.ReadSeeker) (Metadata, error) {
	m := metadataMP4{
		data:     make(map[string]interface{}),
		fileType: UnknownFileType,
	}
	size, err := getFileSize(r)
	if err != nil {
		return nil, err
	}
	var haveMovie bool
	err = walkMP4(r, 0, size, func(c fileChunk) error {
		switch c.id {
		case "ftyp":
			if c.size < 8 {
				return io.ErrUnexpectedEOF
			}
			brand, err := readAt(r, c.data, 4)
			if err != nil {
				return err
			}
			switch string(brand) {
			case "qt  ":
				m.fileType = MOV
			case "M4A ":
				m.fileType = M4A
			case "M4B ":
				m.fileType = M4B
			case "M4P ":
				m.fileType = M4P
			}
		case "moov":
			haveMovie = true
			return m.readMP4Metadata(r, c.data, c.end, "moov")
		}
		return nil
	})
	if err == nil && !haveMovie {
		err = errors.New("missing QuickTime movie atom")
	}
	if m.fileType == UnknownFileType && isLegacyQuickTime(r) {
		m.fileType = MOV
	}
	return m, err
}

func (m *metadataMP4) readMP4Metadata(r io.ReadSeeker, start, end int64, parent string) error {
	return walkMP4(r, start, end, func(c fileChunk) error {
		switch {
		case c.id == "udta" && parent == "moov":
			return m.readMP4Metadata(r, c.data, c.end, c.id)
		case c.id == "meta" && (parent == "moov" || parent == "udta"):
			data, err := mp4MetaStart(r, c)
			if err != nil {
				return err
			}
			return m.readMP4Metadata(r, data, c.end, c.id)
		case c.id == "ilst" && parent == "meta":
			return m.readMP4Metadata(r, c.data, c.end, c.id)
		case c.id == "mvhd" && parent == "moov":
			if c.size < 20 {
				return io.ErrUnexpectedEOF
			}
			b, err := readAt(r, c.data, 20)
			if err != nil {
				return err
			}
			if b[0] == 0 {
				m.duration = sampleDuration(uint64(binary.BigEndian.Uint32(b[16:])), int(binary.BigEndian.Uint32(b[12:16])))
			} else if b[0] == 1 && c.size >= 32 {
				b, err = readAt(r, c.data+20, 12)
				if err != nil {
					return err
				}
				m.duration = sampleDuration(binary.BigEndian.Uint64(b[4:]), int(binary.BigEndian.Uint32(b[:4])))
			}
		case parent == "ilst" && c.id == "----":
			b, err := chunkData(r, c)
			if err != nil {
				return err
			}
			name, data, err := readCustomAtom(bytes.NewReader(b), uint32(len(b)+8))
			if err != nil {
				return err
			}
			if name != "----" && len(data) > 0 {
				m.data[name] = strings.Join(data, ";")
			}
		case parent == "ilst" && atoms[c.id] != "":
			b, err := chunkData(r, c)
			if err != nil {
				return err
			}
			return m.readAtomData(bytes.NewReader(b), c.id, uint32(len(b)), nil)
		case parent == "udta" && atoms[c.id] != "" && c.id[0] == 0xa9:
			b, err := chunkData(r, c)
			if err != nil {
				return err
			}
			if len(b) < 4 || int(binary.BigEndian.Uint16(b[:2])) > len(b)-4 {
				return io.ErrUnexpectedEOF
			}
			if _, ok := m.data[c.id]; !ok {
				m.data[c.id] = fixEncoding(b[4 : 4+int(binary.BigEndian.Uint16(b[:2]))])
			}
		}
		return nil
	})
}

func (m *metadataMP4) readAtomData(r io.ReadSeeker, name string, size uint32, processedData []string) error {
	var b []byte
	var err error
	var contentType string
	if len(processedData) > 0 {
		b = []byte(strings.Join(processedData, ";")) // add delimiter if multiple data fields
		contentType = "text"
	} else {
		// read the data
		b, err = readBytes(r, uint(size))
		if err != nil {
			return err
		}
		if len(b) < 8 {
			return fmt.Errorf("invalid encoding: expected at least %d bytes, got %d", 8, len(b))
		}

		// "data" + size (4 bytes each)
		b = b[8:]

		if len(b) < 4 {
			return fmt.Errorf("invalid encoding: expected at least %d bytes, for class, got %d", 4, len(b))
		}
		class := getInt(b[1:4])
		var ok bool
		contentType, ok = atomTypes[class]
		if !ok {
			return fmt.Errorf("invalid content type: %v (%x) (%x)", class, b[1:4], b)
		}

		// 4: atom version (1 byte) + atom flags (3 bytes)
		// 4: NULL (usually locale indicator)
		if len(b) < 8 {
			return fmt.Errorf("invalid encoding: expected at least %d bytes, for atom version and flags, got %d", 8, len(b))
		}
		b = b[8:]
	}

	if name == "trkn" || name == "disk" {
		if len(b) < 6 {
			return fmt.Errorf("invalid encoding: expected at least %d bytes, for track and disk numbers, got %d", 6, len(b))
		}

		m.data[name] = int(binary.BigEndian.Uint16(b[2:4]))
		m.data[name+"_count"] = int(binary.BigEndian.Uint16(b[4:6]))
		return nil
	}

	if contentType == "implicit" {
		if name == "covr" {
			if bytes.HasPrefix(b, pngHeader) {
				contentType = "png"
			}
			// TODO(dhowden): Detect JPEG formats too (harder).
		}
	}

	var data interface{}
	switch contentType {
	case "implicit":
		if _, ok := atoms[name]; ok {
			return fmt.Errorf("unhandled implicit content type for required atom: %q", name)
		}
		return nil

	case "text":
		data = string(b)

	case "uint8":
		if len(b) < 1 {
			return fmt.Errorf("invalid encoding: expected at least %d bytes, for integer tag data, got %d", 1, len(b))
		}
		data = getInt(b[:1])

	case "jpeg", "png":
		data = &Picture{
			Ext:      contentType,
			MIMEType: "image/" + contentType,
			Data:     b,
		}
	}
	m.data[name] = data

	return nil
}

func readAtomHeader(r io.ReadSeeker) (name string, size uint32, err error) {
	err = binary.Read(r, binary.BigEndian, &size)
	if err != nil {
		return
	}
	name, err = readString(r, 4)
	return
}

// Generic atom.
// Should have 3 sub atoms : mean, name and data.
// We check that mean is "com.apple.iTunes" or others and we use the subname as
// the name, and move to the data atom.
// Data atom could have multiple data values, each with a header.
// If anything goes wrong, we jump at the end of the "----" atom.
func readCustomAtom(r io.ReadSeeker, size uint32) (_ string, data []string, _ error) {
	subNames := make(map[string]string)

	for size > 8 {
		subName, subSize, err := readAtomHeader(r)
		if err != nil {
			return "", nil, err
		}

		// Remove the size of the atom from the size counter
		if size >= subSize {
			size -= subSize
		} else {
			return "", nil, errors.New("--- invalid size")
		}

		b, err := readBytes(r, uint(subSize-8))
		if err != nil {
			return "", nil, err
		}

		if len(b) < 4 {
			return "", nil, fmt.Errorf("invalid encoding: expected at least %d bytes, got %d", 4, len(b))
		}
		switch subName {
		case "mean", "name":
			// mean/name atom body: [1 version][3 flags][value]
			subNames[subName] = string(b[4:])
		case "data":
			// data atom body: [1 version][3 flags/class][4 locale][value].
			// Skip version+flags(4) AND the 4-byte locale/reserved field so the
			// raw value is returned without a leading 4-null-byte prefix.
			if len(b) >= 8 {
				data = append(data, string(b[8:]))
			}
		}
	}

	// there should remain only the header size
	if size != 8 {
		err := errors.New("---- atom out of bounds")
		return "", nil, err
	}

	if !means[subNames["mean"]] || subNames["name"] == "" || len(data) == 0 {
		return "----", nil, nil
	}

	return subNames["name"], data, nil
}

func (metadataMP4) Format() Format       { return MP4 }
func (m metadataMP4) FileType() FileType { return m.fileType }

func (m metadataMP4) Raw() map[string]interface{} { return m.data }

func (m metadataMP4) getString(n []string) string {
	for _, k := range n {
		if x, ok := m.data[k]; ok {
			if value, ok := x.(string); ok {
				return value
			}
		}
	}
	return ""
}

func (m metadataMP4) getInt(n []string) int {
	for _, k := range n {
		if x, ok := m.data[k]; ok {
			if value, ok := x.(int); ok {
				return value
			}
		}
	}
	return 0
}

func (m metadataMP4) Title() string {
	return m.getString(atoms.Name("title"))
}

func (m metadataMP4) Artist() string {
	return m.getString(atoms.Name("artist"))
}

func (m metadataMP4) Album() string {
	return m.getString(atoms.Name("album"))
}

func (m metadataMP4) AlbumArtist() string {
	return m.getString(atoms.Name("album_artist"))
}

// MP4 standard text atoms (©ART/aART) do not natively split multiple artists;
// multi-value would arrive as multiple data atoms joined by ';' in
// readAtomData. Expose each value by splitting on ';' as a best-effort until
// the read path preserves the raw slice.
func (m metadataMP4) Artists() []string {
	return splitSemicolonArtists(m.getString(atoms.Name("artist")))
}
func (m metadataMP4) AlbumArtists() []string {
	return splitSemicolonArtists(m.getString(atoms.Name("album_artist")))
}

func (m metadataMP4) Composer() string {
	return m.getString(atoms.Name("composer"))
}

// splitSemicolonArtists best-effort splits an MP4 multi-data atom value (joined
// by ';' in readAtomData) back into individual artists. Returns nil when empty.
func splitSemicolonArtists(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return nonEmptyArtists(strings.Split(s, ";"))
}

func (m metadataMP4) Genre() string {
	return m.getString(atoms.Name("genre"))
}

// Language / Style have no standard iTunes atom, so they are stored as freeform
// ("----") atoms under com.apple.iTunes. readCustomAtom stores the value under
// the freeform "name" sub-atom value (e.g. "LANGUAGE"/"STYLE").
func (m metadataMP4) Language() string {
	return m.getString([]string{"LANGUAGE", "language"})
}

func (m metadataMP4) Style() string {
	return m.getString([]string{"STYLE", "style"})
}

func (m metadataMP4) Year() int {
	date := m.getString(atoms.Name("year"))
	if len(date) >= 4 {
		year, _ := strconv.Atoi(date[:4])
		return year
	}
	return 0
}

func (m metadataMP4) Track() (int, int) {
	x := m.getInt([]string{"trkn"})
	if n, ok := m.data["trkn_count"]; ok {
		return x, n.(int)
	}
	return x, 0
}

func (m metadataMP4) Disc() (int, int) {
	x := m.getInt([]string{"disk"})
	if n, ok := m.data["disk_count"]; ok {
		return x, n.(int)
	}
	return x, 0
}

func (m metadataMP4) Lyrics() string {
	t, ok := m.data["\xa9lyr"]
	if !ok {
		return ""
	}
	return t.(string)
}

func (m metadataMP4) Comment() string {
	t, ok := m.data["\xa9cmt"]
	if !ok {
		return ""
	}
	return t.(string)
}

func (m metadataMP4) Picture() *Picture {
	v, ok := m.data["covr"]
	if !ok {
		return nil
	}
	p, _ := v.(*Picture)
	return p
}

func (m metadataMP4) Duration() time.Duration {
	return m.duration
}

// BitRate / SampleRate 暂未从 stsd/mp4a box 解析,返回 0 由 ffprobe 兜底。
// 注意:mvhd 的 timescale 不是音频采样率,不能直接用作 SampleRate。
func (m metadataMP4) BitRate() int    { return 0 }
func (m metadataMP4) SampleRate() int { return 0 }
