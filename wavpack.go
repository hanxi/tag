package tag

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
)

type apeItem struct {
	key        string
	flags      uint32
	value, raw []byte
}

func parseAPEItems(data []byte, count uint32) ([]apeItem, error) {
	items := make([]apeItem, 0, min(int(count), 128))
	for range count {
		if len(data) < 10 {
			return nil, errors.New("truncated APEv2 item")
		}
		n := binary.LittleEndian.Uint32(data[:4])
		k := bytes.IndexByte(data[8:min(264, len(data))], 0)
		if k < 2 || uint64(n) > uint64(len(data)-9-k) {
			return nil, errors.New("invalid APEv2 item")
		}
		start := 9 + k
		end := start + int(n)
		items = append(items, apeItem{string(data[8 : start-1]), binary.LittleEndian.Uint32(data[4:8]), data[start:end], data[:end]})
		data = data[end:]
	}
	if len(data) != 0 {
		return nil, errors.New("invalid APEv2 item count")
	}
	return items, nil
}

// readAPETail returns the audio end and the start of an optional ID3v1 trailer.
func readAPETail(r io.ReadSeeker, size int64, allowLegacy bool) (int64, int64, []apeItem, error) {
	end := size
	if size >= 128 {
		h, err := readAt(r, size-128, 3)
		if err != nil {
			return 0, 0, nil, err
		}
		if string(h) == "TAG" {
			end -= 128
		}
	}
	if end < 32 {
		return end, end, nil, nil
	}
	footer, err := readAt(r, end-32, 32)
	if err != nil {
		return 0, 0, nil, err
	}
	if string(footer[:8]) != "APETAGEX" {
		return end, end, nil, nil
	}
	u32 := binary.LittleEndian.Uint32
	sizeTag := int64(u32(footer[12:16]))
	flags := u32(footer[20:24])
	version := u32(footer[8:12])
	if (version != 2000 && !(allowLegacy && version == 1000)) || sizeTag < 32 || sizeTag > min(end, maxMetadataSize) || flags&0x60000000 != 0 {
		return 0, 0, nil, errors.New("invalid APEv2 footer")
	}
	start := end - sizeTag
	b, err := readAt(r, start, int(sizeTag-32))
	if err != nil {
		return 0, 0, nil, err
	}
	if flags&0x80000000 != 0 {
		if start < 32 {
			return 0, 0, nil, errors.New("truncated APEv2 header")
		}
		header, err := readAt(r, start-32, 32)
		if err != nil || !bytes.Equal(header[:20], footer[:20]) || u32(header[20:24]) != flags|0x20000000 {
			return 0, 0, nil, errors.New("invalid APEv2 header")
		}
		start -= 32
	}
	items, err := parseAPEItems(b, u32(footer[16:20]))
	return start, end, items, err
}

var wavPackRates = [...]int{6000, 8000, 9600, 11025, 12000, 16000, 22050, 24000, 32000, 44100, 48000, 64000, 88200, 96000, 192000, 0}

func readWavPackBlocks(r io.ReadSeeker, end int64) (int, uint64, error) {
	var rate int
	var samples, declaredSamples uint64
	for pos := int64(0); pos < end; {
		h, err := readAt(r, pos, 32)
		if err != nil {
			return 0, 0, err
		}
		length := int64(binary.LittleEndian.Uint32(h[4:8])) + 8
		version := binary.LittleEndian.Uint16(h[8:10])
		if string(h[:4]) != "wvpk" || version < 0x402 || version > 0x410 || length < 32 || length > end-pos || length&1 != 0 {
			return 0, 0, errors.New("invalid WavPack block")
		}
		u32 := binary.LittleEndian.Uint32
		if pos == 0 {
			// All-ones means the encoder did not know the final sample count.
			// WavPack 5 uses a biased high byte to keep that low word reserved.
			if count := u32(h[12:16]); count != 0xffffffff {
				declaredSamples = uint64(count) + uint64(h[11])*0xffffffff
			}
		}

		flags := u32(h[24:28])
		blockSamples := u32(h[20:24])
		if blockSamples != 0 {
			blockRate := wavPackRates[flags>>23&15]
			// WavPack DSD/custom rates require block metadata. Let the caller probe them.
			if flags>>31 != 0 {
				blockRate = 0
			}
			rate = blockRate
			index := uint64(u32(h[16:20])) | uint64(h[10])<<32
			samples = max(samples, index+uint64(blockSamples))
		}
		pos += length
	}
	if samples == 0 {
		return 0, 0, errors.New("missing WavPack audio blocks")
	}
	if declaredSamples != 0 && declaredSamples != samples {
		return 0, 0, errors.New("WavPack sample count does not match its blocks")
	}
	return rate, samples, nil
}

type metadataWavPack struct{ *metadataAPE }

func (m metadataWavPack) FileType() FileType { return WV }
func (m metadataWavPack) BitRate() int {
	if m.duration > 0 {
		return int(float64(m.fileSize) * 0.008 / m.duration.Seconds())
	}
	return 0
}

func readWavPack(r io.ReadSeeker) (Metadata, error) {
	size, err := getFileSize(r)
	if err != nil {
		return nil, err
	}
	end, trailer, items, err := readAPETail(r, size, false)
	if err != nil {
		return nil, err
	}
	rate, samples, err := readWavPackBlocks(r, end)
	if err != nil {
		return nil, err
	}
	m := &metadataAPE{fileSize: end, sampleRate: rate, duration: sampleDuration(samples, rate)}
	for _, item := range items {
		m.applyAPEItem(item.key, item.flags, item.value)
	}
	if len(items) == 0 && trailer != size {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		if old, err := ReadID3v1Tags(r); err == nil {
			m.title, m.artist, m.album, m.year, m.genre = old.Title(), old.Artist(), old.Album(), old.Year(), old.Genre()
		}
	}
	return metadataWavPack{m}, nil
}

func writeAPEv2(path string, kind FileType, opts WriteOptions) error {
	if !hasTagEdits(opts) {
		return ErrUnsupportedWrite
	}
	return atomicRewrite(path, kind, func(src, dst *os.File, size int64) error {
		end, trailer, old, err := readAPETail(src, size, kind == APE)
		if err != nil {
			return err
		}
		if kind == WV {
			if _, _, err := readWavPackBlocks(src, end); err != nil {
				return err
			}
		}
		// A second tag with legacy values could resurrect explicitly cleared fields.
		if kind == WV && trailer != size && len(opts.ClearFields) != 0 {
			return unsupportedLayout("WavPack with legacy ID3v1")
		}
		built, count := buildAPEv2Items(opts)
		items, err := parseAPEItems(built, count)
		if err != nil {
			return err
		}
		key := func(s string) string {
			s = strings.ToLower(s)
			if s == "discnumber" {
				return "disc"
			}
			return s
		}
		changed := make(map[string]bool)
		for _, item := range items {
			changed[key(item.key)] = true
		}
		if opts.Incremental {
			for name := range opts.CustomTags {
				if name != "" {
					changed[key(name)] = true
				}
			}
		}
		for _, field := range opts.ClearFields {
			switch field {
			case FieldPicture:
				field = "cover art (front)"
			case FieldAlbumArtist:
				field = "album artist"
			}
			changed[key(field)] = true
		}
		for _, item := range old {
			if changed[key(item.key)] {
				if item.flags&1 != 0 {
					return unsupportedLayout("read-only APEv2 item")
				}
				continue
			}
			built = append(built, item.raw...)
			count++
		}
		if len(built)+32 > maxMetadataSize {
			return unsupportedLayout("APEv2 metadata size")
		}
		if err := copySection(dst, src, 0, end); err != nil {
			return err
		}
		if _, err := dst.Write(built); err != nil {
			return err
		}
		if _, err := dst.Write(buildAPETagEXFooter(uint32(len(built))+32, count)); err != nil {
			return err
		}
		return copySection(dst, src, trailer, size-trailer)
	})
}
