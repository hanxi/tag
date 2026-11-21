package tag

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// MPEGService and MPEGStream are scoped descriptions, exposed through Raw.
// DVB service names describe a multiplex service, never a song title.
type MPEGService struct{ Name, Provider string }
type MPEGStream struct {
	Type      byte
	Languages []string
}

func isTransportStream(r io.ReadSeeker) (stride, sync int) {
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, 0
	}
	defer r.Seek(pos, io.SeekStart)
	b := make([]byte, 4*204+4)
	n, _ := io.ReadFull(r, b)
	for _, layout := range [][2]int{{188, 0}, {192, 4}, {204, 0}} {
		stride, sync = layout[0], layout[1]
		if n < sync+2*stride+4 {
			continue
		}
		valid := true
		for i := sync; i+4 <= n; i += stride {
			if b[i] != 0x47 || (b[i+3]>>4)&3 == 0 {
				valid = false
				break
			}
		}
		if valid {
			return stride, sync
		}
	}
	return 0, 0
}

type psiState struct {
	data []byte
	next byte
}

func (p *psiState) consume(payload []byte, start bool, cc byte, visit func([]byte)) {
	if len(payload) == 0 {
		return
	}
	if p.next != cc {
		p.data = nil
	}
	p.next = (cc + 1) & 15
	if start {
		length := int(payload[0])
		payload = payload[1:]
		if length > len(payload) {
			p.data = nil
			return
		}
		if len(p.data) > 0 {
			p.add(payload[:length], visit)
		}
		p.data = nil
		payload = payload[length:]
	} else if len(p.data) == 0 {
		return
	}
	p.add(payload, visit)
}

func (p *psiState) add(in []byte, visit func([]byte)) {
	p.data = append(p.data, in...)
	for len(p.data) >= 3 {
		if p.data[0] == 0xff {
			p.data = nil
			return
		}
		n := 3 + (int(p.data[1]&15)<<8 | int(p.data[2]))
		if n < 7 || n > 4096 {
			p.data = nil
			return
		}
		if n > len(p.data) {
			return
		}
		if mpegCRC(p.data[:n]) == 0 {
			visit(p.data[:n-4])
		}
		p.data = p.data[n:]
	}
}

func mpegCRC(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, x := range b {
		crc ^= uint32(x) << 24
		for range 8 {
			if crc>>31 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func streamLanguages(b []byte) []string {
	var langs []string
	for len(b) >= 2 {
		n := int(b[1])
		if n > len(b)-2 {
			break
		}
		if b[0] == 0x0a {
			for at := 2; at+4 <= n+2; at += 4 {
				langs = append(langs, string(b[at:at+3]))
			}
		}
		b = b[n+2:]
	}
	return langs
}

func readTransportStream(r io.ReadSeeker, stride, sync int) (Metadata, error) {
	m := newLegacyMetadata(MPEGTS, UnknownFormat)
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	services := make(map[uint16]MPEGService)
	streams := make(map[uint16]MPEGStream)
	psi := map[uint16]*psiState{0: {}, 0x11: {}}
	var pes, pat bool
	reader := bufio.NewReaderSize(r, 64<<10)
	b := make([]byte, stride)
	for {
		_, err := io.ReadFull(reader, b)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		packet := b[sync : sync+188]
		adapt := packet[3] >> 4 & 3
		if packet[0] != 0x47 || adapt == 0 {
			return nil, errors.New("invalid MPEG-TS packet header")
		}
		at := 4
		if adapt&2 != 0 {
			at += 1 + int(packet[4])
		}
		if at > 188 {
			return nil, errors.New("invalid MPEG-TS adaptation field")
		}
		if adapt&1 == 0 || at == 188 || packet[1]&0x80 != 0 {
			continue
		}
		start := packet[1]&0x40 != 0
		pid := binary.BigEndian.Uint16(packet[1:3]) & 0x1fff
		payload := packet[at:]
		if start && len(payload) >= 4 && bytes.HasPrefix(payload, []byte{0, 0, 1}) && isPSMediaID(payload[3]) {
			pes = true
		}
		if state := psi[pid]; state != nil {
			state.consume(payload, start, packet[3]&15, func(section []byte) {
				if len(section) < 8 || section[5]&1 == 0 {
					return
				}
				switch section[0] {
				case 0x00:
					if pid != 0 {
						return
					}
					pat = true
					for table := section[8:]; len(table) >= 4; table = table[4:] {
						next := binary.BigEndian.Uint16(table[2:4]) & 0x1fff
						if binary.BigEndian.Uint16(table[:2]) != 0 && psi[next] == nil {
							psi[next] = &psiState{}
						}
					}
				case 0x02:
					if len(section) < 12 {
						return
					}
					at := 12 + int(binary.BigEndian.Uint16(section[10:12])&0xfff)
					for at+5 <= len(section) {
						pid := binary.BigEndian.Uint16(section[at+1:at+3]) & 0x1fff
						n := int(binary.BigEndian.Uint16(section[at+3:at+5]) & 0xfff)
						if at+5+n > len(section) {
							break
						}
						streams[pid] = MPEGStream{section[at], streamLanguages(section[at+5 : at+5+n])}
						at += 5 + n
					}
				case 0x42:
					if pid == 0x11 && len(section) >= 11 {
						readSDT(section[11:], services)
					}
				}
			})
		}
	}
	if !pes || !pat || len(streams) == 0 {
		return nil, errors.New("missing MPEG-TS program or media packets")
	}
	m.raw = map[string]interface{}{"services": services, "streams": streams}
	return m, nil
}

func readSDT(b []byte, services map[uint16]MPEGService) {
	for len(b) >= 5 {
		id := binary.BigEndian.Uint16(b[:2])
		n := int(binary.BigEndian.Uint16(b[3:5]) & 0xfff)
		if n > len(b)-5 {
			return
		}
		desc := b[5 : 5+n]
		b = b[5+n:]
		for len(desc) >= 2 {
			size := int(desc[1])
			if size > len(desc)-2 {
				break
			}
			d := desc[2 : 2+size]
			if desc[0] == 0x48 && len(d) >= 3 {
				provider := int(d[1])
				if provider+3 <= len(d) {
					name := int(d[2+provider])
					if provider+3+name <= len(d) {
						services[id] = MPEGService{dvbText(d[3+provider : 3+provider+name]), dvbText(d[2 : 2+provider])}
					}
				}
			}
			desc = desc[2+size:]
		}
	}
}

func dvbText(b []byte) string {
	if len(b) > 0 && b[0] == 0x15 {
		return fixEncoding(b[1:])
	}
	// DVB's default table is ISO 6937. Preserve its ASCII subset; decode other
	// charsets only when identified, leaving uncertain text out.
	for _, v := range b {
		if v < 0x20 || v >= 0x7f {
			return ""
		}
	}
	return string(b)
}

func isPSMediaID(id byte) bool { return id == 0xbd || id >= 0xc0 && id <= 0xef || id == 0xfd }

func readProgramStream(r io.ReadSeeker) (Metadata, error) {
	m := newLegacyMetadata(MPEGPS, UnknownFormat)
	b := bufio.NewReaderSize(r, 64<<10)
	var media, pack bool
	streams := make(map[uint16]MPEGStream)
	consume := func(n int) error { _, err := b.Discard(n); return err }
	for {
		h, err := b.Peek(4)
		if err == io.EOF && len(h) == 0 {
			break
		}
		if err != nil {
			return nil, io.ErrUnexpectedEOF
		}
		if !bytes.HasPrefix(h, []byte{0, 0, 1}) {
			return nil, errors.New("invalid MPEG-PS start code")
		}
		id := h[3]
		if err := consume(4); err != nil {
			return nil, err
		}
		switch id {
		case 0xba:
			x, err := b.Peek(1)
			if err != nil {
				return nil, err
			}
			switch {
			case x[0]&0xc4 == 0x44:
				h, err := b.Peek(10)
				if err != nil {
					return nil, err
				}
				if h[2]&4 == 0 || h[4]&4 == 0 || h[5]&1 == 0 || h[8]&3 != 3 {
					return nil, errors.New("invalid MPEG-2 pack markers")
				}
				if err := consume(10 + int(h[9]&7)); err != nil {
					return nil, err
				}
			case x[0]&0xf1 == 0x21:
				h, err := b.Peek(8)
				if err != nil {
					return nil, err
				}
				if h[2]&1 == 0 || h[4]&1 == 0 || h[5]&0x80 == 0 || h[7]&1 == 0 {
					return nil, errors.New("invalid MPEG-1 pack markers")
				}
				if err := consume(8); err != nil {
					return nil, err
				}
			default:
				return nil, errors.New("invalid MPEG-PS pack header")
			}
			pack = true
		case 0xb9:
			if !pack || !media {
				return nil, errors.New("empty MPEG program")
			}
		default:
			if id < 0xbb {
				return nil, errors.New("invalid MPEG-PS packet ID")
			}
			h, err := b.Peek(2)
			if err != nil {
				return nil, err
			}
			n := int(binary.BigEndian.Uint16(h))
			if err := consume(2); err != nil {
				return nil, err
			}
			if n == 0 && id >= 0xe0 && id <= 0xef {
				for {
					h, err := b.Peek(4)
					if err != nil || bytes.HasPrefix(h, []byte{0, 0, 1}) && h[3] >= 0xb9 {
						break
					}
					if err := consume(1); err != nil {
						return nil, err
					}
				}
			} else if id == 0xbc {
				buf := make([]byte, n)
				if _, err := io.ReadFull(b, buf); err != nil {
					return nil, err
				}
				if err := readProgramMap(buf, streams); err != nil {
					return nil, err
				}
			} else if err := consume(n); err != nil {
				return nil, err
			}
			media = media || isPSMediaID(id)
		}
	}
	if !pack || !media {
		return nil, errors.New("missing MPEG-PS pack or media packet")
	}
	m.raw = map[string]interface{}{"streams": streams}
	return m, nil
}

func readProgramMap(b []byte, streams map[uint16]MPEGStream) error {
	if len(b) < 10 {
		return io.ErrUnexpectedEOF
	}
	at := 4 + int(binary.BigEndian.Uint16(b[2:4]))
	if at > len(b)-6 {
		return io.ErrUnexpectedEOF
	}
	length := int(binary.BigEndian.Uint16(b[at : at+2]))
	at += 2
	if length > len(b)-at-4 {
		return io.ErrUnexpectedEOF
	}
	for end := at + length; at < end; {
		if end-at < 4 {
			return io.ErrUnexpectedEOF
		}
		n := int(binary.BigEndian.Uint16(b[at+2 : at+4]))
		if n > end-at-4 {
			return io.ErrUnexpectedEOF
		}
		streams[uint16(b[at+1])] = MPEGStream{b[at], streamLanguages(b[at+4 : at+4+n])}
		at += 4 + n
	}
	return nil
}
