// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"errors"
	"io"
	"time"
)

// ReadDSFMeta reads DSF metadata from the io.ReadSeeker, returning the resulting
// metadata in a Metadata implementation, or non-nil error if there was a problem.
// samples: http://www.2l.no/hires/index.html
func ReadDSFMeta(r io.ReadSeeker) (Metadata, error) {
	dsd, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if dsd != "DSD " {
		return nil, errors.New("expected 'DSD '")
	}

	_, err = r.Seek(int64(16), io.SeekCurrent)
	if err != nil {
		return nil, err
	}

	id3Pointer, err := readUint64LittleEndian(r)
	if err != nil {
		return nil, err
	}

	// fmt chunk(DSD chunk 之后从 offset 28 起):
	//   "fmt "(4) + chunk_size(8) + version(4) + format_id(4) + channel_type(4)
	//   + channel_num(4) + sampling_freq(4) + bits_per_sample(4) + sample_count(8) + ...
	// 已读到 pos=28,skip "fmt "(4) + chunk_size(8) + version(4) + format_id(4) + channel_type(4) = 24 字节到 channel_num
	_, err = r.Seek(int64(24), io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	channels, err := readUint32LittleEndian(r)
	if err != nil {
		return nil, err
	}

	sampleRate, err := readUint32LittleEndian(r)
	if err != nil {
		return nil, err
	}

	bitsPerSample, err := readUint32LittleEndian(r)
	if err != nil {
		return nil, err
	}

	sampleNum, err := readUint64LittleEndian(r)
	if err != nil {
		return nil, err
	}

	duration := time.Second * (time.Duration(sampleNum) / time.Duration(sampleRate))

	_, err = r.Seek(int64(id3Pointer), io.SeekStart)
	if err != nil {
		return nil, err
	}

	id3, err := ReadID3v2Tags(r)
	if err != nil {
		return nil, err
	}

	return metadataDSF{
		metadataID3v2: id3,
		duration:      duration,
		sampleRate:    int(sampleRate),
		channels:      int(channels),
		bitsPerSample: int(bitsPerSample),
	}, nil
}

type metadataDSF struct {
	*metadataID3v2
	duration      time.Duration
	sampleRate    int
	channels      int
	bitsPerSample int
}

func (m metadataDSF) FileType() FileType {
	return DSF
}

func (m metadataDSF) Duration() time.Duration {
	return m.duration
}

func (m metadataDSF) SampleRate() int {
	return m.sampleRate
}

// BitRate 返回 DSD 流的原始比特率(kbps)= sampleRate * channels * bitsPerSample / 1000。
func (m metadataDSF) BitRate() int {
	if m.sampleRate == 0 || m.channels == 0 || m.bitsPerSample == 0 {
		return 0
	}
	return m.sampleRate * m.channels * m.bitsPerSample / 1000
}
