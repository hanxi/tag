// Copyright 2026 songloft contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"errors"
	"fmt"
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
	Lyrics      string   // UTF-8 lyrics; embedded as USLT (MP3) / LYRICS or unsynced lyrics (others)
	Track       string   // Track number, "3" or "3/12" (number/total); empty skips the field
	Picture     *Picture // Cover art (MIMEType + Data required; Description optional)
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

// WriteTag writes the supplied metadata into the music file at filePath.
//
// The format is selected by file extension. Supported formats and their tag mappings:
//
//	Format          | Text fields              | Lyrics      | Picture
//	.mp3            | ID3v2.3 text frames      | USLT        | APIC
//	.flac           | Vorbis Comment           | LYRICS      | PICTURE block
//	.m4a/.mp4/.m4b  | iTunes atoms (©nam etc)  | ©lyr        | covr
//	.ogg/.oga       | Vorbis Comment           | LYRICS      | METADATA_BLOCK_PICTURE
//	.ape            | APEv2 items              | Lyrics      | Cover Art (Front) (binary)
//	.wav            | RIFF LIST INFO           | ICMT        | (not supported)
//
// Returns ErrUnsupportedWrite for other extensions. The original file is
// rewritten atomically (write to a sibling temp file then rename).
func WriteTag(filePath string, opts WriteOptions) error {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp3":
		return WriteID3v2(filePath, opts)
	case ".flac":
		return WriteFLAC(filePath, opts)
	case ".ape":
		return WriteAPE(filePath, opts)
	case ".wav":
		return WriteWAV(filePath, opts)
	case ".m4a", ".mp4", ".m4b":
		return WriteMP4(filePath, opts)
	case ".ogg", ".oga":
		return WriteOGG(filePath, opts)
	case ".aif", ".aiff":
		return WriteAIFF(filePath, opts)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedWrite, ext)
	}
}
