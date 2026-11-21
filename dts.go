package tag

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	dtsCoreBE   = 0x7ffe8001
	dtsCoreLE   = 0xfe7f0180
	dtsCore14BE = 0x1fffe800
	dtsCore14LE = 0xff1f00e8
	dtsEXSS     = 0x64582025
)

var dtsCoreRates = [...]int{0, 8000, 16000, 32000, 0, 0, 11025, 22050, 44100, 0, 0, 12000, 24000, 48000, 96000, 192000}
var dtsModes = [...]int{1, 2, 2, 2, 2, 3, 3, 4, 4, 5}

type mediaBits struct {
	data []byte
	pos  int
}

func (b *mediaBits) take(n int) uint32 {
	var v uint32
	for range n {
		v = v<<1 | uint32(b.data[b.pos/8]>>(7-b.pos%8)&1)
		b.pos++
	}
	return v
}

func isDTS(h []byte) bool {
	switch binary.BigEndian.Uint32(h[:4]) {
	case dtsCoreBE, dtsCoreLE, dtsCore14BE, dtsCore14LE, dtsEXSS:
		return true
	}
	return len(h) >= 8 && string(h[:8]) == "DTSHDHDR"
}

func readDTS(r io.ReadSeeker) (Metadata, error) {
	m := newLegacyMetadata(DTS, UnknownFormat)
	end, err := getFileSize(r)
	if err != nil {
		return nil, err
	}
	start := int64(0)
	h, err := readAt(r, 0, 8)
	if err != nil {
		return nil, err
	}
	var declaredRate int
	var declaredSamples uint64
	if string(h) == "DTSHDHDR" {
		var dataEnd int64
		for pos := int64(0); pos < end; {
			if end-pos < 16 {
				return nil, io.ErrUnexpectedEOF
			}
			h, err := readAt(r, pos, 16)
			if err != nil {
				return nil, err
			}
			n := binary.BigEndian.Uint64(h[8:])
			if n < 4 || n > uint64(end-pos-16) {
				return nil, errors.New("invalid DTS-HD chunk size")
			}
			pos += 16
			switch string(h[:8]) {
			case "AUPR-HDR":
				if n < 21 {
					return nil, io.ErrUnexpectedEOF
				}
				b, err := readAt(r, pos, 21)
				if err != nil {
					return nil, err
				}
				declaredRate = int(b[3])<<16 | int(b[4])<<8 | int(b[5])
				declaredSamples = uint64(binary.BigEndian.Uint32(b[6:10])) * uint64(binary.BigEndian.Uint16(b[10:12]))
			case "STRMDATA":
				if dataEnd != 0 {
					return nil, errors.New("multiple DTS-HD stream chunks")
				}
				start, dataEnd = pos, pos+int64(n)
			}
			pos += int64(n)
		}
		if dataEnd == 0 {
			return nil, errors.New("missing DTS-HD stream data")
		}
		end = dataEnd
	}
	var samples uint64
	var rate, channels, frames int
	haveExtension := false
	for pos := start; pos < end; {
		if end-pos < 12 {
			return nil, io.ErrUnexpectedEOF
		}
		h, err := readAt(r, pos, int(min(16, end-pos)))
		if err != nil {
			return nil, err
		}
		n := 0
		switch marker := binary.BigEndian.Uint32(h[:4]); marker {
		case dtsEXSS:
			b := mediaBits{h, 42}
			wide := int(b.take(1))
			header := int(b.take(8+4*wide)) + 1
			n = int(b.take(16+4*wide)) + 1
			if header < 9 || n < header {
				return nil, errors.New("invalid DTS-HD extension frame")
			}
			haveExtension = true
		case dtsCoreBE, dtsCoreLE, dtsCore14BE, dtsCore14LE:
			little := marker == dtsCoreLE || marker == dtsCore14LE
			packed := marker == dtsCore14BE || marker == dtsCore14LE
			decoded := h
			if packed {
				decoded = nil
				var acc uint32
				bits := 0
				for i := 0; i+1 < len(h); i += 2 {
					word := binary.BigEndian.Uint16(h[i:])
					if little {
						word = binary.LittleEndian.Uint16(h[i:])
					}
					acc = acc<<14 | uint32(word&0x3fff)
					bits += 14
					for bits >= 8 {
						bits -= 8
						decoded = append(decoded, byte(acc>>bits))
					}
				}
			} else if little {
				for i := 0; i+1 < len(h); i += 2 {
					h[i], h[i+1] = h[i+1], h[i]
				}
			}
			if len(decoded) < 11 || binary.BigEndian.Uint32(decoded) != dtsCoreBE {
				return nil, errors.New("invalid DTS core header")
			}
			b := mediaBits{decoded, 33}
			deficit := b.take(5) + 1
			b.take(1)
			blocks := b.take(7) + 1
			n = int(b.take(14)) + 1
			mode := int(b.take(6))
			frameRate := dtsCoreRates[b.take(4)]
			b.take(5)
			reserved := b.take(1)
			b.take(9)
			lfe := b.take(2)
			if reserved != 0 || lfe == 3 || deficit != 32 || blocks%8 != 0 || n < 96 || mode >= len(dtsModes) || frameRate == 0 || rate != 0 && rate != frameRate {
				return nil, errors.New("invalid DTS core parameters")
			}
			rate, channels = frameRate, dtsModes[mode]
			if lfe != 0 {
				channels++
			}
			samples += uint64(32 * blocks)
			if packed {
				n = n * 16 / 14
			}
		default:
			return nil, errors.New("invalid DTS frame marker")
		}
		if n <= 0 || int64(n) > end-pos {
			return nil, io.ErrUnexpectedEOF
		}
		frames++
		pos += int64(n)
	}
	if frames == 0 {
		return nil, errors.New("missing DTS frames")
	}
	if declaredRate != 0 && declaredSamples != 0 {
		rate, samples = declaredRate, declaredSamples
	}
	if haveExtension && declaredRate == 0 {
		// The DTS core parameters need not match the selected HD presentation.
		rate, channels, samples = 0, 0, 0
	}
	m.sampleRate, m.channels, m.duration = rate, channels, sampleDuration(samples, rate)
	m.fileSize = end - start
	if m.raw == nil {
		m.raw = make(map[string]interface{})
	}
	m.raw["dts_hd"] = haveExtension
	if channels > 0 {
		m.raw["channels"] = channels
	}
	return metadataDTS{m}, nil
}

type metadataDTS struct{ metadataLegacy }

func (m metadataDTS) BitRate() int {
	if m.duration > 0 {
		return int(float64(m.fileSize) * 0.008 / m.duration.Seconds())
	}
	return 0
}
