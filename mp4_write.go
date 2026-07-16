// Copyright 2026 songloft contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// WriteMP4 writes iTunes-style metadata atoms into an M4A/MP4 file.
//
// 流程:
//  1. 扫描顶层 atom 列表,定位 moov
//  2. 解析 moov 内部,找到 udta > meta > ilst(不存在则创建)
//  3. 用 opts 构建新 ilst
//  4. 替换 moov 中的 ilst 区域,级联更新父容器 size
//  5. 如果 moov 在 mdat 之前,更新 stco/co64 偏移
//  6. 原子写入临时文件后 rename
func WriteMP4(filePath string, opts WriteOptions) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read mp4: %w", err)
	}

	topAtoms, err := scanAtoms(data, 0, len(data))
	if err != nil {
		return fmt.Errorf("scan top atoms: %w", err)
	}

	moovAtom := findAtom(topAtoms, "moov")
	if moovAtom == nil {
		return fmt.Errorf("moov atom not found")
	}

	mdatAtom := findAtom(topAtoms, "mdat")
	moovBeforeMdat := mdatAtom != nil && moovAtom.offset < mdatAtom.offset

	oldMoovSize := moovAtom.size

	newIlst := buildMP4Ilst(opts)
	newMoovBody, err := rebuildMoov(data[moovAtom.offset+8:moovAtom.offset+moovAtom.size], newIlst)
	if err != nil {
		return fmt.Errorf("rebuild moov: %w", err)
	}

	newMoov := buildAtom("moov", newMoovBody)
	delta := len(newMoov) - oldMoovSize

	if moovBeforeMdat && delta != 0 {
		adjustSTCO(newMoov[8:], int32(delta))
	}

	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".songloft-tag-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	// [before moov] [new moov] [after moov]
	if _, err := tmp.Write(data[:moovAtom.offset]); err != nil {
		cleanup()
		return fmt.Errorf("write before moov: %w", err)
	}
	if _, err := tmp.Write(newMoov); err != nil {
		cleanup()
		return fmt.Errorf("write moov: %w", err)
	}
	if _, err := tmp.Write(data[moovAtom.offset+moovAtom.size:]); err != nil {
		cleanup()
		return fmt.Errorf("write after moov: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

type mp4Atom struct {
	name   string
	offset int
	size   int
}

func scanAtoms(data []byte, start, end int) ([]mp4Atom, error) {
	var atoms []mp4Atom
	pos := start
	for pos+8 <= end {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		name := string(data[pos+4 : pos+8])

		if size == 0 {
			size = end - pos
		} else if size == 1 {
			if pos+16 > end {
				break
			}
			size = int(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
		}

		if size < 8 || pos+size > end {
			break
		}

		atoms = append(atoms, mp4Atom{name: name, offset: pos, size: size})
		pos += size
	}
	return atoms, nil
}

func findAtom(atoms []mp4Atom, name string) *mp4Atom {
	for i := range atoms {
		if atoms[i].name == name {
			return &atoms[i]
		}
	}
	return nil
}

// rebuildMoov 替换 moov body 中的 ilst 并级联更新 parent size。
// 如果 udta > meta > ilst 路径不存在则创建。
func rebuildMoov(moovBody []byte, newIlst []byte) ([]byte, error) {
	udtaIdx, udtaOff, udtaSize := findChildAtom(moovBody, "udta")
	if udtaIdx < 0 {
		// 没有 udta：在 moov body 末尾追加 udta > meta > ilst
		metaBody := buildMP4MetaHdlr()
		metaBody = append(metaBody, newIlst...)
		meta := buildAtomWithMeta("meta", metaBody)
		udta := buildAtom("udta", meta)
		return append(moovBody, udta...), nil
	}

	udtaBody := moovBody[udtaOff+8 : udtaOff+udtaSize]
	newUdtaBody, err := rebuildUdta(udtaBody, newIlst)
	if err != nil {
		return nil, err
	}
	newUdta := buildAtom("udta", newUdtaBody)

	var result []byte
	result = append(result, moovBody[:udtaOff]...)
	result = append(result, newUdta...)
	result = append(result, moovBody[udtaOff+udtaSize:]...)
	return result, nil
}

func rebuildUdta(udtaBody []byte, newIlst []byte) ([]byte, error) {
	_, metaOff, metaSize := findChildAtom(udtaBody, "meta")
	if metaOff < 0 {
		metaBody := buildMP4MetaHdlr()
		metaBody = append(metaBody, newIlst...)
		meta := buildAtomWithMeta("meta", metaBody)
		return append(udtaBody, meta...), nil
	}

	// meta atom 的头部长度：标准 MP4/iTunes 是 FullBox（8 字节 header + 4 字节
	// version+flags，子 atom 从 +12 起）；老式纯 QuickTime 的 meta 不带 version+flags，
	// 子 atom 紧跟 header 从 +8 起（bilibili 等下载源可能产出此布局，旧代码写死 +12
	// 会错位 4 字节导致解析出错乱的 ilst、写标签失败）。按实际布局定位既有子 atom，
	// 再统一写回标准 FullBox 布局（buildAtomWithMeta），使产出文件对读侧/播放器最大兼容。
	hdrLen := mp4MetaHeaderLen(udtaBody, metaOff, metaSize)
	metaBody := udtaBody[metaOff+hdrLen : metaOff+metaSize]
	newMetaBody, err := rebuildMeta(metaBody, newIlst)
	if err != nil {
		return nil, err
	}
	newMeta := buildAtomWithMeta("meta", newMetaBody)

	var result []byte
	result = append(result, udtaBody[:metaOff]...)
	result = append(result, newMeta...)
	result = append(result, udtaBody[metaOff+metaSize:]...)
	return result, nil
}

// mp4MetaHeaderLen 判定 meta atom 的头部长度（相对 metaOff），返回 8 或 12。
// meta 的首个子 atom 规范上恒为 hdlr：FullBox 布局下其名出现在 metaOff+16，
// bare QuickTime 布局下出现在 metaOff+12。据此消歧；仅在明确命中 bare 位置且
// 未命中 full 位置时判为 bare(8)，否则一律回退标准 full(12)，保证零回归。
func mp4MetaHeaderLen(body []byte, metaOff, metaSize int) int {
	end := metaOff + metaSize
	nameEq := func(pos int, want string) bool {
		if pos < 0 || pos+4 > end || pos+4 > len(body) {
			return false
		}
		return string(body[pos:pos+4]) == want
	}
	bareHdlr := nameEq(metaOff+12, "hdlr")
	fullHdlr := nameEq(metaOff+16, "hdlr")
	if bareHdlr && !fullHdlr {
		return 8
	}
	return 12
}

func rebuildMeta(metaBody []byte, newIlst []byte) ([]byte, error) {
	_, ilstOff, ilstSize := findChildAtom(metaBody, "ilst")
	if ilstOff < 0 {
		// 没有 ilst，在 meta body 末尾追加（跳过 free atom）
		return append(metaBody, newIlst...), nil
	}

	// 替换 ilst，丢弃紧随其后的 free atom（如果有）
	afterIlst := ilstOff + ilstSize
	if afterIlst+8 <= len(metaBody) {
		freeName := string(metaBody[afterIlst+4 : afterIlst+8])
		if freeName == "free" {
			freeSize := int(binary.BigEndian.Uint32(metaBody[afterIlst : afterIlst+4]))
			if freeSize >= 8 && afterIlst+freeSize <= len(metaBody) {
				afterIlst += freeSize
			}
		}
	}

	var result []byte
	result = append(result, metaBody[:ilstOff]...)
	result = append(result, newIlst...)
	result = append(result, metaBody[afterIlst:]...)
	return result, nil
}

// findChildAtom 在容器体内查找指定名称的子 atom。
// 返回 (index, offset, size)，未找到返回 (-1, -1, -1)。
func findChildAtom(body []byte, name string) (int, int, int) {
	pos := 0
	idx := 0
	for pos+8 <= len(body) {
		sz := int(binary.BigEndian.Uint32(body[pos : pos+4]))
		n := string(body[pos+4 : pos+8])
		if sz < 8 || pos+sz > len(body) {
			break
		}
		if n == name {
			return idx, pos, sz
		}
		pos += sz
		idx++
	}
	return -1, -1, -1
}

// buildAtom 构建 [size][name][body] 格式的 atom
func buildAtom(name string, body []byte) []byte {
	size := 8 + len(body)
	buf := make([]byte, size)
	binary.BigEndian.PutUint32(buf[0:4], uint32(size))
	copy(buf[4:8], name)
	copy(buf[8:], body)
	return buf
}

// buildAtomWithMeta 构建含 4 字节 version+flags 的 full atom（如 meta）
func buildAtomWithMeta(name string, body []byte) []byte {
	size := 12 + len(body)
	buf := make([]byte, size)
	binary.BigEndian.PutUint32(buf[0:4], uint32(size))
	copy(buf[4:8], name)
	// buf[8:12] = 0x00000000 (version + flags)
	copy(buf[12:], body)
	return buf
}

// buildMP4MetaHdlr 构建 meta 必需的 hdlr atom (mdirappl)
func buildMP4MetaHdlr() []byte {
	var buf bytes.Buffer
	hdlrBody := make([]byte, 25)
	// version+flags = 0
	copy(hdlrBody[4:8], "mdir")
	copy(hdlrBody[8:12], "appl")
	// name = 0 (null terminator, 1 byte minimum)
	hdlr := buildAtom("hdlr", hdlrBody)
	buf.Write(hdlr)
	return buf.Bytes()
}

// buildMP4Ilst 从 WriteOptions 构建 ilst atom
func buildMP4Ilst(opts WriteOptions) []byte {
	var body bytes.Buffer

	writeText := func(atomName, value string) {
		if value == "" {
			return
		}
		body.Write(buildMP4TextAtom(atomName, value))
	}

	writeText("\xa9nam", opts.Title)
	writeText("\xa9ART", opts.Artist)
	writeText("aART", opts.AlbumArtist)
	writeText("\xa9alb", opts.Album)
	if opts.Year > 0 {
		writeText("\xa9day", strconv.Itoa(opts.Year))
	}
	writeText("\xa9gen", opts.Genre)
	if opts.Language != "" {
		body.Write(buildMP4FreeformAtom("LANGUAGE", opts.Language))
	}
	if opts.Style != "" {
		body.Write(buildMP4FreeformAtom("STYLE", opts.Style))
	}
	writeText("\xa9lyr", opts.Lyrics)

	if trkn := buildMP4TrackAtom(opts.Track); trkn != nil {
		body.Write(trkn)
	}

	if opts.Picture != nil && len(opts.Picture.Data) > 0 {
		body.Write(buildMP4PictureAtom(opts.Picture))
	}

	return buildAtom("ilst", body.Bytes())
}

// buildMP4TextAtom 构建文本元数据 atom: [size][name][data_atom]
// data_atom = [size]["data"][0x00000001(text class)][0x00000000(locale)][UTF-8 text]
func buildMP4TextAtom(name, value string) []byte {
	dataPayload := make([]byte, 8+len(value))
	binary.BigEndian.PutUint32(dataPayload[0:4], 1) // class = text
	// dataPayload[4:8] = 0 (locale)
	copy(dataPayload[8:], value)
	dataAtom := buildAtom("data", dataPayload)
	return buildAtom(name, dataAtom)
}

// buildMP4FreeformAtom 构建 iTunes 风格的 freeform ("----") atom，用于无标准
// atom 的字段（如 LANGUAGE / STYLE）。
//
// 布局:
//
//	"----" atom
//	  "mean" atom: [size]["mean"][4 version+flags=0]["com.apple.iTunes"]
//	  "name" atom: [size]["name"][4 version+flags=0][name]
//	  "data" atom: [size]["data"][4 class=1(text)][4 locale=0][value]
func buildMP4FreeformAtom(name, value string) []byte {
	meanBody := make([]byte, 4+len("com.apple.iTunes"))
	copy(meanBody[4:], "com.apple.iTunes")
	mean := buildAtom("mean", meanBody)

	nameBody := make([]byte, 4+len(name))
	copy(nameBody[4:], name)
	nameAtom := buildAtom("name", nameBody)

	dataPayload := make([]byte, 8+len(value))
	binary.BigEndian.PutUint32(dataPayload[0:4], 1) // class = text
	// dataPayload[4:8] = 0 (locale)
	copy(dataPayload[8:], value)
	dataAtom := buildAtom("data", dataPayload)

	var body bytes.Buffer
	body.Write(mean)
	body.Write(nameAtom)
	body.Write(dataAtom)
	return buildAtom("----", body.Bytes())
}

// buildMP4TrackAtom 构建 trkn atom（二进制 data atom，class=0/implicit）。
// body 布局：[2 reserved][2 track number][2 total tracks][2 reserved]，大端。
// opts.Track 为空或无有效轨号时返回 nil（不写 trkn）。
func buildMP4TrackAtom(track string) []byte {
	numStr, totalStr := splitTrack(track)
	num, _ := strconv.Atoi(numStr)
	if num <= 0 {
		return nil
	}
	total, _ := strconv.Atoi(totalStr)

	trkn := make([]byte, 8)
	binary.BigEndian.PutUint16(trkn[2:4], uint16(num))
	binary.BigEndian.PutUint16(trkn[4:6], uint16(total))

	dataPayload := make([]byte, 8+len(trkn))
	// class = 0 (implicit/binary), locale = 0
	copy(dataPayload[8:], trkn)
	dataAtom := buildAtom("data", dataPayload)
	return buildAtom("trkn", dataAtom)
}

// buildMP4PictureAtom 构建 covr atom
func buildMP4PictureAtom(pic *Picture) []byte {
	class := 13 // JPEG
	if bytes.HasPrefix(pic.Data, pngHeader) {
		class = 14 // PNG
	}

	dataPayload := make([]byte, 8+len(pic.Data))
	binary.BigEndian.PutUint32(dataPayload[0:4], uint32(class))
	// dataPayload[4:8] = 0 (locale)
	copy(dataPayload[8:], pic.Data)
	dataAtom := buildAtom("data", dataPayload)
	return buildAtom("covr", dataAtom)
}

// adjustSTCO 遍历 moov body 中所有 stco/co64 atom，将 chunk offset 加 delta。
// 递归进入容器 atom (trak/mdia/minf/stbl)。
func adjustSTCO(moovBody []byte, delta int32) {
	adjustSTCORecursive(moovBody, delta)
}

func adjustSTCORecursive(data []byte, delta int32) {
	pos := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		name := string(data[pos+4 : pos+8])
		if size < 8 || pos+size > len(data) {
			break
		}

		switch name {
		case "trak", "mdia", "minf", "stbl":
			adjustSTCORecursive(data[pos+8:pos+size], delta)
		case "stco":
			adjustSTCOAtom(data[pos+8:pos+size], int64(delta))
		case "co64":
			adjustCO64Atom(data[pos+8:pos+size], int64(delta))
		}
		pos += size
	}
}

func adjustSTCOAtom(body []byte, delta int64) {
	if len(body) < 8 {
		return
	}
	// body = [4 version+flags][4 entry_count][entries * 4 bytes]
	count := int(binary.BigEndian.Uint32(body[4:8]))
	for i := 0; i < count; i++ {
		off := 8 + i*4
		if off+4 > len(body) {
			break
		}
		old := int64(binary.BigEndian.Uint32(body[off : off+4]))
		binary.BigEndian.PutUint32(body[off:off+4], uint32(old+delta))
	}
}

func adjustCO64Atom(body []byte, delta int64) {
	if len(body) < 8 {
		return
	}
	count := int(binary.BigEndian.Uint32(body[4:8]))
	for i := 0; i < count; i++ {
		off := 8 + i*8
		if off+8 > len(body) {
			break
		}
		old := int64(binary.BigEndian.Uint64(body[off : off+8]))
		binary.BigEndian.PutUint64(body[off:off+8], uint64(old+delta))
	}
}
