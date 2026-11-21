package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"slices"
	"time"
)

type metadataDSDIFF struct {
	*metadataID3v2
	title, artist  *string
	rate, channels int
	duration       time.Duration
}

func (m metadataDSDIFF) FileType() FileType { return DFF }
func (m metadataDSDIFF) Title() string {
	if m.title != nil {
		return *m.title
	}
	return m.metadataID3v2.Title()
}
func (m metadataDSDIFF) Artist() string {
	if m.artist != nil {
		return *m.artist
	}
	return m.metadataID3v2.Artist()
}
func (m metadataDSDIFF) Artists() []string {
	if m.artist != nil {
		return singleArtistList(*m.artist)
	}
	return m.metadataID3v2.Artists()
}
func (m metadataDSDIFF) SampleRate() int         { return m.rate }
func (m metadataDSDIFF) BitRate() int            { return 0 }
func (m metadataDSDIFF) Duration() time.Duration { return m.duration }

func walkDSDIFF(r io.ReadSeeker, visit func(fileChunk) error) error {
	size, err := getFileSize(r)
	if err != nil {
		return err
	}
	h, err := readAt(r, 0, 16)
	if err != nil {
		return err
	}
	if string(h[:4]) != "FRM8" || string(h[12:16]) != "DSD " || binary.BigEndian.Uint64(h[4:12]) != uint64(size-12) {
		return errors.New("invalid DSDIFF form")
	}
	return walkChunks(r, 16, size, true, visit)
}

func readDSDIFF(r io.ReadSeeker) (Metadata, error) {
	id3, _ := readOptionalID3(r, 0, 0)
	m := metadataDSDIFF{metadataID3v2: id3}
	var codec, declaredCodec string
	var dsdBytes int64
	err := walkDSDIFF(r, func(c fileChunk) error {
		switch c.id {
		case "FVER":
			if c.size != 4 {
				return errors.New("invalid DSDIFF version chunk")
			}
		case "PROP":
			if c.size < 4 {
				return io.ErrUnexpectedEOF
			}
			h, err := readAt(r, c.data, 4)
			if err != nil {
				return err
			}
			if string(h) != "SND " {
				return nil
			}
			return walkChunks(r, c.data+4, c.data+c.size, true, func(p fileChunk) error {
				var n int
				switch p.id {
				case "FS  ", "CMPR":
					n = 4
				case "CHNL":
					n = 2
				default:
					return nil
				}
				if p.size < int64(n) {
					return io.ErrUnexpectedEOF
				}
				value, err := readAt(r, p.data, n)
				if err != nil {
					return err
				}
				switch p.id {
				case "FS  ":
					m.rate = int(binary.BigEndian.Uint32(value))
				case "CHNL":
					m.channels = int(binary.BigEndian.Uint16(value))
					if p.size != int64(2+4*m.channels) {
						return errors.New("invalid DSDIFF channel list")
					}
				case "CMPR":
					declaredCodec = string(value)
				}
				return nil
			})
		case "DSD ":
			if codec != "" || c.size == 0 {
				return errors.New("invalid DSDIFF audio data")
			}
			codec, dsdBytes = c.id, c.size
		case "DST ":
			if codec != "" {
				return errors.New("multiple DSDIFF audio chunks")
			}
			codec = c.id
			var count, frames int
			err := walkChunks(r, c.data, c.data+c.size, true, func(p fileChunk) error {
				switch p.id {
				case "FRTE":
					if p.size != 6 || frames != 0 {
						return errors.New("invalid DST frame information")
					}
					b, err := chunkData(r, p)
					if err != nil {
						return err
					}
					count, frames = int(binary.BigEndian.Uint32(b[:4])), int(binary.BigEndian.Uint16(b[4:]))
				case "DSTF":
					if frames == 0 || p.size == 0 {
						return errors.New("invalid DST frame")
					}
					count--
				}
				return nil
			})
			if err != nil {
				return err
			}
			b, err := readAt(r, c.data, 18)
			if err != nil || frames == 0 || count != 0 || string(b[:4]) != "FRTE" {
				return errors.New("invalid DST frame sequence")
			}
			m.duration = sampleDuration(uint64(binary.BigEndian.Uint32(b[12:16])), frames)
		case "DIIN":
			return walkChunks(r, c.data, c.data+c.size, true, func(p fileChunk) error {
				if p.id != "DITI" && p.id != "DIAR" {
					return nil
				}
				b, err := chunkData(r, p)
				if err != nil {
					return err
				}
				if len(b) < 4 || binary.BigEndian.Uint32(b[:4]) != uint32(len(b)-4) {
					return errors.New("invalid DSDIFF text chunk")
				}
				value := fixEncoding(b[4:])
				if p.id == "DITI" {
					m.title = &value
				} else {
					m.artist = &value
				}
				return nil
			})
		case "ID3 ", "id3 ":
			tags, err := readOptionalID3(r, c.data, c.size)
			if err != nil {
				return err
			}
			m.metadataID3v2 = tags
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if m.rate <= 0 || m.channels <= 0 || codec == "" || (declaredCodec != "" && declaredCodec != codec) {
		return nil, errors.New("missing DSDIFF stream properties")
	}
	if dsdBytes > 0 {
		if dsdBytes%int64(m.channels) != 0 {
			return nil, errors.New("incomplete DSDIFF sample")
		}
		m.duration = sampleDuration(8*(uint64(dsdBytes)/uint64(m.channels)), m.rate)
	}
	return m, nil
}

func writeDSDIFF(path string, opts WriteOptions) error {
	changed := map[string]string{}
	if opts.Title != "" || slices.Contains(opts.ClearFields, FieldTitle) {
		changed["DITI"] = opts.Title
	}
	if opts.Artist != "" || slices.Contains(opts.ClearFields, FieldArtist) {
		changed["DIAR"] = opts.Artist
	}
	if len(changed) == 0 {
		return ErrUnsupportedWrite
	}

	return atomicRewrite(path, DFF, func(src, dst *os.File, size int64) error {
		var diin fileChunk
		if err := walkDSDIFF(src, func(c fileChunk) error {
			if c.id == "DIIN" {
				if diin.id != "" {
					return unsupportedLayout("multiple DSDIFF master chunks")
				}
				diin = c
			}
			return nil
		}); err != nil {
			return err
		}
		var body bytes.Buffer
		if diin.id != "" {
			err := walkChunks(src, diin.data, diin.data+diin.size, true, func(c fileChunk) error {
				if _, replace := changed[c.id]; replace {
					return nil
				}
				if c.end-c.start > int64(maxMetadataSize-body.Len()) {
					return unsupportedLayout("oversized DSDIFF master chunk")
				}
				return copySection(&body, src, c.start, c.end-c.start)
			})
			if err != nil {
				return err
			}
		}
		for _, id := range []string{"DITI", "DIAR"} {
			if value, ok := changed[id]; ok {
				data := append(binary.BigEndian.AppendUint32(nil, uint32(len(value))), value...)
				body.Write(appendChunk(nil, id, data, true))
			}
		}
		edited := appendChunk(nil, "DIIN", body.Bytes(), true)
		start, end := diin.start, diin.end
		if diin.id == "" {
			start, end = size, size
		}
		if end != size && int64(len(edited)) != end-start {
			return unsupportedLayout("DSDIFF metadata before indexed data")
		}
		h, err := readAt(src, 0, 16)
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint64(h[4:12], uint64(size-12+int64(len(edited))-(end-start)))
		if _, err := dst.Write(h); err != nil {
			return err
		}
		if err := copySection(dst, src, 16, start-16); err != nil {
			return err
		}
		if _, err := dst.Write(edited); err != nil {
			return err
		}
		return copySection(dst, src, end, size-end)
	})
}
