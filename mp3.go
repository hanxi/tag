package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"
)

type mpegVersion int

const (
	mpeg25 mpegVersion = iota
	mpegReserved
	mpeg2
	mpeg1
	mpegMax
)

type mpegLayer int

const (
	layerReserved mpegLayer = iota
	layer3
	layer2
	layer1
	layerMax
)

// Took from: https://github.com/tcolgate/mp3/blob/master/frames.go
var (
	bitrates = [mpegMax][layerMax][15]int{
		{ // MPEG 2.5
			{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},                       // LayerReserved
			{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},      // Layer3
			{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},      // Layer2
			{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256}, // Layer1
		},
		{ // Reserved
			{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // LayerReserved
			{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // Layer3
			{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // Layer2
			{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // Layer1
		},
		{ // MPEG 2
			{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},                       // LayerReserved
			{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},      // Layer3
			{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},      // Layer2
			{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256}, // Layer1
		},
		{ // MPEG 1
			{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},                          // LayerReserved
			{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},     // Layer3
			{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},    // Layer2
			{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448}, // Layer1
		},
	}
	sampleRates = [int(mpegMax)][3]int{
		{11025, 12000, 8000},  //MPEG25
		{0, 0, 0},             //MPEGReserved
		{22050, 24000, 16000}, //MPEG2
		{44100, 48000, 32000}, //MPEG1
	}

	samplesPerFrame = [mpegMax][layerMax]int{
		{ // MPEG25
			0,
			576,
			1152,
			384,
		},
		{ // Reserved
			0,
			0,
			0,
			0,
		},
		{ // MPEG2
			0,
			576,
			1152,
			384,
		},
		{ // MPEG1
			0,
			1152,
			1152,
			384,
		},
	}
	slotSize = [layerMax]int{
		0, //	LayerReserved
		1, //	Layer3
		1, //	Layer2
		4, //	Layer1
	}
)

type metadataV2MP3 struct {
	*metadataID3v2
	duration time.Duration
}

type metadataV1MP3 struct {
	*metadataID3v1
	duration time.Duration
}

func getMP3Duration(header []byte, strippedSize int64) (time.Duration, error) {
	// 检查 header 长度
	if len(header) < 4 {
		return 0, fmt.Errorf("header too short: need 4 bytes, got %d", len(header))
	}

	// 验证 MP3 帧同步字（前 11 bits 应全为 1）
	if header[0] != 0xFF || (header[1]&0xE0) != 0xE0 {
		return 0, fmt.Errorf("invalid mp3 frame sync word: got 0x%02X%02X", header[0], header[1])
	}

	version, err := cutBits(header, 11, 2)
	if err != nil {
		return 0, fmt.Errorf("reading mpeg version: %w", err)
	}
	// version == 1 是 reserved
	if mpegVersion(version) == mpegReserved {
		return 0, fmt.Errorf("invalid mpeg version: reserved value 1")
	}
	if version >= uint64(mpegMax) {
		return 0, fmt.Errorf("invalid mpeg version index: %d", version)
	}

	layer, err := cutBits(header, 13, 2)
	if err != nil {
		return 0, fmt.Errorf("reading mpeg layer: %w", err)
	}
	// layer == 0 是 reserved
	if mpegLayer(layer) == layerReserved {
		return 0, fmt.Errorf("invalid mpeg layer: reserved value 0")
	}
	if layer >= uint64(layerMax) {
		return 0, fmt.Errorf("invalid mpeg layer index: %d", layer)
	}

	protection, err := cutBits(header, 15, 1)
	if err != nil {
		return 0, fmt.Errorf("reading mpeg protection: %w", err)
	}

	bitrateIndex, err := cutBits(header, 16, 4)
	if err != nil {
		return 0, fmt.Errorf("reading mpeg bitrate index: %w", err)
	}
	// bitrateIndex == 0 (free) 或 15 (bad) 应返回错误
	if bitrateIndex == 0 {
		return 0, fmt.Errorf("invalid bitrate index: free bitrate not supported")
	}
	if bitrateIndex == 15 {
		return 0, fmt.Errorf("invalid bitrate index: bad value 15")
	}

	samplerateIndex, err := cutBits(header, 20, 2)
	if err != nil {
		return 0, fmt.Errorf("reading mpeg samplerate index: %w", err)
	}
	// samplerateIndex == 3 是 reserved
	if samplerateIndex == 3 {
		return 0, fmt.Errorf("invalid samplerate index: reserved value 3")
	}

	padding, err := cutBits(header, 21, 1)
	if err != nil {
		return 0, fmt.Errorf("reading mpeg padding bit: %w", err)
	}

	// 访问查找表前验证索引范围
	sampleRate := sampleRates[version][samplerateIndex]
	if sampleRate == 0 {
		return 0, fmt.Errorf("invalid sample rate: got 0 for version=%d samplerate_index=%d", version, samplerateIndex)
	}

	bitrate := bitrates[version][layer][bitrateIndex]
	if bitrate == 0 {
		return 0, fmt.Errorf("invalid bitrate: got 0 for version=%d layer=%d bitrate_index=%d", version, layer, bitrateIndex)
	}

	frameSampleNum := samplesPerFrame[version][layer]
	if frameSampleNum == 0 {
		return 0, fmt.Errorf("invalid samples per frame: got 0 for version=%d layer=%d", version, layer)
	}

	frameDuration := float64(frameSampleNum) / float64(sampleRate)
	frameSize := math.Floor(((frameDuration * float64(bitrate)) * 1000) / 8)
	if padding == 1 {
		frameSize += float64(slotSize[layer])
	}
	if protection == 1 {
		frameSize += 2
	}
	// add the header length
	frameSize += 4

	// 防止除以零
	if frameSize == 0 {
		return 0, fmt.Errorf("invalid frame size: calculated as 0")
	}

	duration := time.Second * time.Duration(math.Round((float64(strippedSize)/float64(frameSize))*frameDuration))

	return duration, nil
}

// parseVBRHeader 尝试解析 Xing/LAME VBR 头，如果找到则返回总帧数和总字节数
func parseVBRHeader(r io.ReadSeeker, headerOffset int64, mpegVersion mpegVersion, mpegLayer mpegLayer, protection uint64) (totalFrames uint32, totalBytes uint32, hasVBR bool, err error) {
	// 计算 VBR 头的偏移位置
	// Xing 头在帧同步字和边信息之后
	offset := headerOffset + 4 // 跳过 4 字节帧头

	// 跳过边信息 (Side Information)
	// MPEG1: 17 bytes (mono) or 32 bytes (stereo)
	// MPEG2/2.5: 9 bytes (mono) or 17 bytes (stereo)
	// 这里我们简单处理，读取更多数据来查找 Xing/Lame 标记
	skipBytes := int64(0)
	if mpegVersion == mpeg1 {
		skipBytes = 32 // 假设 stereo
	} else {
		skipBytes = 17 // 假设 stereo
	}
	offset += skipBytes

	// 如果有 CRC，跳过 2 字节
	if protection == 0 {
		offset += 2
	}

	// 读取 Xing/Lame 标记（在帧头后的固定位置）
	// Xing 头通常在偏移 4+(skipBytes) 或 4+(skipBytes)+2(CRC) 的位置
	// 我们读取 4 字节来检查是否有 "Xing" 或 "Info" 标记
	_, err = r.Seek(offset, io.SeekStart)
	if err != nil {
		return 0, 0, false, fmt.Errorf("seeking to VBR header: %w", err)
	}

	marker := make([]byte, 4)
	_, err = io.ReadFull(r, marker)
	if err != nil {
		// 读取失败，可能没有 VBR 头
		return 0, 0, false, nil
	}

	// 检查是否是 Xing 或 Info 标记
	if !bytes.Equal(marker, []byte("Xing")) && !bytes.Equal(marker, []byte("Info")) {
		return 0, 0, false, nil
	}

	// 读取 flags（4 字节）
	flagsBytes := make([]byte, 4)
	_, err = io.ReadFull(r, flagsBytes)
	if err != nil {
		return 0, 0, false, fmt.Errorf("reading VBR flags: %w", err)
	}
	flags := binary.BigEndian.Uint32(flagsBytes)

	// Flag 0 (bit 0) = Frames 字段存在
	// Flag 1 (bit 1) = Bytes 字段存在
	hasFrames := (flags & 0x0001) != 0
	hasBytes := (flags & 0x0002) != 0

	if hasFrames {
		framesBytes := make([]byte, 4)
		_, err = io.ReadFull(r, framesBytes)
		if err != nil {
			return 0, 0, false, fmt.Errorf("reading VBR frames count: %w", err)
		}
		totalFrames = binary.BigEndian.Uint32(framesBytes)
	}

	if hasBytes {
		bytesField := make([]byte, 4)
		_, err = io.ReadFull(r, bytesField)
		if err != nil {
			return 0, 0, false, fmt.Errorf("reading VBR bytes count: %w", err)
		}
		totalBytes = binary.BigEndian.Uint32(bytesField)
	}

	return totalFrames, totalBytes, true, nil
}

func ReadV2MP3Meta(r io.ReadSeeker, size int64) (Metadata, error) {
	tagMeta, err := ReadID3v2Tags(r)
	if err != nil {
		return nil, fmt.Errorf("reading id3v2 tags: %w", err)
	}

	id3Size := tagMeta.header.Size + 10
	if tagMeta.header.FooterPresent {
		id3Size += 10
	}
	_, err = r.Seek(int64(id3Size), io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("seeking to skip id3v2: %w", err)

	}

	header := make([]byte, 4)
	_, err = io.ReadFull(r, header)
	if err != nil {
		return nil, fmt.Errorf("reading first frame header: %w", err)
	}

	// FLAC 文件可以在 fLaC 头之前包含 ID3v2 标签，
	// 跳过 ID3v2 后如果遇到 fLaC 魔数，则转交给 FLAC 解析器处理。
	if string(header) == "fLaC" {
		_, err = r.Seek(int64(id3Size), io.SeekStart)
		if err != nil {
			return nil, fmt.Errorf("seeking back for FLAC parsing: %w", err)
		}
		return ReadFLACMeta(r)
	}

	// 验证 MP3 帧同步字（前 11 bits 应全为 1）
	if header[0] != 0xFF || (header[1]&0xE0) != 0xE0 {
		return nil, fmt.Errorf("invalid mp3 frame sync word at offset %d", id3Size)
	}

	// 解析 MP3 帧头信息
	version, _ := cutBits(header, 11, 2)
	layer, _ := cutBits(header, 13, 2)
	protection, _ := cutBits(header, 15, 1)
	samplerateIndex, _ := cutBits(header, 20, 2)

	sampleRate := sampleRates[version][samplerateIndex]
	frameSampleNum := samplesPerFrame[version][layer]

	// 尝试解析 VBR 头
	vbrOffset := int64(id3Size)
	totalFrames, _, hasVBR, err := parseVBRHeader(r, vbrOffset, mpegVersion(version), mpegLayer(layer), protection)
	if err != nil {
		// VBR 头解析失败，回退到 CBR 计算
		fmt.Printf("Warning: failed to parse VBR header: %v, falling back to CBR calculation\n", err)
	}

	var duration time.Duration
	if hasVBR && totalFrames > 0 && sampleRate > 0 && frameSampleNum > 0 {
		// 使用 VBR 信息计算时长
		frameDuration := float64(frameSampleNum) / float64(sampleRate)
		duration = time.Second * time.Duration(math.Round(float64(totalFrames)*frameDuration))
	} else {
		// 使用 CBR 计算方法
		duration, err = getMP3Duration(header, size-int64(id3Size))
		if err != nil {
			return nil, fmt.Errorf("reading the mp3 duration: %w", err)
		}
	}

	return &metadataV2MP3{
		metadataID3v2: tagMeta,
		duration:      duration,
	}, nil

}

func ReadV1MP3Meta(r io.ReadSeeker, size int64) (Metadata, error) {
	tagMeta, err := ReadID3v1Tags(r)
	if err != nil {
		return nil, fmt.Errorf("reading id3v1 tags: %w", err)
	}

	_, err = r.Seek(0, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("seeking to the start: %w", err)

	}

	header := make([]byte, 4)
	_, err = io.ReadFull(r, header)
	if err != nil {
		return nil, fmt.Errorf("reading first frame header: %w", err)
	}

	// 解析 MP3 帧头信息
	version, _ := cutBits(header, 11, 2)
	layer, _ := cutBits(header, 13, 2)
	protection, _ := cutBits(header, 15, 1)
	samplerateIndex, _ := cutBits(header, 20, 2)

	sampleRate := sampleRates[version][samplerateIndex]
	frameSampleNum := samplesPerFrame[version][layer]

	// 尝试解析 VBR 头
	vbrOffset := int64(0)
	totalFrames, _, hasVBR, err := parseVBRHeader(r, vbrOffset, mpegVersion(version), mpegLayer(layer), protection)
	if err != nil {
		// VBR 头解析失败，回退到 CBR 计算
		fmt.Printf("Warning: failed to parse VBR header: %v, falling back to CBR calculation\n", err)
	}

	var duration time.Duration
	if hasVBR && totalFrames > 0 && sampleRate > 0 && frameSampleNum > 0 {
		// 使用 VBR 信息计算时长
		frameDuration := float64(frameSampleNum) / float64(sampleRate)
		duration = time.Second * time.Duration(math.Round(float64(totalFrames)*frameDuration))
	} else {
		// 使用 CBR 计算方法
		duration, err = getMP3Duration(header, size-128)
		if err != nil {
			return nil, fmt.Errorf("reading the mp3 duration: %w", err)
		}
	}

	return &metadataV1MP3{
		metadataID3v1: &tagMeta,
		duration:      duration,
	}, nil

}

func (m *metadataV2MP3) Duration() time.Duration {
	return m.duration
}

func (m *metadataV1MP3) Duration() time.Duration {
	return m.duration
}
