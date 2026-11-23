package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf16"
)

func readID3Block(r io.ReadSeeker, offset, size int64) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	if size < 10 || size > maxMetadataSize {
		return nil, errors.New("invalid ID3 block size")
	}
	b, err := readAt(r, offset, int(size))
	if err != nil {
		return nil, err
	}
	n, err := id3BlockSize(b)
	if err != nil || int64(n) != size {
		return nil, errors.New("invalid ID3 block length")
	}
	return b, nil
}

func id3BlockSize(header []byte) (int, error) {
	if len(header) < 10 || string(header[:3]) != "ID3" || header[3] < 2 || header[3] > 4 ||
		(header[6]|header[7]|header[8]|header[9])&0x80 != 0 {
		return 0, errors.New("invalid ID3 header")
	}
	size := 10 + int(decodeSyncSafe(header[6:10]))
	if header[3] == 4 && header[5]&0x10 != 0 {
		size += 10
	}
	return size, nil
}

func readOptionalID3(r io.ReadSeeker, offset, size int64) (*metadataID3v2, error) {
	b, err := readID3Block(r, offset, size)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return &metadataID3v2{header: &id3v2Header{}}, nil
	}
	return ReadID3v2Tags(bytes.NewReader(b))
}

type rawID3Frame struct {
	id  string
	raw []byte
}

func writeMP3Incremental(path string, opts WriteOptions) error {
	return atomicRewrite(path, MP3, func(src, dst *os.File, size int64) error {
		offset, err := id3v2AudioOffset(src)
		if err != nil {
			return err
		}
		if offset > size-4 {
			return unsupportedLayout("missing MPEG audio payload")
		}
		head, err := readAt(src, offset, 4)
		if err != nil {
			return err
		}
		// An ID3 prefix can also appear on non-MP3 containers.
		if head[0] != 0xff || head[1]&0xe0 != 0xe0 || head[1]&0x18 == 0x08 ||
			head[1]&0x06 == 0 || head[2]&0xf0 == 0xf0 || head[2]&0x0c == 0x0c {
			return unsupportedLayout("unknown ID3 payload")
		}
		old, err := readID3Block(src, 0, offset)
		if err != nil {
			return err
		}
		edited, err := updateID3Block(old, opts)
		if err != nil {
			return err
		}
		if _, err := dst.Write(edited); err != nil {
			return err
		}
		return copySection(dst, src, offset, size-offset)
	})
}

func splitID3Frames(data []byte, version byte) ([]rawID3Frame, error) {
	var result []rawID3Frame
	for len(data) != 0 && data[0] != 0 {
		if len(data) < 10 {
			return nil, unsupportedLayout("truncated ID3 frame")
		}
		n := binary.BigEndian.Uint32(data[4:8])
		if version == 4 {
			if (data[4]|data[5]|data[6]|data[7])&0x80 != 0 {
				return nil, unsupportedLayout("invalid ID3v2.4 frame length")
			}
			n = decodeSyncSafe(data[4:8])
		}
		if n == 0 || n > uint32(len(data)-10) {
			return nil, unsupportedLayout("invalid ID3 frame length")
		}
		for _, c := range data[:4] {
			if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				return nil, unsupportedLayout("invalid ID3 frame ID")
			}
		}
		result = append(result, rawID3Frame{string(data[:4]), data[:10+int(n)]})
		data = data[10+int(n):]
	}
	for _, b := range data {
		if b != 0 {
			return nil, unsupportedLayout("invalid ID3 padding")
		}
	}
	return result, nil
}

func id3FrameKey(f rawID3Frame) string {
	switch f.id {
	case "TXXX":
		c, err := readTextWithDescrFrame(f.raw[10:], false, true)
		if err == nil {
			return "TXXX:" + strings.ToUpper(c.Description)
		}
	case "APIC":
		payload := f.raw[10:]
		if len(payload) > 1 {
			if n := bytes.IndexByte(payload[1:], 0); n >= 0 && n+2 < len(payload) &&
				(payload[n+2] == 3 || payload[n+2] == 0) {
				return f.id
			}
		}
	case "TYER", "TDRC":
		return "year"
	default:
		return f.id
	}
	return ""
}

func updateID3Block(old []byte, opts WriteOptions) ([]byte, error) {
	version := byte(4)
	var previous []rawID3Frame
	if len(old) != 0 {
		version = old[3]
		if (version != 3 && version != 4) || old[5] != 0 {
			return nil, unsupportedLayout("ID3 version or extended header")
		}
		var err error
		previous, err = splitID3Frames(old[10:], version)
		if err != nil {
			return nil, err
		}
	}
	built, err := buildID3v2Frames(opts)
	if err != nil {
		return nil, err
	}
	replacements, err := splitID3Frames(built, 3)
	if err != nil {
		return nil, err
	}
	changed := make(map[string]bool)
	for _, f := range replacements {
		changed[id3FrameKey(f)] = true
	}
	for _, field := range opts.ClearFields {
		switch field {
		case FieldYear:
			changed["year"] = true
		case FieldStyle:
			changed["TXXX:STYLE"] = true
		default:
			changed[frames.Name(field, ID3v2_3)] = true
		}
	}
	delete(changed, "")
	var body bytes.Buffer
	for _, f := range previous {
		if f.raw[9] != 0 && (f.id == "TXXX" || f.id == "APIC" || changed[id3FrameKey(f)]) {
			return nil, unsupportedLayout("encoded ID3 frame")
		}
		if !changed[id3FrameKey(f)] {
			body.Write(f.raw)
		}
	}
	for _, f := range replacements {
		payload := f.raw[10:]
		if version == 3 {
			payload = id3v23Payload(f.id, payload)
		}
		if version == 4 && f.id == "TYER" {
			f.id = "TDRC"
		}
		at := body.Len()
		writeID3v2Frame(&body, f.id, payload)
		if version == 4 {
			n := encodeSyncSafe(uint32(len(payload)))
			copy(body.Bytes()[at+4:at+8], n[:])
		}
	}
	if body.Len()+10 > maxMetadataSize {
		return nil, unsupportedLayout("ID3 metadata size")
	}
	size := encodeSyncSafe(uint32(body.Len()))
	return append([]byte{'I', 'D', '3', version, 0, 0, size[0], size[1], size[2], size[3]}, body.Bytes()...), nil
}

func id3v23Payload(id string, payload []byte) []byte {
	wide := func(b []byte) []byte {
		dst := []byte{0xff, 0xfe}
		for _, u := range utf16.Encode([]rune(string(b))) {
			dst = binary.LittleEndian.AppendUint16(dst, u)
		}
		return dst
	}
	switch id {
	case "APIC":
		return append([]byte{0}, payload[1:]...)
	case "USLT":
		return append([]byte{1, 'x', 'x', 'x', 0xff, 0xfe, 0, 0}, wide(payload[5:])...)
	case "TXXX":
		i := bytes.IndexByte(payload[1:], 0) + 1
		desc := append(wide(payload[1:i]), 0, 0)
		return append(append([]byte{1}, desc...), wide(payload[i+1:])...)
	default:
		return append([]byte{1}, wide(payload[1:])...)
	}
}

func id3Options(opts WriteOptions, exclude ...string) WriteOptions {
	for _, field := range exclude {
		opts.ClearFields = slices.DeleteFunc(slices.Clone(opts.ClearFields), func(v string) bool { return field == v })
		switch field {
		case FieldTitle:
			opts.Title = ""
		case FieldArtist:
			opts.Artist = ""
		case FieldAlbum:
			opts.Album = ""
		case FieldYear:
			opts.Year = 0
		case FieldGenre:
			opts.Genre = ""
		case FieldTrack:
			opts.Track = ""
		case FieldLyrics:
			opts.Lyrics = ""
		}
	}
	return opts
}
