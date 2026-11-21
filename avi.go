package tag

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
)

type metadataAVI struct{ *metadataWAV }

func (m metadataAVI) FileType() FileType { return AVI }
func (m metadataAVI) SampleRate() int    { return 0 }
func (m metadataAVI) BitRate() int       { return 0 }
func (m metadataAVI) Format() Format     { return RIFFInfo }

func walkAVI(r io.ReadSeeker, visit func(fileChunk) error) (int, error) {
	size, err := getFileSize(r)
	if err != nil {
		return 0, err
	}
	segments := 0
	var hdrl, movi bool
	for offset := int64(0); offset < size; {
		if size-offset < 12 {
			return 0, io.ErrUnexpectedEOF
		}
		h, err := readAt(r, offset, 12)
		if err != nil {
			return 0, err
		}
		form := string(h[8:12])
		length := int64(binary.LittleEndian.Uint32(h[4:8]))
		end := offset + 8 + length
		if string(h[:4]) != "RIFF" || (segments == 0 && form != "AVI ") || (segments > 0 && form != "AVIX") || length < 4 || end+(length&1) > size {
			return 0, errors.New("invalid AVI RIFF segment")
		}
		err = walkChunks(r, offset+12, end, false, func(c fileChunk) error {
			if c.id == "LIST" {
				if c.size < 4 {
					return io.ErrUnexpectedEOF
				}
				t, err := readAt(r, c.data, 4)
				if err != nil {
					return err
				}
				hdrl = hdrl || string(t) == "hdrl"
				movi = movi || string(t) == "movi"
			}
			return visit(c)
		})
		if err != nil {
			return 0, err
		}
		segments++
		offset = end + (length & 1)
	}
	if !hdrl || !movi {
		return 0, errors.New("AVI header or movie list missing")
	}
	return segments, nil
}

func readAVI(r io.ReadSeeker) (Metadata, error) {
	m := &metadataWAV{}
	_, err := walkAVI(r, func(c fileChunk) error {
		if c.id != "LIST" {
			return nil
		}
		t, err := readAt(r, c.data, 4)
		if err != nil || string(t) != "INFO" {
			return err
		}
		return m.readLISTInfo(r, c.size-4)
	})
	return metadataAVI{m}, err
}

func writeAVI(path string, opts WriteOptions) error {
	changes := riffTextOptions(id3Options(opts, FieldLyrics))
	if len(changes) == 0 {
		return ErrUnsupportedWrite
	}
	return atomicRewrite(path, AVI, func(src, dst *os.File, size int64) error {
		var info fileChunk
		segments, err := walkAVI(src, func(c fileChunk) error {
			if c.id != "LIST" {
				return nil
			}
			b, err := readAt(src, c.data, 4)
			if err != nil || string(b) != "INFO" {
				return err
			}
			if info.id != "" {
				return unsupportedLayout("multiple AVI INFO chunks")
			}
			info = c
			return nil
		})
		if err != nil {
			return err
		}
		if segments != 1 {
			return unsupportedLayout("OpenDML AVI")
		}
		// A one-segment OpenDML file still contains a dmlh or super index.
		if err := walkChunks(src, 12, size, false, func(c fileChunk) error {
			if c.id != "LIST" {
				return nil
			}
			b, err := readAt(src, c.data, 4)
			if err != nil || string(b) != "hdrl" {
				return err
			}
			return checkClassicAVIHeader(src, c.data+4, c.data+c.size, 0)
		}); err != nil {
			return err
		}
		body, err := editRIFFInfo(src, info, changes)
		if err != nil {
			return err
		}
		newInfo := appendChunk(nil, "LIST", body, false)
		tail := size
		if info.end == size {
			tail = info.start
		}
		end := tail + int64(len(newInfo))
		if end-8 > math.MaxUint32 {
			return unsupportedLayout("AVI RIFF size")
		}
		if err := copySection(dst, src, 0, tail); err != nil {
			return err
		}
		if _, err := dst.Write(newInfo); err != nil {
			return err
		}
		if info.id != "" && tail == size {
			if _, err := dst.WriteAt([]byte("JUNK"), info.start); err != nil {
				return err
			}
		}
		_, err = dst.WriteAt(binary.LittleEndian.AppendUint32(nil, uint32(end-8)), 4)
		return err
	})
}

func checkClassicAVIHeader(r io.ReadSeeker, start, end int64, depth int) error {
	if depth > 4 {
		return unsupportedLayout("nested AVI headers")
	}
	return walkChunks(r, start, end, false, func(c fileChunk) error {
		switch c.id {
		case "indx", "dmlh":
			return unsupportedLayout("OpenDML AVI index")
		case "LIST":
			if c.size < 4 {
				return io.ErrUnexpectedEOF
			}
			b, err := readAt(r, c.data, 4)
			if err != nil {
				return err
			}
			if string(b) == "odml" {
				return unsupportedLayout("OpenDML AVI header")
			}
			return checkClassicAVIHeader(r, c.data+4, c.data+c.size, depth+1)
		}
		return nil
	})
}
