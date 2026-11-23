// Copyright 2026 songloft contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsupportedWrite is returned when WriteTag is called for a file format
// that does not yet have a writer implementation.
var ErrUnsupportedWrite = errors.New("tag write not supported for this format")

// WriteOptions describes the metadata fields that should be written to a
// music file. Empty string fields and a zero Year are skipped. A nil Picture
// means no embedded cover is written (an existing one in the file is left
// untouched only when the underlying format supports incremental updates;
// otherwise the picture frame may be cleared — see per-format docs).
type WriteOptions struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Year        int
	Genre       string
	Language    string            // Track language; empty skips the field
	Style       string            // Track style/sub-genre; empty skips the field
	Lyrics      string            // UTF-8 lyrics; embedded as USLT (MP3) / LYRICS or unsynced lyrics (others)
	Track       string            // Track number, "3" or "3/12" (number/total); empty skips the field
	Disc        string            // Disc number, "1" or "1/2" (number/total); empty skips the field
	Picture     *Picture          // Cover art (MIMEType + Data required; Description optional)
	Incremental bool              // Skip legacy rebuild writers that cannot preserve unspecified fields.
	ClearFields []string          // Explicitly remove fields in incremental writers.
	CustomTags  map[string]string // User-defined tags: key→value. Written as TXXX (MP3), Vorbis Comment (FLAC/OGG), freeform atom (M4A), APEv2 item (APE).
}

// splitTrack 拆分音轨号字符串为 (number, total)。
// 支持 "3"（仅轨号）与 "3/12"（轨号/总数）两种形态；两侧空白被裁剪。
// number 为空表示未提供音轨号。
func splitTrack(s string) (number, total string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// WriteTag writes metadata to the container identified from the file header.
// Incremental writers preserve fields omitted in opts. ErrUnsupportedWrite
// indicates that the requested fields or container layout require database
// storage. Advanced writers validate a sibling file before atomic replacement.
func WriteTag(filePath string, opts WriteOptions) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	h := make([]byte, 12)
	_, err = io.ReadFull(f, h)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("%w: incomplete file header", ErrUnsupportedWrite)
	}
	switch {
	case looksLikeMP4(h):
		if opts.Incremental || strings.EqualFold(filepath.Ext(filePath), ".mov") || isQuickTimeFile(filePath) {
			return writeQuickTime(filePath, opts)
		}
		if string(h[4:8]) == "ftyp" {
			return WriteMP4(filePath, withLegacyDisc(filePath, opts))
		}
	case string(h[:4]) == "RIFF" && string(h[8:12]) == "AVI ":
		return writeAVI(filePath, opts)
	case string(h[:4]) == asfHeader[:4]:
		return writeFixedText(filePath, ASF, opts, readASF, FieldTitle, FieldArtist, FieldAlbum, FieldAlbumArtist, FieldYear, FieldGenre, FieldLanguage, FieldStyle, FieldTrack, FieldDisc, FieldLyrics)
	case string(h[:3]) == "FLV":
		return writeFixedText(filePath, FLV, opts, readFLV, FieldTitle, FieldArtist, FieldAlbum, FieldAlbumArtist, FieldYear, FieldGenre, FieldLanguage, FieldStyle, FieldTrack, FieldDisc, FieldLyrics)
	case string(h[:4]) == ".RMF":
		return writeFixedText(filePath, RM, opts, readRealMedia, FieldTitle, FieldArtist)

	case string(h[:4]) == "DSD ":
		return writeDSF(filePath, opts)
	case string(h[:4]) == "wvpk":
		return writeWavPack(filePath, opts)
	case string(h[:4]) == "RIFF" && string(h[8:12]) == "WAVE":
		return WriteWAV(filePath, opts)
	case isDTS(h), string(h[:4]) == "\x00\x00\x01\xba":
		return ErrUnsupportedWrite
	case isADTSHeader(h):
		return writeADTS(filePath, opts)
	case string(h[:3]) == "ID3":
		switch id3PayloadSignature(filePath, h) {
		case "aac":
			return writeADTS(filePath, opts)
		case "flac":
			return WriteFLAC(filePath, withLegacyDisc(filePath, opts))
		}
	case string(h[:4]) == "\x1a\x45\xdf\xa3":
		return writeMatroska(filePath, opts)
	case string(h[:4]) == "FRM8":
		return writeDSDIFF(filePath, opts)
	}

	if opts.Incremental {
		switch {
		case string(h[:4]) == "fLaC":
			return WriteFLAC(filePath, opts)
		case string(h[:3]) == "ID3" || h[0] == 0xff && h[1]&0xe0 == 0xe0:
			return writeMP3Incremental(filePath, opts)
		default:
			return ErrUnsupportedWrite
		}
	}
	opts = withLegacyDisc(filePath, opts)
	switch {
	case string(h[:3]) == "ID3" || h[0] == 0xff && h[1]&0xe0 == 0xe0:
		return WriteID3v2(filePath, opts)
	case string(h[:4]) == "fLaC":
		return WriteFLAC(filePath, opts)
	case string(h[:4]) == "MAC ":
		return WriteAPE(filePath, opts)
	case string(h[:4]) == "OggS":
		return WriteOGG(filePath, opts)
	case string(h[:4]) == "FORM" && (string(h[8:12]) == "AIFF" || string(h[8:12]) == "AIFC"):
		return WriteAIFF(filePath, opts)
	default:
		return ErrUnsupportedWrite
	}
}

// Older databases and rebuild callers omit disc; preserve the value in the file.
func withLegacyDisc(path string, opts WriteOptions) WriteOptions {
	if opts.Disc != "" {
		return opts
	}
	f, err := os.Open(path)
	if err != nil {
		return opts
	}
	defer f.Close()
	m, err := ReadFrom(f)
	if err != nil {
		return opts
	}
	number, total := m.Disc()
	if number > 0 {
		opts.Disc = fmt.Sprint(number)
		if total > 0 {
			opts.Disc += "/" + fmt.Sprint(total)
		}
	}
	return opts
}

func id3PayloadSignature(path string, head []byte) string {
	offset, err := id3BlockSize(head)
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := readAt(f, int64(offset), 7)
	if err != nil {
		return ""
	}
	if isADTSHeader(b) {
		return "aac"
	}
	if string(b[:4]) == "fLaC" {
		return "flac"
	}
	return ""
}
