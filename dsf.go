// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"time"
)

type dsfLayout struct {
	size, audioEnd       int64
	sampleRate, channels int
	samples              uint64
}

func parseDSF(r io.ReadSeeker) (dsfLayout, error) {
	var d dsfLayout
	size, err := getFileSize(r)
	if err != nil {
		return d, err
	}
	h, err := readAt(r, 0, 80)
	if err != nil {
		return d, err
	}
	u32, u64 := binary.LittleEndian.Uint32, binary.LittleEndian.Uint64
	bits := u32(h[60:64])
	if string(h[:4]) != "DSD " || u64(h[4:12]) != 28 || u64(h[12:20]) != uint64(size) ||
		string(h[28:32]) != "fmt " || u64(h[32:40]) != 52 || u32(h[40:44]) != 1 ||
		u32(h[44:48]) != 0 || (bits != 1 && bits != 8) {
		return d, errors.New("invalid DSF header")
	}
	rawEnd := u64(h[20:28])
	if rawEnd == 0 {
		rawEnd = uint64(size)
	}
	if rawEnd < 92 || rawEnd > uint64(size) {
		return d, errors.New("invalid DSF metadata pointer")
	}
	data, err := readAt(r, 80, 12)
	if err != nil {
		return d, err
	}
	if string(data[:4]) != "data" || u64(data[4:12]) != rawEnd-80 {
		return d, errors.New("invalid DSF data chunk length")
	}
	d = dsfLayout{size, int64(rawEnd), int(u32(h[56:60])), int(u32(h[52:56])), u64(h[64:72])}
	if d.sampleRate == 0 || d.channels < 1 || d.channels > 6 || d.samples/8 > (rawEnd-92)/uint64(d.channels) {
		return d, errors.New("invalid DSF stream parameters")
	}
	return d, nil
}

// ReadDSFMeta reads Sony DSF stream parameters and the optional trailing ID3v2 tag.
func ReadDSFMeta(r io.ReadSeeker) (Metadata, error) {
	d, err := parseDSF(r)
	if err != nil {
		return nil, err
	}
	id3, err := readOptionalID3(r, d.audioEnd, d.size-d.audioEnd)
	if err != nil {
		return nil, err
	}
	return metadataDSF{id3, sampleDuration(d.samples, d.sampleRate), d.sampleRate, d.channels}, nil
}

func sampleDuration(samples uint64, sampleRate int) time.Duration {
	if sampleRate <= 0 {
		return 0
	}
	rate := uint64(sampleRate)
	seconds := samples / rate
	if seconds >= uint64((1<<63-1)/int64(time.Second)) {
		return 0
	}
	return time.Duration(seconds)*time.Second + time.Duration(samples%rate)*time.Second/time.Duration(rate)
}

type metadataDSF struct {
	*metadataID3v2
	duration             time.Duration
	sampleRate, channels int
}

func (m metadataDSF) FileType() FileType      { return DSF }
func (m metadataDSF) Duration() time.Duration { return m.duration }
func (m metadataDSF) SampleRate() int         { return m.sampleRate }
func (m metadataDSF) BitRate() int            { return m.sampleRate * m.channels / 1000 }

func writeDSF(path string, opts WriteOptions) error {
	if !hasTagEdits(opts) {
		return ErrUnsupportedWrite
	}
	return atomicRewrite(path, DSF, func(src, dst *os.File, _ int64) error {
		d, err := parseDSF(src)
		if err != nil {
			return err
		}
		id3, err := readID3Block(src, d.audioEnd, d.size-d.audioEnd)
		if err != nil {
			return err
		}
		edited, err := updateID3Block(id3, opts)
		if err != nil {
			return err
		}
		h, err := readAt(src, 0, 28)
		if err != nil {
			return err
		}
		binary.LittleEndian.PutUint64(h[12:20], uint64(d.audioEnd)+uint64(len(edited)))
		binary.LittleEndian.PutUint64(h[20:28], uint64(d.audioEnd))
		if _, err := dst.Write(h); err != nil {
			return err
		}
		if err := copySection(dst, src, 28, d.audioEnd-28); err != nil {
			return err
		}
		_, err = dst.Write(edited)
		return err
	})
}
