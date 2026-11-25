package tag

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"slices"
)

func writeOGGIncremental(path string, opts WriteOptions) error {
	return atomicRewrite(path, OGG, func(src, dst *os.File, _ int64) error {
		reader := bufio.NewReader(src)
		packets, last, remaining, err := readOGGHeaders(reader)
		if err != nil {
			return err
		}
		prefix := opusTagsPrefix
		if len(packets) == 3 {
			prefix = vorbisCommentPrefix
		}
		if !bytes.HasPrefix(packets[1], prefix) {
			return unsupportedLayout("missing Ogg comment packet")
		}
		vendor, previous, suffix, err := parseVorbisComments(packets[1][len(prefix):])
		if err != nil {
			return err
		}
		if len(packets) == 3 && len(suffix) == 0 {
			// Older Songloft writers omitted the Vorbis framing bit.
			suffix = []byte{1}
		}
		body := encodeVorbisComments(vendor, mergeVorbisComments(previous, collectOGGComments(opts), opts))
		if len(body)+len(prefix)+len(suffix) > maxMetadataSize {
			return unsupportedLayout("Ogg comment size")
		}
		packets[1] = append(append(bytes.Clone(prefix), body...), suffix...)

		serial := last.header.SerialNumber
		newPages := pageHeaderPackets(packets, serial)
		for _, page := range newPages {
			if _, err := dst.Write(page); err != nil {
				return err
			}
		}
		next := uint32(len(newPages))
		if len(remaining) > 0 {
			n := 0
			for _, segment := range remaining {
				n += int(segment)
			}
			granule := last.header.GranulePosition
			if !slices.ContainsFunc(remaining, func(segment byte) bool { return segment < 255 }) {
				// The completed packet on the original page was a header.
				granule = ^uint64(0)
			}
			page := serializeOGGPage(serial, next, granule, last.header.Flags&4, remaining, last.segmentData[len(last.segmentData)-n:])
			if _, err := dst.Write(page); err != nil {
				return err
			}
			next++
		} else if last.header.Flags&4 != 0 {
			return unsupportedLayout("Ogg stream with headers only")
		}

		eos := last.header.Flags&4 != 0
		for sequence := last.header.SequenceNumber + 1; ; sequence++ {
			page, err := readOneOGGPage(reader)
			if err == io.EOF && eos {
				return nil
			}
			if err != nil {
				return err
			}
			if eos || page.header.SerialNumber != serial || page.header.SequenceNumber != sequence || page.header.Flags&2 != 0 {
				return unsupportedLayout("chained, multiplexed or discontinuous Ogg stream")
			}
			eos = page.header.Flags&4 != 0
			data := page.raw
			if next != sequence {
				data = adjustOGGPageSeq(data, next)
			}
			if _, err := dst.Write(data); err != nil {
				return err
			}
			next++
		}
	})
}

func readOGGHeaders(r io.Reader) ([][]byte, oggPage, []byte, error) {
	first, err := readOneOGGPage(r)
	if err != nil {
		return nil, oggPage{}, nil, err
	}
	lacing := first.segmentTable
	if first.header.Flags != 2 || first.header.SequenceNumber != 0 || len(lacing) == 0 || lacing[len(lacing)-1] == 255 {
		return nil, oggPage{}, nil, unsupportedLayout("invalid Ogg identification page")
	}
	for _, segment := range lacing[:len(lacing)-1] {
		if segment != 255 {
			return nil, oggPage{}, nil, unsupportedLayout("additional packets on Ogg identification page")
		}
	}
	identification := first.segmentData
	count := 2
	switch {
	case bytes.HasPrefix(identification, vorbisIdentificationPrefix):
		count = 3
	case !bytes.HasPrefix(identification, []byte("OpusHead")):
		return nil, oggPage{}, nil, unsupportedLayout("Ogg codec")
	}
	packets := [][]byte{identification}
	var pending []byte
	var total int
	for sequence := uint32(1); ; sequence++ {
		page, err := readOneOGGPage(r)
		if err != nil {
			return nil, oggPage{}, nil, err
		}
		if page.header.SerialNumber != first.header.SerialNumber || page.header.SequenceNumber != sequence ||
			page.header.Flags&2 != 0 || (page.header.Flags&1 != 0) != (len(pending) > 0) || len(page.segmentTable) == 0 {
			return nil, oggPage{}, nil, unsupportedLayout("unexpected Ogg header page")
		}
		offset := 0
		for i, segment := range page.segmentTable {
			n := int(segment)
			total += n
			if total > maxMetadataSize {
				return nil, oggPage{}, nil, unsupportedLayout("Ogg header size")
			}
			pending = append(pending, page.segmentData[offset:offset+n]...)
			offset += n
			if segment == 255 {
				continue
			}
			packets = append(packets, pending)
			pending = nil
			if len(packets) == count {
				if count == 3 && !bytes.HasPrefix(packets[2], []byte("\x05vorbis")) {
					return nil, oggPage{}, nil, unsupportedLayout("missing Vorbis setup")
				}
				return packets, page, page.segmentTable[i+1:], nil
			}
		}
		if page.header.Flags&4 != 0 {
			return nil, oggPage{}, nil, unsupportedLayout("Ogg stream ended inside headers")
		}
	}
}
