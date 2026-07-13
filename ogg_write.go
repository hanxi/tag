// Copyright 2026 songloft contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteOGG writes Vorbis Comment metadata to an Ogg Vorbis/Opus file.
//
// 流程:
//  1. 读取所有 OGG 页，按 serial 重组 packet
//  2. 识别编解码器(Vorbis/Opus)，定位 comment packet
//  3. 用 opts 构建新 comment packet（复用 buildFLACVorbisComment）
//  4. 重新分页 header packets，逐页复制音频数据（更新 sequence + CRC）
//  5. 原子写入临时文件后 rename
func WriteOGG(filePath string, opts WriteOptions) error {
	src, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open ogg: %w", err)
	}
	defer src.Close()

	pages, err := readAllOGGPages(src)
	if err != nil {
		return fmt.Errorf("read ogg pages: %w", err)
	}
	if len(pages) < 2 {
		return fmt.Errorf("ogg file too short: only %d pages", len(pages))
	}

	serialNumber := pages[0].header.SerialNumber

	// 重组 packets 并找出每个 packet 跨哪些页
	packets, pageRanges, err := reassemblePackets(pages, serialNumber)
	if err != nil {
		return fmt.Errorf("reassemble packets: %w", err)
	}
	if len(packets) < 2 {
		return fmt.Errorf("not enough packets: need at least 2, got %d", len(packets))
	}

	// 识别编解码器
	isOpus := bytes.HasPrefix(packets[0], []byte("OpusHead"))
	isVorbis := bytes.HasPrefix(packets[0], vorbisIdentificationPrefix)
	if !isOpus && !isVorbis {
		return fmt.Errorf("unrecognized codec: first packet prefix %x", packets[0][:min(8, len(packets[0]))])
	}

	// 构建新 comment packet
	newComment := buildOGGCommentPacket(opts, isOpus)

	// 确定 header packet 数量: Vorbis=3(id+comment+setup), Opus=2(head+tags)
	headerCount := 2
	if isVorbis {
		headerCount = 3
	}
	if len(packets) < headerCount {
		return fmt.Errorf("not enough header packets: need %d, got %d", headerCount, len(packets))
	}

	// 最后一个 header packet 结束在哪一页
	lastHeaderPage := pageRanges[headerCount-1].endPage

	// 重建 header packets: 替换 comment(index=1)
	headerPackets := make([][]byte, headerCount)
	headerPackets[0] = packets[0]
	headerPackets[1] = newComment
	for i := 2; i < headerCount; i++ {
		headerPackets[i] = packets[i]
	}

	// 分页 header packets
	newHeaderPages := pageHeaderPackets(headerPackets, serialNumber)
	seqDelta := len(newHeaderPages) - (lastHeaderPage + 1)

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

	// 写新 header pages
	for _, p := range newHeaderPages {
		if err := writeRawOGGPage(tmp, p); err != nil {
			cleanup()
			return fmt.Errorf("write header page: %w", err)
		}
	}

	// 逐页复制剩余音频 pages，更新 sequence number 和 CRC
	for i := lastHeaderPage + 1; i < len(pages); i++ {
		p := pages[i]
		if p.header.SerialNumber != serialNumber {
			// 多路复用流，原样写入
			if err := writeRawOGGPage(tmp, p.raw); err != nil {
				cleanup()
				return fmt.Errorf("write other stream page: %w", err)
			}
			continue
		}

		newSeq := int(p.header.SequenceNumber) + seqDelta
		adjusted := adjustOGGPageSeq(p.raw, uint32(newSeq))
		if err := writeRawOGGPage(tmp, adjusted); err != nil {
			cleanup()
			return fmt.Errorf("write audio page: %w", err)
		}
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := src.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close src: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

type oggPage struct {
	header       oggPageHeader
	segmentTable []byte
	segmentData  []byte
	raw          []byte
}

type packetPageRange struct {
	startPage int
	endPage   int
}

func readAllOGGPages(r io.Reader) ([]oggPage, error) {
	var pages []oggPage
	for {
		page, err := readOneOGGPage(r)
		if err != nil {
			if err == io.EOF {
				return pages, nil
			}
			return pages, err
		}
		pages = append(pages, page)
	}
}

func readOneOGGPage(r io.Reader) (oggPage, error) {
	var hdr oggPageHeader
	headerBuf := &bytes.Buffer{}
	if err := binary.Read(io.TeeReader(r, headerBuf), binary.LittleEndian, &hdr); err != nil {
		return oggPage{}, err
	}
	if !bytes.Equal(hdr.Magic[:], []byte("OggS")) {
		return oggPage{}, fmt.Errorf("expected OggS, got %q", string(hdr.Magic[:]))
	}

	segTable := make([]byte, hdr.Segments)
	if _, err := io.ReadFull(r, segTable); err != nil {
		return oggPage{}, err
	}
	var totalSize int
	for _, s := range segTable {
		totalSize += int(s)
	}
	segData := make([]byte, totalSize)
	if _, err := io.ReadFull(r, segData); err != nil {
		return oggPage{}, err
	}

	raw := make([]byte, 0, headerBuf.Len()+len(segTable)+len(segData))
	raw = append(raw, headerBuf.Bytes()...)
	raw = append(raw, segTable...)
	raw = append(raw, segData...)

	return oggPage{header: hdr, segmentTable: segTable, segmentData: segData, raw: raw}, nil
}

func reassemblePackets(pages []oggPage, serial uint32) ([][]byte, []packetPageRange, error) {
	var packets [][]byte
	var ranges []packetPageRange
	var packetBuf bytes.Buffer
	startPage := 0

	for pageIdx, page := range pages {
		if page.header.SerialNumber != serial {
			continue
		}

		if packetBuf.Len() == 0 {
			startPage = pageIdx
		}

		pos := 0
		for _, s := range page.segmentTable {
			packetBuf.Write(page.segmentData[pos : pos+int(s)])
			if s < 255 {
				packets = append(packets, bytes.Clone(packetBuf.Bytes()))
				ranges = append(ranges, packetPageRange{startPage: startPage, endPage: pageIdx})
				packetBuf.Reset()
				startPage = pageIdx
			}
			pos += int(s)
		}
	}

	// 未结束的包（文件末尾）
	if packetBuf.Len() > 0 {
		packets = append(packets, packetBuf.Bytes())
		ranges = append(ranges, packetPageRange{startPage: startPage, endPage: len(pages) - 1})
	}

	return packets, ranges, nil
}

func buildOGGCommentPacket(opts WriteOptions, isOpus bool) []byte {
	vc := buildOGGVorbisComment(opts)
	var buf bytes.Buffer
	if isOpus {
		buf.Write([]byte("OpusTags"))
	} else {
		buf.Write(vorbisCommentPrefix) // \x03vorbis
	}
	buf.Write(vc)
	return buf.Bytes()
}

// buildOGGVorbisComment 构建 Vorbis Comment 二进制体（与 FLAC 共用格式），
// 支持 METADATA_BLOCK_PICTURE。
func buildOGGVorbisComment(opts WriteOptions) []byte {
	// 先用基础的 buildFLACVorbisComment 但需要加上 picture
	if opts.Picture == nil || len(opts.Picture.Data) == 0 {
		return buildFLACVorbisComment(opts)
	}

	// 有 picture：手动构建，加入 METADATA_BLOCK_PICTURE comment
	picBlock := buildFLACPicture(opts.Picture)
	picB64 := base64.StdEncoding.EncodeToString(picBlock)

	// 构建带 picture 的 vorbis comment
	var buf bytes.Buffer
	vendor := "songloft"
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(vendor)))
	buf.Write(lenBuf[:])
	buf.WriteString(vendor)

	comments := collectVorbisComments(opts)
	comments = append(comments, "METADATA_BLOCK_PICTURE="+picB64)

	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(comments)))
	buf.Write(lenBuf[:])
	for _, c := range comments {
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(c)))
		buf.Write(lenBuf[:])
		buf.WriteString(c)
	}
	return buf.Bytes()
}

func collectVorbisComments(opts WriteOptions) []string {
	var comments []string
	if opts.Title != "" {
		comments = append(comments, "TITLE="+opts.Title)
	}
	if opts.Artist != "" {
		comments = append(comments, "ARTIST="+opts.Artist)
	}
	if opts.AlbumArtist != "" {
		comments = append(comments, "ALBUMARTIST="+opts.AlbumArtist)
	}
	if opts.Album != "" {
		comments = append(comments, "ALBUM="+opts.Album)
	}
	if opts.Year > 0 {
		comments = append(comments, "DATE="+fmt.Sprintf("%d", opts.Year))
	}
	if opts.Genre != "" {
		comments = append(comments, "GENRE="+opts.Genre)
	}
	if opts.Language != "" {
		comments = append(comments, "LANGUAGE="+opts.Language)
	}
	if opts.Style != "" {
		comments = append(comments, "STYLE="+opts.Style)
	}
	if opts.Lyrics != "" {
		comments = append(comments, "LYRICS="+opts.Lyrics)
	}
	if trackNum, trackTotal := splitTrack(opts.Track); trackNum != "" {
		comments = append(comments, "TRACKNUMBER="+trackNum)
		if trackTotal != "" {
			comments = append(comments, "TRACKTOTAL="+trackTotal)
		}
	}
	return comments
}

// pageHeaderPackets 将 header packets 分页。
// Page 0: BOS，只含第一个 packet
// Page 1+: comment + setup(Vorbis) 或只有 comment(Opus)
func pageHeaderPackets(packets [][]byte, serial uint32) [][]byte {
	var result [][]byte

	// Page 0: BOS, 只含 identification/head packet
	result = append(result, buildOGGPage(serial, 0, 0, 0x02, [][]byte{packets[0]}))

	// 剩余 header packets (comment + optional setup)
	remaining := packets[1:]
	seq := uint32(1)

	// 所有剩余 header packets 放到同一个 page 序列
	// 如果数据太大会自动拆成多页
	allData := concatPackets(remaining)
	segTable := buildSegmentTable(remaining)

	// 拆分成多页（每页最多 255 个 segment）
	dataPos := 0
	segPos := 0
	prevLastSeg := byte(0) // 上一页最后一个 segment 值；255 表示包未结束
	for segPos < len(segTable) {
		pageSegs := min(255, len(segTable)-segPos)
		thisSegTable := segTable[segPos : segPos+pageSegs]

		var thisDataSize int
		for _, s := range thisSegTable {
			thisDataSize += int(s)
		}

		flags := byte(0)
		if prevLastSeg == 255 {
			flags |= 0x01 // continued packet
		}

		page := serializeOGGPage(serial, seq, 0, flags, thisSegTable, allData[dataPos:dataPos+thisDataSize])
		result = append(result, page)

		prevLastSeg = thisSegTable[len(thisSegTable)-1]
		dataPos += thisDataSize
		segPos += pageSegs
		seq++
	}

	return result
}

func concatPackets(packets [][]byte) []byte {
	var buf bytes.Buffer
	for _, p := range packets {
		buf.Write(p)
	}
	return buf.Bytes()
}

func buildSegmentTable(packets [][]byte) []byte {
	var table []byte
	for _, p := range packets {
		remaining := len(p)
		for remaining >= 255 {
			table = append(table, 255)
			remaining -= 255
		}
		table = append(table, byte(remaining))
	}
	return table
}

func buildOGGPage(serial, seq uint32, granule uint64, flags byte, packets [][]byte) []byte {
	segTable := buildSegmentTable(packets)
	data := concatPackets(packets)
	return serializeOGGPage(serial, seq, granule, flags, segTable, data)
}

func serializeOGGPage(serial, seq uint32, granule uint64, flags byte, segTable, data []byte) []byte {
	headerSize := 27 + len(segTable)
	page := make([]byte, headerSize+len(data))

	copy(page[0:4], "OggS")
	page[4] = 0 // version
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], serial)
	binary.LittleEndian.PutUint32(page[18:22], seq)
	// page[22:26] = CRC (computed below)
	page[26] = byte(len(segTable))
	copy(page[27:], segTable)
	copy(page[headerSize:], data)

	// CRC
	crc := oggCRCUpdate(0, oggCRC32Poly04c11db7, page)
	binary.LittleEndian.PutUint32(page[22:26], crc)

	return page
}

func adjustOGGPageSeq(raw []byte, newSeq uint32) []byte {
	adjusted := make([]byte, len(raw))
	copy(adjusted, raw)
	// sequence number at offset 18
	binary.LittleEndian.PutUint32(adjusted[18:22], newSeq)
	// clear CRC and recompute
	adjusted[22] = 0
	adjusted[23] = 0
	adjusted[24] = 0
	adjusted[25] = 0
	crc := oggCRCUpdate(0, oggCRC32Poly04c11db7, adjusted)
	binary.LittleEndian.PutUint32(adjusted[22:26], crc)
	return adjusted
}

func writeRawOGGPage(w io.Writer, data []byte) error {
	_, err := w.Write(data)
	return err
}
