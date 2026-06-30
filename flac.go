// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// blockType is a type which represents an enumeration of valid FLAC blocks
type blockType byte

// FLAC block types.
const (
	// Padding Block               1
	// Application Block           2
	// Seektable Block             3
	// Cue Sheet Block             5
	streamInfoBlock    blockType = 0
	vorbisCommentBlock blockType = 4
	pictureBlock       blockType = 6
)

// ReadFLACMeta reads FLAC metadata from the io.ReadSeeker, returning the resulting
// metadata in a Metadata implementation, or non-nil error if there was a problem.
func ReadFLACMeta(r io.ReadSeeker) (Metadata, error) {
	flac, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if flac != "fLaC" {
		return nil, errors.New("expected 'fLaC'")
	}

	m := &metadataFLAC{
		metadataVorbis: newMetadataVorbis(),
	}

	for {
		last, err := m.readFLACBlock(r)
		if err != nil {
			return nil, err
		}

		if last {
			break
		}
	}
	return m, nil
}

type metadataFLAC struct {
	*metadataVorbis
	duration       time.Duration
	sampleRate     int // Hz
	channels       int
	bitsPerSample  int
	cueSheetBlock  *CUESheetData
}

const cueSheetBlockType blockType = 5

func (m *metadataFLAC) readFLACBlock(r io.ReadSeeker) (last bool, err error) {
	blockHeader, err := readBytes(r, 1)
	if err != nil {
		return
	}

	if getBit(blockHeader[0], 7) {
		blockHeader[0] ^= (1 << 7)
		last = true
	}

	blockLen, err := readInt(r, 3)
	if err != nil {
		return
	}

	switch blockType(blockHeader[0]) {
	case vorbisCommentBlock:
		err = m.readVorbisComment(r)

	case pictureBlock:
		err = m.readPictureBlock(r)

	case streamInfoBlock:
		err = m.readStreamingInfoBlock(r, blockLen)

	case cueSheetBlockType:
		cueData := make([]byte, blockLen)
		if _, err = io.ReadFull(r, cueData); err != nil {
			return
		}
		m.cueSheetBlock = parseFLACCueSheetBlockRaw(cueData)

	default:
		_, err = r.Seek(int64(blockLen), io.SeekCurrent)
	}
	return
}

func (m *metadataFLAC) readStreamingInfoBlock(r io.Reader, len int) error {
	data := make([]byte, len)
	if _, err := r.Read(data); err != nil {
		return err
	}

	sampleRate, err := cutBits(data, 80, 20)
	if err != nil {
		return fmt.Errorf("reading sample rate: %w", err)
	}
	// streaminfo: sample_rate(20) + channels(3, value+1) + bits_per_sample(5, value+1) + sample_num(36)
	channelsRaw, err := cutBits(data, 100, 3)
	if err != nil {
		return fmt.Errorf("reading channels: %w", err)
	}
	bitsRaw, err := cutBits(data, 103, 5)
	if err != nil {
		return fmt.Errorf("reading bits per sample: %w", err)
	}

	sampleNum, err := cutBits(data, 108, 36)
	if err != nil {
		return fmt.Errorf("reading sample number: %w", err)
	}

	m.sampleRate = int(sampleRate)
	m.channels = int(channelsRaw) + 1
	m.bitsPerSample = int(bitsRaw) + 1
	m.duration = time.Second * (time.Duration(sampleNum) / time.Duration(sampleRate))

	return nil
}

func (m *metadataFLAC) FileType() FileType {
	return FLAC
}

func (m *metadataFLAC) Duration() time.Duration {
	return m.duration
}

func (m *metadataFLAC) SampleRate() int {
	return m.sampleRate
}

func (m *metadataFLAC) CUESheetBlock() *CUESheetData {
	return m.cueSheetBlock
}

// parseFLACCueSheetBlockRaw 解析 FLAC CUESHEET metadata block 的二进制数据。
// 参考 FLAC specification: https://xiph.org/flac/format.html#metadata_block_cuesheet
func parseFLACCueSheetBlockRaw(data []byte) *CUESheetData {
	// 最小长度：128 (catalog) + 8 (lead-in) + 1 (flags) + 258 (reserved) + 1 (num_tracks) = 396
	if len(data) < 396 {
		return nil
	}

	numTracks := int(data[395])
	if numTracks == 0 {
		return nil
	}

	result := &CUESheetData{}
	pos := 396

	for range numTracks {
		if pos+36 > len(data) {
			break
		}

		offsetSample := binary.BigEndian.Uint64(data[pos : pos+8])
		trackNumber := int(data[pos+8])
		isrc := strings.TrimRight(string(data[pos+9:pos+21]), "\x00")
		isAudio := (data[pos+21] & 0x80) == 0
		numIndexPoints := int(data[pos+35])
		pos += 36

		var indexPoints []CUESheetIndex
		for range numIndexPoints {
			if pos+12 > len(data) {
				break
			}
			idxOffset := binary.BigEndian.Uint64(data[pos : pos+8])
			idxNumber := int(data[pos+8])
			indexPoints = append(indexPoints, CUESheetIndex{
				OffsetSample: idxOffset,
				Number:       idxNumber,
			})
			pos += 12
		}

		// track 170 (0xAA) 是 lead-out
		if trackNumber == 170 || !isAudio {
			continue
		}

		result.Tracks = append(result.Tracks, CUESheetTrack{
			Number:       trackNumber,
			OffsetSample: offsetSample,
			ISRC:         isrc,
			IndexPoints:  indexPoints,
		})
	}

	if len(result.Tracks) == 0 {
		return nil
	}
	return result
}

// BitRate 返回 FLAC 的平均 bitrate(kbps)。streaminfo 不直接给 bitrate,
// 这里返回 0,让上层(ProbeForValidation)回退到 ffprobe 用 file_size/duration 算实测平均值。
// 用 sampleRate*bitsPerSample*channels 得到的是未压缩 PCM 比特率(典型 1411 kbps),不能表示 FLAC 压缩后的实际比特率。
func (m *metadataFLAC) BitRate() int {
	return 0
}
