// Copyright 2026 songloft contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Matroska / EBML 只读解析（songloft-org/songloft#297）。
//
// Matroska（.mka 音频容器 / .mkv 视频容器）用 EBML(Extensible Binary Meta Language)
// 组织数据：每个 Element = ElementID(VINT，保留长度描述位) + Size(VINT，去掉长度标记位) + Data。
// 本实现手写最小 EBML 遍历，只下潜到需要的元素，遇到 Cluster 等大块音视频数据直接 Seek 跳过，
// 不解析音频帧本身。**仅实现读取，不支持写入**（WriteTag 对 .mka 返回 ErrUnsupportedWrite）。
//
// 读取范围：
//   - Segment > Tags > Tag > SimpleTag(TagName / TagString|TagBinary) → 文本标签
//   - Segment > Attachments > AttachedFile(FileName / FileMimeType / FileData) → 封面
//   - Segment > Info(Duration / TimestampScale) → 时长
//   - Segment > Tracks > TrackEntry > Audio(SamplingFrequency) → 采样率
//
// 参考：https://www.matroska.org/technical/elements.html

// EBML / Matroska Element ID（保留 VINT 长度标记位的完整值）。
const (
	ebmlIDHeader = 0x1A45DFA3 // magic: EBML Header

	mkvIDSegment     = 0x18538067
	mkvIDInfo        = 0x1549A966
	mkvIDTimecodeScl = 0x2AD7B1 // TimestampScale (uint, ns per tick, 默认 1000000)
	mkvIDDuration    = 0x4489   // Duration (float, 单位为 TimestampScale tick)
	mkvIDSegTitle    = 0x7BA9   // Segment Title (UTF-8, 整个文件的标题)

	mkvIDTracks    = 0x1654AE6B
	mkvIDTrackEnt  = 0xAE
	mkvIDTrackType = 0x83 // uint: 1=video 2=audio ...
	mkvIDAudio     = 0xE1
	mkvIDSampFreq  = 0xB5 // SamplingFrequency (float, Hz)

	mkvIDTags      = 0x1254C367
	mkvIDTag       = 0x7373
	mkvIDTargets   = 0x63C0
	mkvIDSimpleTag = 0x67C8
	mkvIDTagName   = 0x45A3 // UTF-8
	mkvIDTagString = 0x4487 // UTF-8
	mkvIDTagBinary = 0x4485 // binary

	mkvIDAttachments  = 0x1941A469
	mkvIDAttachedFile = 0x61A7
	mkvIDFileName     = 0x466E // UTF-8
	mkvIDFileMimeType = 0x4660 // ASCII
	mkvIDFileData     = 0x465C // binary
)

// maxMatroskaBinary 单个 binary element（封面 / TagBinary）允许读取的上限，
// 防御异常大的 Size 触发过量分配。封面通常远小于此值。
const maxMatroskaBinary = 32 << 20 // 32MB

type metadataMatroska struct {
	c              map[string]string // 小写化的 TagName -> 值
	p              *Picture
	duration       time.Duration
	sampleRate     int
	timestampScale uint64 // ns per tick，默认 1000000
	rawDuration    float64
	segmentTitle   string // Segment>Info>Title：整个文件的标题（ffmpeg 把 -metadata title 写这里而非 TITLE 标签）
}

// ReadMatroskaMeta 从 io.ReadSeeker 读取 Matroska 元数据（只读）。
func ReadMatroskaMeta(r io.ReadSeeker) (Metadata, error) {
	m := &metadataMatroska{
		c:              make(map[string]string),
		timestampScale: 1000000,
	}

	fileEnd, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// 顶层遍历：EBML Header + Segment。
	err = m.forEachChild(r, fileEnd, func(id uint64, size int64, unknown bool, dataStart int64) error {
		switch id {
		case ebmlIDHeader:
			// 跳过 EBML 头
			return nil
		case mkvIDSegment:
			segEnd := dataStart + size
			if unknown {
				segEnd = fileEnd
			}
			return m.parseSegment(r, segEnd)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 计算时长：Duration(tick) * TimestampScale(ns) → time.Duration
	if m.rawDuration > 0 && m.timestampScale > 0 {
		ns := m.rawDuration * float64(m.timestampScale)
		m.duration = time.Duration(int64(ns))
	}

	return m, nil
}

// forEachChild 遍历 [当前位置, end) 区间内的同级 Element，对每个调用 fn。
// fn 返回后，无论其是否消费完 Element 数据，都会 Seek 回 dataStart+size 对齐到下一个 Element。
// end 为已知的父容器结束偏移；对未知大小的 Element，fn 需自行下潜消费。
func (m *metadataMatroska) forEachChild(r io.ReadSeeker, end int64, fn func(id uint64, size int64, unknown bool, dataStart int64) error) error {
	for {
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if end >= 0 && pos >= end {
			return nil
		}

		id, size, unknown, err := readEBMLElementHeader(r)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil // 尾部残缺，尽力而为
			}
			return err
		}

		dataStart, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}

		if err := fn(id, size, unknown, dataStart); err != nil {
			return err
		}

		if unknown {
			// 未知大小且 fn 未继续下潜 → 无法安全跳过，停止遍历（尽力而为）。
			newPos, err := r.Seek(0, io.SeekCurrent)
			if err != nil {
				return err
			}
			if newPos <= dataStart {
				return nil
			}
			continue
		}

		if _, err := r.Seek(dataStart+size, io.SeekStart); err != nil {
			return err
		}
	}
}

func (m *metadataMatroska) parseSegment(r io.ReadSeeker, end int64) error {
	return m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown {
			return nil
		}
		childEnd := dataStart + size
		switch id {
		case mkvIDInfo:
			return m.parseInfo(r, childEnd)
		case mkvIDTracks:
			return m.parseTracks(r, childEnd)
		case mkvIDTags:
			return m.parseTags(r, childEnd)
		case mkvIDAttachments:
			return m.parseAttachments(r, childEnd)
		}
		return nil // 其余（SeekHead / Cluster / Cues 等）跳过
	})
}

func (m *metadataMatroska) parseInfo(r io.ReadSeeker, end int64) error {
	return m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown {
			return nil
		}
		switch id {
		case mkvIDTimecodeScl:
			v, err := readEBMLUint(r, size)
			if err != nil {
				return err
			}
			if v > 0 {
				m.timestampScale = v
			}
		case mkvIDDuration:
			f, err := readEBMLFloat(r, size)
			if err != nil {
				return err
			}
			m.rawDuration = f
		case mkvIDSegTitle:
			b, err := readEBMLBytes(r, size)
			if err != nil {
				return err
			}
			m.segmentTitle = string(b)
		}
		return nil
	})
}

func (m *metadataMatroska) parseTracks(r io.ReadSeeker, end int64) error {
	return m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown || id != mkvIDTrackEnt {
			return nil
		}
		return m.parseTrackEntry(r, dataStart+size)
	})
}

func (m *metadataMatroska) parseTrackEntry(r io.ReadSeeker, end int64) error {
	var trackType uint64
	var sampleRate int
	err := m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown {
			return nil
		}
		switch id {
		case mkvIDTrackType:
			v, err := readEBMLUint(r, size)
			if err != nil {
				return err
			}
			trackType = v
		case mkvIDAudio:
			return m.forEachChild(r, dataStart+size, func(aid uint64, asize int64, aunknown bool, _ int64) error {
				if aunknown {
					return nil
				}
				if aid == mkvIDSampFreq {
					f, err := readEBMLFloat(r, asize)
					if err != nil {
						return err
					}
					sampleRate = int(f)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	// 仅采纳首个音频轨的采样率
	if trackType == 2 && sampleRate > 0 && m.sampleRate == 0 {
		m.sampleRate = sampleRate
	}
	return nil
}

func (m *metadataMatroska) parseTags(r io.ReadSeeker, end int64) error {
	return m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown || id != mkvIDTag {
			return nil
		}
		return m.parseTag(r, dataStart+size)
	})
}

func (m *metadataMatroska) parseTag(r io.ReadSeeker, end int64) error {
	return m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown {
			return nil
		}
		switch id {
		case mkvIDSimpleTag:
			return m.parseSimpleTag(r, dataStart+size)
		case mkvIDTargets:
			// Targets(级别信息)当前忽略：所有级别的标签统一收集，最外层优先。
			return nil
		}
		return nil
	})
}

func (m *metadataMatroska) parseSimpleTag(r io.ReadSeeker, end int64) error {
	var name string
	var value string
	var haveValue bool
	err := m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown {
			return nil
		}
		switch id {
		case mkvIDTagName:
			b, err := readEBMLBytes(r, size)
			if err != nil {
				return err
			}
			name = string(b)
		case mkvIDTagString:
			b, err := readEBMLBytes(r, size)
			if err != nil {
				return err
			}
			value = string(b)
			haveValue = true
		case mkvIDTagBinary:
			b, err := readEBMLBytes(r, size)
			if err != nil {
				return err
			}
			if !haveValue {
				value = string(b)
				haveValue = true
			}
		case mkvIDSimpleTag:
			// 嵌套 SimpleTag：递归收集
			return m.parseSimpleTag(r, dataStart+size)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if name != "" && haveValue {
		key := strings.ToLower(strings.TrimSpace(name))
		// 最外层(先出现的)优先，不覆盖已有值
		if _, ok := m.c[key]; !ok {
			m.c[key] = value
		}
	}
	return nil
}

func (m *metadataMatroska) parseAttachments(r io.ReadSeeker, end int64) error {
	return m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown || id != mkvIDAttachedFile {
			return nil
		}
		return m.parseAttachedFile(r, dataStart+size)
	})
}

func (m *metadataMatroska) parseAttachedFile(r io.ReadSeeker, end int64) error {
	var fileName, mime string
	var data []byte
	err := m.forEachChild(r, end, func(id uint64, size int64, unknown bool, dataStart int64) error {
		if unknown {
			return nil
		}
		switch id {
		case mkvIDFileName:
			b, err := readEBMLBytes(r, size)
			if err != nil {
				return err
			}
			fileName = string(b)
		case mkvIDFileMimeType:
			b, err := readEBMLBytes(r, size)
			if err != nil {
				return err
			}
			mime = string(b)
		case mkvIDFileData:
			b, err := readEBMLBytes(r, size)
			if err != nil {
				return err
			}
			data = b
		}
		return nil
	})
	if err != nil {
		return err
	}

	if len(data) == 0 {
		return nil
	}
	// 已有封面则保留第一个
	if m.p != nil {
		return nil
	}

	ext := extFromMatroskaCover(mime, fileName)
	m.p = &Picture{
		Ext:         ext,
		MIMEType:    mime,
		Type:        "Cover (front)",
		Description: fileName,
		Data:        data,
	}
	return nil
}

// extFromMatroskaCover 依据 mime 或文件名推断封面扩展名（不含点）。
func extFromMatroskaCover(mime, fileName string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/bmp":
		return "bmp"
	}
	if i := strings.LastIndexByte(fileName, '.'); i >= 0 && i < len(fileName)-1 {
		return strings.ToLower(fileName[i+1:])
	}
	return "jpg"
}

// ---- EBML 原语 ----

// readEBMLElementHeader 读取一个 Element 头：ElementID(VINT，含标记位) + Size(VINT，去标记位)。
func readEBMLElementHeader(r io.Reader) (id uint64, size int64, unknown bool, err error) {
	id, _, err = readVINT(r, true)
	if err != nil {
		return 0, 0, false, err
	}
	v, length, err := readVINT(r, false)
	if err != nil {
		return 0, 0, false, err
	}
	// 全 1 的 Size 表示未知大小（流式场景）
	maxVal := (uint64(1) << (7 * uint(length))) - 1
	if v == maxVal {
		return id, 0, true, nil
	}
	if v > math.MaxInt64 {
		return 0, 0, false, errors.New("ebml: size overflow")
	}
	return id, int64(v), false, nil
}

// readVINT 读取一个 EBML variable-length integer。
// keepMarker=true 保留长度标记位（用于 ElementID）；false 清除标记位（用于 Size/数值）。
// 返回值、字节长度。
func readVINT(r io.Reader, keepMarker bool) (uint64, int, error) {
	var buf [1]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, 0, err
	}
	first := buf[0]
	if first == 0 {
		return 0, 0, errors.New("ebml: invalid VINT (leading byte 0, length > 8)")
	}

	length := 1
	mask := byte(0x80)
	for mask != 0 {
		if first&mask != 0 {
			break
		}
		mask >>= 1
		length++
	}

	var value uint64
	if keepMarker {
		value = uint64(first)
	} else {
		value = uint64(first &^ mask) // 清除标记位
	}

	if length > 1 {
		rest := make([]byte, length-1)
		if _, err := io.ReadFull(r, rest); err != nil {
			return 0, 0, err
		}
		for _, b := range rest {
			value = (value << 8) | uint64(b)
		}
	}
	return value, length, nil
}

// readEBMLBytes 读取 size 字节数据，带上限保护。
func readEBMLBytes(r io.Reader, size int64) ([]byte, error) {
	if size < 0 {
		return nil, errors.New("ebml: negative size")
	}
	if size > maxMatroskaBinary {
		return nil, errors.New("ebml: element too large")
	}
	return readBytes(r, uint(size))
}

// readEBMLUint 读取大端无符号整数（1..8 字节）。
func readEBMLUint(r io.Reader, size int64) (uint64, error) {
	if size <= 0 || size > 8 {
		return 0, errors.New("ebml: invalid uint size")
	}
	b, err := readBytes(r, uint(size))
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, x := range b {
		v = (v << 8) | uint64(x)
	}
	return v, nil
}

// readEBMLFloat 读取 IEEE 754 大端浮点（0/4/8 字节；0 字节视为 0）。
func readEBMLFloat(r io.Reader, size int64) (float64, error) {
	switch size {
	case 0:
		return 0, nil
	case 4:
		b, err := readBytes(r, 4)
		if err != nil {
			return 0, err
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case 8:
		b, err := readBytes(r, 8)
		if err != nil {
			return 0, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	}
	return 0, errors.New("ebml: invalid float size")
}

// ---- Metadata 接口实现 ----

func (m *metadataMatroska) Format() Format     { return Matroska }
func (m *metadataMatroska) FileType() FileType { return MKA }

func (m *metadataMatroska) Raw() map[string]interface{} {
	raw := make(map[string]interface{}, len(m.c))
	for k, v := range m.c {
		raw[k] = v
	}
	return raw
}

func (m *metadataMatroska) Title() string {
	// TITLE 标签优先；ffmpeg 通常把整体标题写进 Segment>Info>Title，故作兜底。
	if v := m.c["title"]; v != "" {
		return v
	}
	return m.segmentTitle
}

func (m *metadataMatroska) Artist() string {
	if v := m.c["artist"]; v != "" {
		return v
	}
	return m.c["performer"]
}

func (m *metadataMatroska) Album() string { return m.c["album"] }

func (m *metadataMatroska) AlbumArtist() string {
	for _, k := range []string{"album_artist", "albumartist", "album artist"} {
		if v := m.c[k]; v != "" {
			return v
		}
	}
	return ""
}

func (m *metadataMatroska) Composer() string {
	if v := m.c["composer"]; v != "" {
		return v
	}
	return ""
}

func (m *metadataMatroska) Genre() string { return m.c["genre"] }

func (m *metadataMatroska) Language() string {
	if v := m.c["language"]; v != "" {
		return v
	}
	return m.c["lang"]
}

func (m *metadataMatroska) Style() string { return m.c["style"] }

func (m *metadataMatroska) Year() int {
	// Matroska 标准日期标签：DATE_RELEASED / DATE_RECORDED；ffmpeg 常写 DATE / YEAR。
	for _, k := range []string{"date_released", "date_recorded", "date", "year"} {
		if y := parseMatroskaYear(m.c[k]); y > 0 {
			return y
		}
	}
	return 0
}

// parseMatroskaYear 从形如 "2005"、"2005-11-01"、"2005-11-01T00:00:00Z" 提取 4 位年份。
func parseMatroskaYear(s string) int {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return 0
	}
	if y, err := strconv.Atoi(s[:4]); err == nil && y > 0 {
		return y
	}
	return 0
}

func (m *metadataMatroska) Track() (int, int) {
	// Matroska track 目标级标签常见 PART_NUMBER；ffmpeg 亦可写 track/tracknumber。
	for _, k := range []string{"part_number", "tracknumber", "track"} {
		if v := m.c[k]; v != "" {
			return splitMatroskaNumberTotal(v)
		}
	}
	return 0, 0
}

func (m *metadataMatroska) Disc() (int, int) {
	for _, k := range []string{"discnumber", "disc"} {
		if v := m.c[k]; v != "" {
			return splitMatroskaNumberTotal(v)
		}
	}
	return 0, 0
}

// splitMatroskaNumberTotal 解析 "3" 或 "3/12" 形态的编号。
func splitMatroskaNumberTotal(s string) (int, int) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		x, _ := strconv.Atoi(strings.TrimSpace(s[:i]))
		n, _ := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		return x, n
	}
	x, _ := strconv.Atoi(s)
	return x, 0
}

func (m *metadataMatroska) Lyrics() string {
	if v := m.c["lyrics"]; v != "" {
		return v
	}
	return m.c["unsynced_lyrics"]
}

func (m *metadataMatroska) Comment() string {
	if v := m.c["comment"]; v != "" {
		return v
	}
	return m.c["description"]
}

func (m *metadataMatroska) Picture() *Picture { return m.p }

func (m *metadataMatroska) Duration() time.Duration { return m.duration }

// BitRate 返回 0：Matroska 容器不直接给平均比特率，交由上层回退 ffprobe 实测。
func (m *metadataMatroska) BitRate() int { return 0 }

func (m *metadataMatroska) SampleRate() int { return m.sampleRate }
