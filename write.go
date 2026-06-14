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
	Picture     *Picture // Cover art (MIMEType + Data required; Description optional)
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
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedWrite, ext)
	}
}
