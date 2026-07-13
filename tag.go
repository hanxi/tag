// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package tag provides MP3 (ID3: v1, 2.2, 2.3 and 2.4), MP4, FLAC and OGG metadata detection,
// parsing and artwork extraction.
//
// Detect and parse tag metadata from an io.ReadSeeker (i.e. an *os.File):
//
//	m, err := tag.ReadFrom(f)
//	if err != nil {
//		log.Fatal(err)
//	}
//	log.Print(m.Format()) // The detected format.
//	log.Print(m.Title())  // The title of the track (see Metadata interface for more details).
package tag

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrNoTagsFound is the error returned by ReadFrom when the metadata format
// cannot be identified.
var ErrNoTagsFound = errors.New("no tags found")

// ReadFrom detects and parses audio file metadata tags (currently supports ID3v1,2.{2,3,4}, MP4, FLAC/OGG).
// Returns non-nil error if the format of the given data could not be determined, or if there was a problem
// parsing the data.
func ReadFrom(r io.ReadSeeker) (Metadata, error) {
	b, err := readBytes(r, 11)
	if err != nil {
		return nil, err
	}

	_, err = r.Seek(-11, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("could not seek back to original position: %v", err)
	}

	switch {
	case string(b[0:4]) == "fLaC":
		return ReadFLACMeta(r)

	case string(b[0:4]) == "OggS":
		return ReadOGGMeta(r)

	case string(b[4:8]) == "ftyp":
		return ReadAtoms(r)

	case string(b[0:3]) == "ID3":
		size, err := getFileSize(r)
		if err != nil {
			return nil, fmt.Errorf("could not get file size: %w", err)
		}
		return ReadV2MP3Meta(r, size)

	case b[0] == 0xff && (b[1]&0xe0) == 0xe0:
		size, err := getFileSize(r)
		if err != nil {
			return nil, fmt.Errorf("could not get file size: %w", err)
		}
		return ReadV1MP3Meta(r, size)

	case string(b[0:4]) == "MAC ":
		return ReadAPEMeta(r)

	case string(b[0:4]) == "DSD ":
		return ReadDSFMeta(r)

	case string(b[0:4]) == "RIFF":
		return ReadWAVMeta(r)

	case string(b[0:4]) == "FORM":
		return ReadAIFFMeta(r)
	}

	return nil, errors.ErrUnsupported
}

func getFileSize(r io.ReadSeeker) (int64, error) {
	current, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fmt.Errorf("could not get current pos: %w", err)
	}

	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("could not seek to end pos: %w", err)
	}

	_, err = r.Seek(current, io.SeekStart)
	if err != nil {
		return 0, fmt.Errorf("could not reset current pos: %w", err)
	}

	return size, nil

}

// Format is an enumeration of metadata types supported by this package.
type Format string

// Supported tag formats.
const (
	UnknownFormat Format = ""        // Unknown Format.
	ID3v1         Format = "ID3v1"   // ID3v1 tag format.
	ID3v2_2       Format = "ID3v2.2" // ID3v2.2 tag format.
	ID3v2_3       Format = "ID3v2.3" // ID3v2.3 tag format (most common).
	ID3v2_4       Format = "ID3v2.4" // ID3v2.4 tag format.
	MP4           Format = "MP4"     // MP4 tag (atom) format (see http://www.ftyps.com/ for a full file type list)
	VORBIS        Format = "VORBIS"  // Vorbis Comment tag format.
	APEv2         Format = "APEv2"   // APEv2 tag format (Monkey's Audio / WavPack / Musepack)
)

// FileType is an enumeration of the audio file types supported by this package, in particular
// there are audio file types which share metadata formats, and this type is used to distinguish
// between them.
type FileType string

// Supported file types.
const (
	UnknownFileType FileType = ""     // Unknown FileType.
	MP3             FileType = "MP3"  // MP3 file
	M4A             FileType = "M4A"  // M4A file Apple iTunes (ACC) Audio
	M4B             FileType = "M4B"  // M4A file Apple iTunes (ACC) Audio Book
	M4P             FileType = "M4P"  // M4A file Apple iTunes (ACC) AES Protected Audio
	ALAC            FileType = "ALAC" // Apple Lossless file FIXME: actually detect this
	FLAC            FileType = "FLAC" // FLAC file
	OGG             FileType = "OGG"  // OGG file
	DSF             FileType = "DSF"  // DSF file DSD Sony format see https://dsd-guide.com/sites/default/files/white-papers/DSFFileFormatSpec_E.pdf
	WAV             FileType = "WAV"  // WAVE file
	AIFF            FileType = "AIFF" // AIFF/AIFF-C file
	APE             FileType = "APE"  // Monkey's Audio file
)

// Metadata is an interface which is used to describe metadata retrieved by this package.
type Metadata interface {
	// Format returns the metadata Format used to encode the data.
	Format() Format

	// FileType returns the file type of the audio file.
	FileType() FileType

	// Title returns the title of the track.
	Title() string

	// Album returns the album name of the track.
	Album() string

	// Artist returns the artist name of the track.
	Artist() string

	// AlbumArtist returns the album artist name of the track.
	AlbumArtist() string

	// Composer returns the composer of the track.
	Composer() string

	// Year returns the year of the track.
	Year() int

	// Genre returns the genre of the track.
	Genre() string

	// Language returns the language of the track, or empty string if unavailable.
	Language() string

	// Style returns the style/sub-genre of the track, or empty string if unavailable.
	Style() string

	// Track returns the track number and total tracks, or zero values if unavailable.
	Track() (int, int)

	// Disc returns the disc number and total discs, or zero values if unavailable.
	Disc() (int, int)

	// Picture returns a picture, or nil if not available.
	Picture() *Picture

	// Lyrics returns the lyrics, or an empty string if unavailable.
	Lyrics() string

	// Comment returns the comment, or an empty string if unavailable.
	Comment() string

	// Raw returns the raw mapping of retrieved tag names and associated values.
	// NB: tag/atom names are not standardised between formats.
	Raw() map[string]interface{}

	Duration() time.Duration

	// BitRate returns the average bitrate in kbps, or 0 if not available
	// from the container (e.g. ID3v1, generic MP4 without stsd parsing).
	BitRate() int

	// SampleRate returns the audio sampling rate in Hz, or 0 if not available.
	SampleRate() int
}

// CUESheetTrack 表示 FLAC CUESHEET block 中的一个 track
type CUESheetTrack struct {
	Number       int
	OffsetSample uint64
	ISRC         string
	IndexPoints  []CUESheetIndex
}

// CUESheetIndex 表示 track 中的一个 index point
type CUESheetIndex struct {
	OffsetSample uint64
	Number       int
}

// CUESheetData 表示 FLAC 内嵌 CUESHEET block 的解析结果
type CUESheetData struct {
	Tracks []CUESheetTrack
}

// CUESheetProvider 是一个可选接口，支持返回 FLAC 内嵌 CUESHEET。
// 目前仅 FLAC 实现，其他格式不实现此接口。
// 使用方式：if p, ok := metadata.(tag.CUESheetProvider); ok { data := p.CUESheetBlock() }
type CUESheetProvider interface {
	CUESheetBlock() *CUESheetData
}
