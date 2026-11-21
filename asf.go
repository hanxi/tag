package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	asfHeader             = "\x30\x26\xb2\x75\x8e\x66\xcf\x11\xa6\xd9\x00\xaa\x00\x62\xce\x6c"
	asfData               = "\x36\x26\xb2\x75\x8e\x66\xcf\x11\xa6\xd9\x00\xaa\x00\x62\xce\x6c"
	asfFileProperties     = "\xa1\xdc\xab\x8c\x47\xa9\xcf\x11\x8e\xe4\x00\xc0\x0c\x20\x53\x65"
	asfContentDescription = "\x33\x26\xb2\x75\x8e\x66\xcf\x11\xa6\xd9\x00\xaa\x00\x62\xce\x6c"
	asfExtendedContent    = "\x40\xa4\xd0\xd2\x07\xe3\xd2\x11\x97\xf0\x00\xa0\xc9\x5e\xa8\x50"
)

var asfFields = map[string]string{
	"title": FieldTitle, "author": FieldArtist, "wm/albumtitle": FieldAlbum,
	"wm/albumartist": FieldAlbumArtist, "wm/year": FieldYear, "wm/genre": FieldGenre,
	"wm/language": FieldLanguage, "wm/tracknumber": FieldTrack, "wm/partofset": FieldDisc,
	"wm/lyrics": FieldLyrics, "wm/style": FieldStyle,
}

func encodeUTF16LE(s string) []byte {
	var dst []byte
	for _, u := range utf16.Encode([]rune(s)) {
		dst = binary.LittleEndian.AppendUint16(dst, u)
	}
	return dst
}

func asfString(b []byte) (string, error) {
	if len(b)&1 != 0 {
		return "", errors.New("unaligned ASF string")
	}
	s, err := decodeUTF16(b, binary.LittleEndian)
	before, _, _ := strings.Cut(s, "\x00")
	return before, err
}

func readASF(r io.ReadSeeker) (metadataLegacy, []textSlot, error) {
	m := newLegacyMetadata(ASF, ASFMetadata)
	size, err := getFileSize(r)
	if err != nil {
		return m, nil, err
	}
	h, err := readAt(r, 0, 30)
	if err != nil {
		return m, nil, err
	}
	u64, u32, u16 := binary.LittleEndian.Uint64, binary.LittleEndian.Uint32, binary.LittleEndian.Uint16
	rawEnd := u64(h[16:24])
	if string(h[:16]) != asfHeader || rawEnd < 30 || rawEnd > uint64(size) || h[28] != 1 || h[29] != 2 {
		return m, nil, errors.New("invalid ASF header")
	}
	end := int64(rawEnd)
	pos := int64(30)
	count := u32(h[24:28])
	props := false
	var slots []textSlot
	for range count {
		if end-pos < 24 {
			return m, nil, io.ErrUnexpectedEOF
		}
		obj, err := readAt(r, pos, 24)
		if err != nil {
			return m, nil, err
		}
		n := u64(obj[16:24])
		if n < 24 || n > uint64(end-pos) {
			return m, nil, errors.New("bad ASF object size")
		}
		c := fileChunk{string(obj[:16]), pos, pos + 24, int64(n) - 24, pos + int64(n)}
		switch c.id {
		case asfFileProperties:
			if c.size < 80 {
				return m, nil, io.ErrUnexpectedEOF
			}
			b, err := readAt(r, c.data+40, 40)
			if err != nil {
				return m, nil, err
			}
			play := u64(b[:8])
			preroll := u64(b[16:24])
			if play/10000 > preroll {
				m.duration = sampleDuration(play/10000-preroll, 1000)
			}

			props = true
		case asfContentDescription:
			b, err := chunkData(r, c)
			if err != nil {
				return m, nil, err
			}
			if len(b) < 10 {
				return m, nil, io.ErrUnexpectedEOF
			}
			at := 10
			for i, field := range []string{FieldTitle, FieldArtist, "copyright", "comment", "rating"} {
				length := int(u16(b[i*2 : i*2+2]))
				if length > len(b)-at {
					return m, nil, io.ErrUnexpectedEOF
				}
				s, err := asfString(b[at : at+length])
				if err != nil {
					return m, nil, err
				}
				if s != "" {
					m.setField(field, s)
				}
				slots = append(slots, textSlot{field, c.data + int64(at), length, "utf16"})
				at += length
			}
		case asfExtendedContent:
			b, err := chunkData(r, c)
			if err != nil {
				return m, nil, err
			}
			if len(b) < 2 {
				return m, nil, io.ErrUnexpectedEOF
			}
			at := 2
			for range u16(b[:2]) {
				if len(b)-at < 2 {
					return m, nil, io.ErrUnexpectedEOF
				}
				n := int(u16(b[at : at+2]))
				at += 2
				if len(b)-at < n+4 {
					return m, nil, io.ErrUnexpectedEOF
				}
				key, err := asfString(b[at : at+n])
				if err != nil {
					return m, nil, err
				}
				at += n
				kind, length := u16(b[at:at+2]), int(u16(b[at+2:at+4]))
				at += 4
				if len(b)-at < length {
					return m, nil, io.ErrUnexpectedEOF
				}
				value := b[at : at+length]
				if strings.EqualFold(key, "WM/Picture") && kind == 1 && m.picture == nil {
					m.picture = readASFPicture(value)
				}
				if field := asfFields[strings.ToLower(key)]; field != "" {
					s, encoding, err := asfValue(kind, value)
					if err != nil {
						return m, nil, err
					}
					if s != "" {
						m.setField(field, s)
					}
					slots = append(slots, textSlot{field, c.data + int64(at), length, encoding})
				}

				at += length
			}
		}
		pos = c.end
	}
	data, err := readAt(r, end, 50)
	if err != nil {
		return m, nil, err
	}
	if !props || pos != end || !bytes.Equal(data[:16], []byte(asfData)) || u64(data[16:24]) < 50 || u64(data[16:24]) > uint64(size-end) {
		return m, nil, errors.New("invalid ASF data object")
	}
	return m, slots, nil
}

func asfValue(kind uint16, b []byte) (string, string, error) {
	switch kind {
	case 0:
		s, err := asfString(b)
		return s, "utf16", err
	case 3, 4, 5:
		width := map[uint16]int{3: 4, 4: 8, 5: 2}[kind]
		if len(b) != width {
			return "", "", errors.New("invalid ASF integer size")
		}
		var n uint64
		for i, v := range b {
			n |= uint64(v) << (8 * i)
		}
		return strconv.FormatUint(n, 10), "integer", nil
	default:
		return "", "unsupported", nil
	}
}

func readASFPicture(b []byte) *Picture {
	if len(b) < 9 || (b[0] != 0 && b[0] != 3) {
		return nil
	}
	n := uint64(binary.LittleEndian.Uint32(b[1:5]))
	rest := b[5:]
	var mime string
	for range 2 {
		found := false
		for i := 0; i+1 < len(rest); i += 2 {
			if rest[i] != 0 || rest[i+1] != 0 {
				continue
			}
			if mime == "" {
				mime, _ = asfString(rest[:i])
			}
			rest = rest[i+2:]
			found = true
			break
		}
		if !found {
			return nil
		}
	}
	if n == 0 || n > uint64(len(rest)) || !strings.HasPrefix(mime, "image/") {
		return nil
	}
	return &Picture{MIMEType: mime, Ext: extFromMatroskaCover(mime, ""), Data: rest[:n]}
}
