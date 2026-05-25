// Copyright 2026 mimusic contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// FLAC 块类型常量(METADATA_BLOCK_HEADER 中 7 位的 block type)
const (
	flacBlockStreamInfo    = 0
	flacBlockPadding       = 1
	flacBlockApplication   = 2
	flacBlockSeekTable     = 3
	flacBlockVorbisComment = 4
	flacBlockCueSheet      = 5
	flacBlockPicture       = 6
)

// WriteFLAC 写入 FLAC 文件的 Vorbis Comment + Picture 元数据。
//
// 流程:
//  1. 校验 "fLaC" 魔数
//  2. 解析现有元数据块,保留 STREAMINFO + SEEKTABLE + CUESHEET + APPLICATION;
//     丢弃旧的 VORBIS_COMMENT / PICTURE / PADDING
//  3. 用 opts 构造新的 VORBIS_COMMENT 块和 PICTURE 块
//  4. 重新写文件:[fLaC][保留块][新 vc][新 picture][audio frames]
func WriteFLAC(filePath string, opts WriteOptions) error {
	src, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open flac: %w", err)
	}
	defer src.Close()

	var magic [4]byte
	if _, err := io.ReadFull(src, magic[:]); err != nil {
		return fmt.Errorf("read magic: %w", err)
	}
	if string(magic[:]) != "fLaC" {
		return fmt.Errorf("not a FLAC file (magic=%q)", string(magic[:]))
	}

	type savedBlock struct {
		blockType byte
		data      []byte
	}
	var preserved []savedBlock
	var audioOffset int64

	for {
		var head [4]byte
		if _, err := io.ReadFull(src, head[:]); err != nil {
			return fmt.Errorf("read block header: %w", err)
		}
		isLast := head[0]&0x80 != 0
		blockType := head[0] & 0x7f
		length := uint32(head[1])<<16 | uint32(head[2])<<8 | uint32(head[3])
		data := make([]byte, length)
		if _, err := io.ReadFull(src, data); err != nil {
			return fmt.Errorf("read block body: %w", err)
		}

		switch blockType {
		case flacBlockStreamInfo, flacBlockSeekTable, flacBlockApplication, flacBlockCueSheet:
			preserved = append(preserved, savedBlock{blockType, data})
		case flacBlockVorbisComment, flacBlockPicture, flacBlockPadding:
			// 旧的 VC/Picture/Padding 丢弃,后面用新的
		default:
			// 未知块保留(保守起见)
			preserved = append(preserved, savedBlock{blockType, data})
		}

		if isLast {
			audioOffset, err = src.Seek(0, io.SeekCurrent)
			if err != nil {
				return fmt.Errorf("locate audio offset: %w", err)
			}
			break
		}
	}

	// 构造新的元数据块
	newVC := buildFLACVorbisComment(opts)
	var newPic []byte
	if opts.Picture != nil && len(opts.Picture.Data) > 0 {
		newPic = buildFLACPicture(opts.Picture)
	}

	// 决定块顺序与 isLast 标志
	// STREAMINFO 必须是第一个块;最后一个块的 last-flag 必须为 1
	type outBlock struct {
		blockType byte
		data      []byte
	}
	var out []outBlock
	for _, b := range preserved {
		out = append(out, outBlock{b.blockType, b.data})
	}
	out = append(out, outBlock{flacBlockVorbisComment, newVC})
	if newPic != nil {
		out = append(out, outBlock{flacBlockPicture, newPic})
	}

	// 准备临时文件
	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".mimusic-tag-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write([]byte("fLaC")); err != nil {
		cleanup()
		return fmt.Errorf("write magic: %w", err)
	}

	for i, b := range out {
		head := [4]byte{b.blockType, 0, 0, 0}
		if i == len(out)-1 {
			head[0] |= 0x80
		}
		length := uint32(len(b.data))
		head[1] = byte(length >> 16)
		head[2] = byte(length >> 8)
		head[3] = byte(length)
		if _, err := tmp.Write(head[:]); err != nil {
			cleanup()
			return fmt.Errorf("write block header: %w", err)
		}
		if _, err := tmp.Write(b.data); err != nil {
			cleanup()
			return fmt.Errorf("write block body: %w", err)
		}
	}

	if _, err := src.Seek(audioOffset, io.SeekStart); err != nil {
		cleanup()
		return fmt.Errorf("seek audio: %w", err)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		cleanup()
		return fmt.Errorf("copy audio: %w", err)
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

// buildFLACVorbisComment 编码 VORBIS_COMMENT 块体。
//
// 布局(所有整数使用 LITTLE-ENDIAN,与 ID3v2 的 BE 相反):
//
//	[u32 vendor_len][vendor bytes]
//	[u32 num_comments]
//	repeated num_comments 次: [u32 comment_len][comment="KEY=value"]
//
// 与 RFC 推荐字段名保持一致(全大写)。
func buildFLACVorbisComment(opts WriteOptions) []byte {
	var buf bytes.Buffer
	vendor := "mimusic"
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(vendor)))
	buf.Write(lenBuf[:])
	buf.WriteString(vendor)

	comments := make([]string, 0, 8)
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
		comments = append(comments, "DATE="+strconv.Itoa(opts.Year))
	}
	if opts.Genre != "" {
		comments = append(comments, "GENRE="+opts.Genre)
	}
	if opts.Lyrics != "" {
		comments = append(comments, "LYRICS="+opts.Lyrics)
	}

	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(comments)))
	buf.Write(lenBuf[:])
	for _, c := range comments {
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(c)))
		buf.Write(lenBuf[:])
		buf.WriteString(c)
	}
	return buf.Bytes()
}

// buildFLACPicture 编码 PICTURE 块体(METADATA_BLOCK_PICTURE,大端)。
//
// 布局:
//
//	[u32 picture_type=3 (front cover)]
//	[u32 mime_len][mime bytes]
//	[u32 desc_len][desc bytes (UTF-8)]
//	[u32 width=0][u32 height=0][u32 depth=0][u32 colors_used=0]
//	[u32 data_len][raw image bytes]
func buildFLACPicture(pic *Picture) []byte {
	mime := pic.MIMEType
	if mime == "" {
		mime = "image/jpeg"
	}
	desc := pic.Description

	var buf bytes.Buffer
	var u32 [4]byte
	put := func(v uint32) {
		binary.BigEndian.PutUint32(u32[:], v)
		buf.Write(u32[:])
	}

	put(3) // Front Cover
	put(uint32(len(mime)))
	buf.WriteString(mime)
	put(uint32(len(desc)))
	buf.WriteString(desc)
	put(0) // width
	put(0) // height
	put(0) // depth
	put(0) // colors_used
	put(uint32(len(pic.Data)))
	buf.Write(pic.Data)
	return buf.Bytes()
}
