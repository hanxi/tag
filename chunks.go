package tag

import (
	"encoding/binary"
	"errors"
	"io"
)

type fileChunk struct {
	id                     string
	start, data, size, end int64
}

// walkChunks visits bounded RIFF or DSDIFF chunks, including their alignment.
func walkChunks(r io.ReadSeeker, start, end int64, wide bool, visit func(fileChunk) error) error {
	width := 8
	if wide {
		width = 12
	}
	for start < end {
		if end-start < int64(width) {
			return io.ErrUnexpectedEOF
		}
		h, err := readAt(r, start, width)
		if err != nil {
			return err
		}
		size := uint64(binary.LittleEndian.Uint32(h[4:8]))
		if wide {
			size = binary.BigEndian.Uint64(h[4:12])
		}
		next := start + int64(width)
		available := uint64(end - next)
		if size > available || size+(size&1) > available {
			return io.ErrUnexpectedEOF
		}
		c := fileChunk{string(h[:4]), start, next, int64(size), next + int64(size+(size&1))}
		if err := visit(c); err != nil {
			return err
		}
		start = c.end
	}
	return nil
}

func chunkData(r io.ReadSeeker, c fileChunk) ([]byte, error) {
	if c.size > maxMetadataSize {
		return nil, errors.New("oversized metadata chunk")
	}
	return readAt(r, c.data, int(c.size))
}

func appendChunk(dst []byte, id string, data []byte, wide bool) []byte {
	dst = append(dst, id...)
	if wide {
		dst = binary.BigEndian.AppendUint64(dst, uint64(len(data)))
	} else {
		dst = binary.LittleEndian.AppendUint32(dst, uint32(len(data)))
	}
	dst = append(dst, data...)
	if len(data)&1 != 0 {
		dst = append(dst, 0)
	}
	return dst
}
