package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"time"
)

func readRealMedia(r io.ReadSeeker) (metadataLegacy, []textSlot, error) {
	m := newLegacyMetadata(RM, RealMedia)
	end, err := getFileSize(r)
	if err != nil {
		return m, nil, err
	}
	var slots []textSlot
	var stream bool
	u32, u16 := binary.BigEndian.Uint32, binary.BigEndian.Uint16
	for pos := int64(0); pos < end; {
		h, err := readAt(r, pos, 10)
		if err != nil {
			return m, nil, err
		}
		id, length, version := string(h[:4]), int64(u32(h[4:8])), u16(h[8:10])
		if pos == 0 && (id != ".RMF" || length < 18 || version > 1) {
			return m, nil, errors.New("invalid RealMedia header")
		}
		if length < 10 || (id != "DATA" && length > end-pos) {
			return m, nil, errors.New("invalid RealMedia object size")
		}
		switch id {
		case "PROP":
			if length >= 50 {
				b, err := readAt(r, pos+10, 24)
				if err != nil {
					return m, nil, err
				}
				m.duration = time.Duration(u32(b[20:24])) * time.Millisecond
			}
		case "MDPR":
			stream = true
		case "CONT":
			if version != 0 {
				return m, nil, errors.New("unknown RealMedia content version")
			}
			b, err := chunkData(r, fileChunk{data: pos + 10, size: length - 10})
			if err != nil {
				return m, nil, err
			}
			offset := 0
			for _, field := range []string{FieldTitle, FieldArtist, "copyright", "comment"} {
				if len(b)-offset < 2 {
					return m, nil, io.ErrUnexpectedEOF
				}
				n := int(u16(b[offset:]))
				offset += 2
				if n > len(b)-offset {
					return m, nil, io.ErrUnexpectedEOF
				}
				m.setField(field, fixEncoding(bytes.TrimRight(b[offset:offset+n], "\x00")))
				slots = append(slots, textSlot{field, pos + 10 + int64(offset), n, "padded"})
				offset += n
			}
		case "DATA":
			if !stream || length < 18 || version != 0 {
				return m, nil, errors.New("invalid RealMedia data object")
			}
			next, err := validateRealPackets(r, pos, length, end)
			if err != nil {
				return m, nil, err
			}
			if next == 0 {
				return m, slots, nil
			}
			pos = next
			continue
		}
		pos += length
	}
	return m, nil, errors.New("missing RealMedia data")
}

// Some RealMedia encoders include the final eight zero bytes and an extra
// object header in DATA's declared size. Validate packet boundaries before
// allowing that legacy layout.
func validateRealPackets(r io.ReadSeeker, start, length, end int64) (int64, error) {
	header, err := readAt(r, start+10, 8)
	if err != nil {
		return 0, err
	}
	count := binary.BigEndian.Uint32(header[:4])
	next := int64(binary.BigEndian.Uint32(header[4:]))
	dataEnd := start + length
	legacy := dataEnd-end == 10
	if legacy {
		dataEnd = end
	}
	if dataEnd > end || next != 0 && (next < start+18 || next >= end || next < dataEnd) {
		return 0, errors.New("invalid RealMedia DATA extent")
	}
	at := start + 18
	for range count {
		if dataEnd-at < 12 {
			return 0, io.ErrUnexpectedEOF
		}
		pkt, err := readAt(r, at, 4)
		if err != nil {
			return 0, err
		}
		version, size := binary.BigEndian.Uint16(pkt[:2]), int64(binary.BigEndian.Uint16(pkt[2:4]))
		if version > 1 || size < 12 || size > dataEnd-at {
			return 0, errors.New("invalid RealMedia packet")
		}
		at += size
	}
	if legacy {
		if dataEnd-at != 8 {
			return 0, errors.New("invalid RealMedia DATA trailer")
		}
		b, err := readAt(r, at, 8)
		if err != nil || !bytes.Equal(b, make([]byte, 8)) {
			return 0, errors.New("invalid RealMedia DATA trailer")
		}
	}
	if count == 0 || !legacy && at != dataEnd {
		return 0, errors.New("RealMedia DATA packet count mismatch")
	}
	if next != 0 {
		return next, nil
	}
	if !legacy && dataEnd < end {
		err := validateRealIndexes(r, dataEnd, end)
		if err != nil {
			return 0, err
		}
	}
	return 0, nil
}

func validateRealIndexes(r io.ReadSeeker, start, end int64) error {
	for start < end {
		if end-start == 8 {
			b, err := readAt(r, start, 8)
			if err == nil && bytes.Equal(b, make([]byte, 8)) {
				return nil
			}
		}
		if end-start < 10 {
			return io.ErrUnexpectedEOF
		}
		h, err := readAt(r, start, 10)
		if err != nil {
			return err
		}
		n := int64(binary.BigEndian.Uint32(h[4:8]))
		if n < 10 || n > end-start {
			return errors.New("invalid RealMedia trailing object")
		}
		start += n
	}
	return nil
}
