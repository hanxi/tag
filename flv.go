package tag

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"time"
)

var flvFields = map[string]string{
	"title": FieldTitle, "artist": FieldArtist, "album": FieldAlbum, "albumartist": FieldAlbumArtist,
	"date": FieldYear, "year": FieldYear, "genre": FieldGenre, "language": FieldLanguage, "style": FieldStyle,
	"track": FieldTrack, "disc": FieldDisc, "lyrics": FieldLyrics, "comment": "comment",
}

func readFLV(r io.ReadSeeker) (metadataLegacy, []textSlot, error) {
	m := newLegacyMetadata(FLV, FLVMetadata)
	size, err := getFileSize(r)
	if err != nil {
		return m, nil, err
	}
	h, err := readAt(r, 0, 9)
	if err != nil {
		return m, nil, err
	}
	start := int64(binary.BigEndian.Uint32(h[5:]))
	if string(h[:3]) != "FLV" || h[3] != 1 || h[4]&0xfa != 0 || start < 9 || start > size-4 {
		return m, nil, errors.New("invalid FLV header")
	}
	prev, err := readAt(r, start, 4)
	if err != nil || binary.BigEndian.Uint32(prev) != 0 {
		return m, nil, errors.New("invalid FLV initial tag size")
	}
	var slots []textSlot
	var media bool
	for pos := start + 4; pos < size; {
		h, err := readAt(r, pos, 11)
		if err != nil {
			return m, nil, err
		}
		n := int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
		if h[0]&0xc0 != 0 || h[8]|h[9]|h[10] != 0 || n > size-pos-15 {
			return m, nil, errors.New("invalid FLV tag size or flags")
		}
		switch h[0] {
		case 8, 9:
			media = media || n > 0
		case 18:
			b, err := chunkData(r, fileChunk{data: pos + 11, size: n})
			if err != nil {
				return m, nil, err
			}
			a := amfReader{data: b}
			event, _, _, err := a.value(0, false, nil)
			if err != nil {
				return m, nil, err
			}
			if event == "onMetaData" {
				_, _, _, err = a.value(0, true, func(key, value string, off, length int) {
					field := flvFields[strings.ToLower(key)]
					if field != "" {
						m.setField(field, value)
						slots = append(slots, textSlot{field, pos + 11 + int64(off), length, "exact"})
					}
				})
				if err != nil {
					return m, nil, err
				}
				if a.duration > 0 && a.duration < float64(1<<63-1)/float64(time.Second) {
					m.duration = time.Duration(a.duration * float64(time.Second))
				}
			}
		}
		prev, err := readAt(r, pos+11+n, 4)
		if err != nil || int64(binary.BigEndian.Uint32(prev)) != n+11 {
			return m, nil, errors.New("invalid FLV previous tag size")
		}
		pos += 15 + n
	}
	if !media {
		return m, nil, errors.New("missing FLV media tags")
	}
	return m, slots, nil
}

type amfReader struct {
	data     []byte
	pos      int
	duration float64
}

func (a *amfReader) consume(n int) ([]byte, error) {
	if n < 0 || n > len(a.data)-a.pos {
		return nil, io.ErrUnexpectedEOF
	}
	b := a.data[a.pos : a.pos+n]
	a.pos += n
	return b, nil
}

func (a *amfReader) str(wide bool) (string, int, int, error) {
	width := 2
	if wide {
		width = 4
	}
	b, err := a.consume(width)
	if err != nil {
		return "", 0, 0, err
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	if wide {
		n = int(binary.BigEndian.Uint32(b))
	}
	offset := a.pos
	b, err = a.consume(n)
	return string(b), offset, n, err
}

func (a *amfReader) value(depth int, top bool, visit func(string, string, int, int)) (string, int, int, error) {
	if depth > 20 {
		return "", 0, 0, errors.New("AMF nesting limit")
	}
	b, err := a.consume(1)
	if err != nil {
		return "", 0, 0, err
	}
	if top && b[0] != 3 && b[0] != 8 {
		return "", 0, 0, errors.New("invalid FLV metadata object")
	}
	n := 0
	switch b[0] {
	case 0:
		n = 8
	case 1:
		n = 1
	case 2, 12:
		return a.str(b[0] == 12)
	case 3, 8, 16:
		if b[0] == 8 {
			// The ECMA array count is advisory. The object-end marker is authoritative.
			if _, err := a.consume(4); err != nil {
				return "", 0, 0, err
			}
		}
		if b[0] == 16 {
			if _, _, _, err := a.str(false); err != nil {
				return "", 0, 0, err
			}
		}
		for {
			key, _, _, err := a.str(false)
			if err != nil {
				return "", 0, 0, err
			}
			if key == "" && a.pos < len(a.data) && a.data[a.pos] == 9 {
				a.pos++
				return "", 0, 0, nil
			}
			offset := a.pos
			value, at, length, err := a.value(depth+1, false, nil)
			if err != nil {
				return "", 0, 0, err
			}
			if visit != nil {
				if marker := a.data[offset]; marker == 2 || marker == 12 {
					visit(key, value, at, length)
				}
				if key == "duration" && a.data[offset] == 0 {
					a.duration = math.Float64frombits(binary.BigEndian.Uint64(a.data[offset+1:]))
				}
			}
		}
	case 5, 6, 13:
		return "", 0, 0, nil
	case 7:
		n = 2
	case 10:
		count, err := a.consume(4)
		if err != nil {
			return "", 0, 0, err
		}
		k := binary.BigEndian.Uint32(count)
		if uint64(k) > uint64(len(a.data)-a.pos) {
			return "", 0, 0, io.ErrUnexpectedEOF
		}
		for range k {
			if _, _, _, err := a.value(depth+1, false, nil); err != nil {
				return "", 0, 0, err
			}
		}
		return "", 0, 0, nil
	case 11:
		n = 10
	case 15:
		_, _, _, err := a.str(true)
		return "", 0, 0, err
	default:
		return "", 0, 0, errors.New("unsupported AMF0 value type")
	}
	_, err = a.consume(n)
	return "", 0, 0, err
}
