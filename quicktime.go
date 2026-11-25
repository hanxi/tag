package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
)

func walkMP4(r io.ReadSeeker, start, end int64, visit func(fileChunk) error) error {
	for start < end {
		if end-start < 8 {
			return io.ErrUnexpectedEOF
		}
		h, err := readAt(r, start, 8)
		if err != nil {
			return err
		}
		width := int64(8)
		n := uint64(binary.BigEndian.Uint32(h[:4]))
		switch n {
		case 0:
			n = uint64(end - start)
		case 1:
			width = 16
			if end-start < width {
				return io.ErrUnexpectedEOF
			}
			large, err := readAt(r, start+8, 8)
			if err != nil {
				return err
			}
			n = binary.BigEndian.Uint64(large)
		}
		if n < uint64(width) || n > uint64(end-start) {
			return errors.New("invalid MP4 atom length")
		}
		c := fileChunk{string(h[4:8]), start, start + width, int64(n) - width, start + int64(n)}
		if err := visit(c); err != nil {
			return err
		}
		start = c.end
	}
	return nil
}

func looksLikeMP4(h []byte) bool {
	switch string(h[4:8]) {
	case "ftyp", "moov", "mdat", "free", "skip", "wide", "pnot", "junk":
		return true
	}
	return false
}

func isLegacyQuickTime(r io.ReadSeeker) bool {
	saved, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return false
	}
	defer r.Seek(saved, io.SeekStart)
	h, err := readAt(r, 0, 8)
	if err != nil || !looksLikeMP4(h) || string(h[4:8]) == "ftyp" {
		return false
	}
	size, err := getFileSize(r)
	if err != nil {
		return false
	}
	var moov, ftyp bool
	err = walkMP4(r, 0, size, func(c fileChunk) error {
		moov = moov || c.id == "moov"
		ftyp = ftyp || c.id == "ftyp"
		return nil
	})
	return err == nil && moov && !ftyp
}

func isQuickTimeFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h, err := readAt(f, 0, 12)
	return err == nil && (string(h[4:12]) == "ftypqt  " || isLegacyQuickTime(f))
}

func mp4MetaStart(r io.ReadSeeker, c fileChunk) (int64, error) {
	if c.size == 0 {
		return c.data, nil
	}
	h, err := readAt(r, c.data, 4)
	if err != nil || c.size < 4 {
		return 0, io.ErrUnexpectedEOF
	}
	if bytes.Equal(h, []byte{0, 0, 0, 0}) {
		return c.data + 4, nil
	}
	return c.data, nil
}

func mp4Field(r io.ReadSeeker, c fileChunk) (string, error) {
	if c.id == "----" {
		var name, domain string
		err := walkMP4(r, c.data, c.end, func(child fileChunk) error {
			if child.id != "mean" && child.id != "name" {
				return nil
			}
			data, err := chunkData(r, child)
			if err != nil {
				return err
			}
			if len(data) < 4 {
				return io.ErrUnexpectedEOF
			}
			if child.id == "mean" {
				domain = string(data[4:])
			} else {
				name = string(data[4:])
			}
			return nil
		})
		if domain == "com.apple.iTunes" {
			return mp4FreeformField(name), err
		}
		return "", err
	}
	return atoms[c.id], nil
}

func mp4FreeformField(name string) string {
	name = strings.ToLower(name)
	switch name {
	case "", FieldLanguage, FieldStyle:
		return name
	default:
		return "----:" + name
	}
}

func writeQuickTime(path string, opts WriteOptions) error {
	return atomicRewrite(path, UnknownFileType, func(src, dst *os.File, size int64) error {
		var moov fileChunk
		var media bool
		err := walkMP4(src, 0, size, func(c fileChunk) error {
			switch c.id {
			case "moov":
				if moov.id != "" {
					return unsupportedLayout("duplicate movie atoms")
				}
				moov = c
			case "mdat":
				media = true
			case "ftyp", "free", "skip", "wide", "junk":
			default:
				return unsupportedLayout("QuickTime top-level " + c.id)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if moov.id == "" || !media {
			return unsupportedLayout("missing QuickTime movie or media")
		}
		additions := buildMP4Ilst(opts)[8:]
		changed := make(map[string]bool)
		for _, field := range opts.ClearFields {
			changed[field] = true
		}
		if opts.Incremental {
			for name := range opts.CustomTags {
				changed[mp4FreeformField(name)] = true
			}
		}
		err = walkMP4(bytes.NewReader(additions), 0, int64(len(additions)), func(c fileChunk) error {
			field, err := mp4Field(bytes.NewReader(additions), c)
			changed[field] = true
			return err
		})
		delete(changed, "")
		if err != nil {
			return err
		}
		if len(changed) == 0 {
			return ErrUnsupportedWrite
		}
		body, err := editQuickTimeAtoms(src, moov.data, moov.end, "moov", changed, additions)
		if err != nil {
			return err
		}
		if moov.end == size {
			if err := copySection(dst, src, 0, moov.start); err != nil {
				return err
			}
		} else {
			// Relocate only the movie metadata. Every sample and chunk offset stays fixed.
			if err := copySection(dst, src, 0, moov.start+4); err != nil {
				return err
			}
			if _, err := dst.WriteString("free"); err != nil {
				return err
			}
			// A zero-size atom consumes the rest of the file, including an appended moov.
			if err := walkMP4(src, moov.end, size, func(c fileChunk) error {
				b, err := readAt(src, c.start, 4)
				if err != nil {
					return err
				}
				if binary.BigEndian.Uint32(b) == 0 {
					return unsupportedLayout("open-ended QuickTime atom")
				}
				return nil
			}); err != nil {
				return err
			}
			if err := copySection(dst, src, moov.start+8, size-moov.start-8); err != nil {
				return err
			}
		}
		_, err = dst.Write(buildAtom("moov", body))
		return err
	})
}

func editQuickTimeAtoms(src io.ReadSeeker, start, end int64, parent string, changed map[string]bool, additions []byte) ([]byte, error) {
	var body []byte
	var wrote bool
	err := walkMP4(src, start, end, func(c fileChunk) error {
		if c.end-c.start > int64(maxMetadataSize-len(body)) {
			return unsupportedLayout("movie metadata size")
		}
		if (parent == "moov" && c.id == "udta") || (parent == "udta" && c.id == "meta") {
			if wrote {
				return unsupportedLayout("multiple QuickTime metadata containers")
			}
			wrote = true
			pos := c.data
			if c.id == "meta" {
				var err error
				pos, err = mp4MetaStart(src, c)
				if err != nil {
					return err
				}
			}
			child, err := editQuickTimeAtoms(src, pos, c.end, c.id, changed, additions)
			if err != nil {
				return err
			}
			prefix, err := readAt(src, c.data, int(pos-c.data))
			if err != nil {
				return err
			}
			body = append(body, buildAtom(c.id, append(prefix, child...))...)
			return nil
		}
		if parent == "moov" && (c.id == "meta" || c.id == "cmov" || c.id == "mvex" || c.id == "rmra") {
			return unsupportedLayout("alternate QuickTime movie layout")
		}
		if parent == "meta" {
			if c.id == "keys" || c.id == "iloc" {
				return unsupportedLayout("keyed or indexed QuickTime metadata")
			}
			if c.id == "ilst" {
				if wrote {
					return unsupportedLayout("multiple QuickTime tag lists")
				}
				wrote = true
				child, err := editQuickTimeAtoms(src, c.data, c.end, c.id, changed, additions)
				if err != nil {
					return err
				}
				body = append(body, buildAtom("ilst", child)...)
				return nil
			}
		}
		if parent == "ilst" || (parent == "udta" && c.id[0] == 0xa9) {
			field, err := mp4Field(src, c)
			if err != nil {
				return err
			}
			if changed[field] {
				return nil
			}
		}
		raw, err := readAt(src, c.start, int(c.end-c.start))
		body = append(body, raw...)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !wrote {
		switch parent {
		case "moov":
			body = append(body, buildAtom("udta", buildAtomWithMeta("meta", append(buildMP4MetaHdlr(), buildAtom("ilst", additions)...)))...)
		case "udta":
			body = append(body, buildAtomWithMeta("meta", append(buildMP4MetaHdlr(), buildAtom("ilst", additions)...))...)
		case "meta":
			body = append(body, buildAtom("ilst", additions)...)
		case "ilst":
			body = append(body, additions...)
		}
	}
	return body, nil
}
