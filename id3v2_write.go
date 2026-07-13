// Copyright 2026 songloft contributors.
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
	"strings"
)

// WriteID3v2 writes an ID3v2.3 tag at the start of an MP3 file.
//
// 流程:
//  1. 打开原文件,检测是否存在旧 ID3v2 头;有则定位音频数据起始偏移
//  2. 编码新 frames(TIT2/TPE1/TPE2/TALB/TYER/TCON/USLT/APIC)
//  3. 写到临时文件:[ID3v2 header][frames][padding=0][原音频数据]
//  4. 原子重命名覆盖原文件
//
// 若 opts 各字段全为空,函数等价于"重写 ID3v2 头为空 tag",但仍会消耗一次 I/O。
// 调用方可在写入前自行判断。
func WriteID3v2(filePath string, opts WriteOptions) error {
	src, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open mp3: %w", err)
	}
	defer src.Close()

	audioOffset, err := id3v2AudioOffset(src)
	if err != nil {
		return fmt.Errorf("locate audio offset: %w", err)
	}
	if _, err := src.Seek(audioOffset, io.SeekStart); err != nil {
		return fmt.Errorf("seek audio: %w", err)
	}

	frames, err := buildID3v2Frames(opts)
	if err != nil {
		return fmt.Errorf("build frames: %w", err)
	}

	// 写入临时文件后 rename,保证原子替换
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

	// ID3v2.3 header: "ID3" + version(2) + flags(1) + size(4 sync-safe)
	header := [10]byte{'I', 'D', '3', 0x03, 0x00, 0x00}
	sz := encodeSyncSafe(uint32(len(frames)))
	copy(header[6:], sz[:])
	if _, err := tmp.Write(header[:]); err != nil {
		cleanup()
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := tmp.Write(frames); err != nil {
		cleanup()
		return fmt.Errorf("write frames: %w", err)
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

// id3v2AudioOffset 返回原文件中音频数据的起始偏移。
// 若存在 ID3v2 头则跳过整段(header + frames + padding);否则返回 0。
// 同时识别 ID3v2.4 footer(如果有)。ID3v1 在文件末尾,不影响起始偏移。
func id3v2AudioOffset(r io.ReadSeeker) (int64, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	var head [10]byte
	n, err := io.ReadFull(r, head[:])
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return 0, nil // 空文件或太短,音频从 0 开始
		}
		return 0, err
	}
	if n < 10 || string(head[:3]) != "ID3" {
		return 0, nil // 没有 ID3v2 头
	}
	flags := head[5]
	size := decodeSyncSafe(head[6:10])
	offset := int64(10) + int64(size)
	// 如果有 footer(v2.4 flag bit 4),额外 10 字节
	if head[3] == 0x04 && flags&0x10 != 0 {
		offset += 10
	}
	return offset, nil
}

// buildID3v2Frames 拼接所有 frame 字节(不含 ID3v2 header,但末尾不留 padding)
func buildID3v2Frames(opts WriteOptions) ([]byte, error) {
	var buf bytes.Buffer

	appendText := func(id string, value string) {
		if value == "" {
			return
		}
		writeTextFrame(&buf, id, value)
	}

	appendText("TIT2", opts.Title)
	appendText("TPE1", opts.Artist)
	appendText("TPE2", opts.AlbumArtist)
	appendText("TALB", opts.Album)
	if opts.Year > 0 {
		appendText("TYER", strconv.Itoa(opts.Year))
	}
	appendText("TCON", opts.Genre)
	appendText("TLAN", opts.Language)
	// Style 无标准帧，写成 description="STYLE" 的 TXXX 用户自定义帧
	if opts.Style != "" {
		writeTXXXFrame(&buf, "STYLE", opts.Style)
	}
	// TRCK 直接写 "3" 或 "3/12"（ID3 阅读器按 x/n 解析）
	appendText("TRCK", opts.Track)

	if opts.Lyrics != "" {
		writeUSLTFrame(&buf, opts.Lyrics)
	}
	if opts.Picture != nil && len(opts.Picture.Data) > 0 {
		writeAPICFrame(&buf, opts.Picture)
	}

	return buf.Bytes(), nil
}

// writeTextFrame writes a v2.3 text-information frame using UTF-8 encoding.
// Layout: [4 ID][4 size BE][2 flags][1 encoding=0x03][text UTF-8]
func writeTextFrame(buf *bytes.Buffer, id, value string) {
	payload := make([]byte, 0, 1+len(value))
	payload = append(payload, 0x03) // UTF-8
	payload = append(payload, []byte(value)...)
	writeID3v2Frame(buf, id, payload)
}

// writeTXXXFrame writes a user-defined text information frame (TXXX).
// Layout: [4 ID="TXXX"][4 size BE][2 flags][1 enc=0x03][desc UTF-8][NUL][value UTF-8]
func writeTXXXFrame(buf *bytes.Buffer, desc, value string) {
	var payload bytes.Buffer
	payload.WriteByte(0x03) // UTF-8
	payload.WriteString(desc)
	payload.WriteByte(0x00) // description terminator
	payload.WriteString(value)
	writeID3v2Frame(buf, "TXXX", payload.Bytes())
}

// writeUSLTFrame writes an Unsynchronised Lyrics frame.
// Layout: [4 ID="USLT"][4 size BE][2 flags][1 enc=0x03][3 lang="xxx"][desc NUL][lyrics]
func writeUSLTFrame(buf *bytes.Buffer, lyrics string) {
	var payload bytes.Buffer
	payload.WriteByte(0x03) // UTF-8
	payload.WriteString("xxx")
	payload.WriteByte(0x00) // empty description terminator
	payload.WriteString(lyrics)
	writeID3v2Frame(buf, "USLT", payload.Bytes())
}

// writeAPICFrame writes an Attached Picture frame.
// Layout: [4 ID="APIC"][4 size BE][2 flags][1 enc=0x03][MIME NUL][1 pic type=0x03][desc NUL][data]
func writeAPICFrame(buf *bytes.Buffer, pic *Picture) {
	mime := pic.MIMEType
	if mime == "" {
		mime = "image/jpeg"
	}
	var payload bytes.Buffer
	payload.WriteByte(0x03) // UTF-8
	payload.WriteString(mime)
	payload.WriteByte(0x00) // mime terminator
	payload.WriteByte(0x03) // picture type: Cover (front)
	payload.WriteByte(0x00) // empty description terminator
	payload.Write(pic.Data)
	writeID3v2Frame(buf, "APIC", payload.Bytes())
}

// writeID3v2Frame writes a generic ID3v2.3 frame header + payload.
// v2.3 size is plain big-endian uint32, NOT sync-safe (that's v2.4).
func writeID3v2Frame(buf *bytes.Buffer, id string, payload []byte) {
	if len(id) != 4 {
		// 不可能发生:id 都是常量
		return
	}
	buf.WriteString(id)
	var sizeBuf [4]byte
	binary.BigEndian.PutUint32(sizeBuf[:], uint32(len(payload)))
	buf.Write(sizeBuf[:])
	buf.WriteByte(0x00) // flags1
	buf.WriteByte(0x00) // flags2
	buf.Write(payload)
}

// encodeSyncSafe encodes a uint32 as a 4-byte sync-safe integer
// (each byte uses only the lower 7 bits, MSB = 0).
// ID3v2 header size and v2.4 frame sizes use this format.
func encodeSyncSafe(n uint32) [4]byte {
	return [4]byte{
		byte((n >> 21) & 0x7f),
		byte((n >> 14) & 0x7f),
		byte((n >> 7) & 0x7f),
		byte(n & 0x7f),
	}
}

// decodeSyncSafe is the inverse of encodeSyncSafe (28-bit big-endian).
func decodeSyncSafe(b []byte) uint32 {
	return uint32(b[0]&0x7f)<<21 |
		uint32(b[1]&0x7f)<<14 |
		uint32(b[2]&0x7f)<<7 |
		uint32(b[3]&0x7f)
}

// MIMETypeFromExt 返回常见图片扩展名对应的 MIME 类型,用于在调用方
// 没有显式设置 Picture.MIMEType 时做兜底。
func MIMETypeFromExt(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "bmp":
		return "image/bmp"
	default:
		return "image/jpeg"
	}
}
