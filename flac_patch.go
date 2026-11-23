package tag

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
)

type flacMetadataBlock struct {
	kind byte
	data []byte
}

func writeFLACIncremental(path string, opts WriteOptions) error {
	return atomicRewrite(path, FLAC, func(src, dst *os.File, size int64) error {
		magic, err := readString(src, 4)
		if err != nil || magic != "fLaC" {
			return unsupportedLayout("FLAC with a foreign prefix")
		}
		var blocks []flacMetadataBlock
		var editedComments bool
		offset := int64(4)
		for {
			var head [4]byte
			if _, err := io.ReadFull(src, head[:]); err != nil {
				return err
			}
			n := int(head[1])<<16 | int(head[2])<<8 | int(head[3])
			offset += int64(4 + n)
			if offset > size || offset > maxMetadataSize {
				return unsupportedLayout("FLAC metadata size")
			}
			data, err := readBytes(src, uint(n))
			if err != nil {
				return err
			}
			kind := head[0] & 0x7f
			switch kind {
			case flacBlockVorbisComment:
				if editedComments {
					return unsupportedLayout("multiple FLAC comment blocks")
				}
				data, err = mergeFLACComments(data, opts)
				if err != nil {
					return err
				}
				editedComments = true
			case flacBlockPicture:
				if len(data) < 4 {
					return unsupportedLayout("truncated FLAC picture")
				}
				pictureType := binary.BigEndian.Uint32(data)
				if _, clearing := textEdits(opts)[FieldPicture]; (clearing || opts.Picture != nil) && (pictureType == 0 || pictureType == 3) {
					kind = flacBlockPadding
					data = nil
				}
			}
			if kind != flacBlockPadding || len(data) > 0 {
				blocks = append(blocks, flacMetadataBlock{kind, data})
			}
			if head[0]&0x80 != 0 {
				break
			}
		}
		if len(blocks) == 0 || blocks[0].kind != flacBlockStreamInfo {
			return unsupportedLayout("missing FLAC stream info")
		}
		if !editedComments {
			blocks = append(blocks, flacMetadataBlock{flacBlockVorbisComment, buildFLACVorbisComment(opts)})
		}
		if opts.Picture != nil {
			blocks = append(blocks, flacMetadataBlock{flacBlockPicture, buildFLACPicture(opts.Picture)})
		}
		if _, err := dst.WriteString(magic); err != nil {
			return err
		}
		for i, block := range blocks {
			n := len(block.data)
			if n > 0xffffff {
				return unsupportedLayout("FLAC block too large")
			}
			kind := block.kind
			if i == len(blocks)-1 {
				kind |= 0x80
			}
			if _, err := dst.Write([]byte{kind, byte(n >> 16), byte(n >> 8), byte(n)}); err != nil {
				return err
			}
			if _, err := dst.Write(block.data); err != nil {
				return err
			}
		}
		return copySection(dst, src, offset, size-offset)
	})
}

func readFLACComments(data []byte) (string, []string, error) {
	r := bytes.NewReader(data)
	n, err := readUint32LittleEndian(r)
	if err != nil || uint64(n) > uint64(r.Len()) {
		return "", nil, unsupportedLayout("FLAC comment vendor")
	}
	vendor, err := readString(r, uint(n))
	if err != nil {
		return "", nil, err
	}
	count, err := readUint32LittleEndian(r)
	if err != nil || uint64(count) > uint64(r.Len()/4) {
		return "", nil, unsupportedLayout("FLAC comment count")
	}
	var comments []string
	for range count {
		n, err := readUint32LittleEndian(r)
		if err != nil || uint64(n) > uint64(r.Len()) {
			return "", nil, unsupportedLayout("FLAC comment length")
		}
		value, err := readString(r, uint(n))
		if err != nil {
			return "", nil, err
		}
		comments = append(comments, value)
	}
	if r.Len() != 0 {
		return "", nil, unsupportedLayout("trailing FLAC comment bytes")
	}
	return vendor, comments, nil
}

func mergeFLACComments(data []byte, opts WriteOptions) ([]byte, error) {
	vendor, previous, err := readFLACComments(data)
	if err != nil {
		return nil, err
	}
	_, edits, err := readFLACComments(buildFLACVorbisComment(opts))
	if err != nil {
		return nil, err
	}
	changed := make(map[string]bool)
	for _, comment := range edits {
		key, _, _ := strings.Cut(comment, "=")
		changed[strings.ToUpper(key)] = true
	}
	aliases := map[string][]string{
		FieldYear: {"DATE", "YEAR"}, FieldTrack: {"TRACKNUMBER", "TRACKTOTAL", "TOTALTRACKS"},
		FieldDisc: {"DISCNUMBER", "DISCTOTAL", "TOTALDISCS"}, FieldAlbumArtist: {"ALBUMARTIST"},
		FieldPicture: {"METADATA_BLOCK_PICTURE", "COVERART", "COVERARTMIME"},
	}
	for field := range textEdits(opts) {
		changed[strings.ToUpper(field)] = true
		for _, key := range aliases[field] {
			changed[key] = true
		}
	}
	if opts.Picture != nil {
		for _, key := range aliases[FieldPicture] {
			changed[key] = true
		}
	}
	comments := make([]string, 0, len(previous)+len(edits))
	for _, comment := range previous {
		key, _, _ := strings.Cut(comment, "=")
		if !changed[strings.ToUpper(key)] {
			comments = append(comments, comment)
		}
	}
	comments = append(comments, edits...)
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(vendor)))
	out = append(out, vendor...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(comments)))
	for _, comment := range comments {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(comment)))
		out = append(out, comment...)
	}
	return out, nil
}
