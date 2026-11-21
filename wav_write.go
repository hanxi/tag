package tag

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
)

var riffInfoFields = []struct{ id, field string }{
	{"INAM", FieldTitle}, {"IART", FieldArtist}, {"IPRD", FieldAlbum}, {"ICRD", FieldYear},
	{"IGNR", FieldGenre}, {"ITRK", FieldTrack}, {"ICMT", FieldLyrics},
}

func riffTextOptions(opts WriteOptions) map[string]string {
	values := []string{opts.Title, opts.Artist, opts.Album, "", opts.Genre, opts.Track, opts.Lyrics}
	if opts.Year > 0 {
		values[3] = strconv.Itoa(opts.Year)
	}
	changed := make(map[string]string)
	for i, f := range riffInfoFields {
		if values[i] != "" || slices.Contains(opts.ClearFields, f.field) {
			changed[f.id] = values[i]
		}
	}
	return changed
}

func editRIFFInfo(r io.ReadSeeker, c fileChunk, changes map[string]string) ([]byte, error) {
	body := []byte("INFO")
	if c.id != "" {
		err := walkChunks(r, c.data+4, c.data+c.size, false, func(child fileChunk) error {
			if _, edited := changes[child.id]; edited {
				return nil
			}
			if child.end-child.start > int64(maxMetadataSize-len(body)) {
				return unsupportedLayout("RIFF INFO size")
			}
			b, err := readAt(r, child.start, int(child.end-child.start))
			if err == nil {
				body = append(body, b...)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	for _, field := range riffInfoFields {
		if s := changes[field.id]; s != "" {
			body = appendChunk(body, field.id, append([]byte(s), 0), false)
		}
	}
	return body, nil
}

// WriteWAV edits RIFF INFO fields and uses a sibling ID3 chunk for artwork and
// fields without a RIFF INFO mapping. Untouched chunks are copied verbatim.
func WriteWAV(path string, opts WriteOptions) error {
	if !hasTagEdits(opts) {
		return ErrUnsupportedWrite
	}
	return atomicRewrite(path, WAV, func(src, dst *os.File, size int64) error {
		h, err := readAt(src, 0, 12)
		if err != nil {
			return err
		}
		if string(h[:4]) != "RIFF" || string(h[8:]) != "WAVE" || int64(binary.LittleEndian.Uint32(h[4:8])) != size-8 {
			return unsupportedLayout("WAVE RIFF size or subtype")
		}
		if _, err := dst.Write(h); err != nil {
			return err
		}
		changes := riffTextOptions(id3Options(opts, FieldLyrics))
		var info, id3 fileChunk
		var metadataChunks []fileChunk
		var hasFormat, hasData bool
		err = walkChunks(src, 12, size, false, func(c fileChunk) error {
			switch c.id {
			case "fmt ":
				hasFormat = true
			case "data":
				hasData = true
			case "LIST":
				if c.size < 4 {
					return io.ErrUnexpectedEOF
				}
				t, err := readAt(src, c.data, 4)
				if err != nil {
					return err
				}
				switch string(t) {
				case "INFO":
					if info.id != "" {
						return unsupportedLayout("multiple WAVE INFO chunks")
					}
					info = c
					metadataChunks = append(metadataChunks, c)
					return nil
				case "wavl":
					return unsupportedLayout("segmented WAVE data")
				}
			case "ID3 ", "id3 ", "id3\x00":
				if id3.id != "" {
					return unsupportedLayout("multiple WAVE ID3 chunks")
				}
				id3 = c
				metadataChunks = append(metadataChunks, c)
				return nil
			}
			return nil
		})
		if err != nil {
			return err
		}
		if !hasData || !hasFormat {
			return errors.New("missing WAVE format or data")
		}
		// Reuse the trailing metadata area when possible. Earlier tag chunks
		// become JUNK so cue and sample offsets never move.
		tail := size
		for len(metadataChunks) > 0 && metadataChunks[len(metadataChunks)-1].end == tail {
			tail = metadataChunks[len(metadataChunks)-1].start
			metadataChunks = metadataChunks[:len(metadataChunks)-1]
		}
		if err := copySection(dst, src, 12, tail-12); err != nil {
			return err
		}
		for _, c := range metadataChunks {
			if _, err := dst.WriteAt([]byte("JUNK"), c.start); err != nil {
				return err
			}
		}

		if info.id != "" || len(changes) > 0 {
			body, err := editRIFFInfo(src, info, changes)
			if err != nil {
				return err
			}
			if _, err := dst.Write(appendChunk(nil, "LIST", body, false)); err != nil {
				return err
			}
		}
		id3Opts := opts
		id3Opts.ClearFields = slices.Clone(opts.ClearFields)
		for _, field := range riffInfoFields {
			if _, edited := changes[field.id]; edited {
				id3Opts = id3Options(id3Opts, field.field)
				id3Opts.ClearFields = append(id3Opts.ClearFields, field.field)
			}
		}
		if id3.id != "" || opts.Picture != nil || opts.Disc != "" || opts.Language != "" || opts.Style != "" || opts.AlbumArtist != "" || opts.Lyrics != "" || len(opts.CustomTags) > 0 {
			old, err := readID3Block(src, id3.data, id3.size)
			if err != nil {
				return err
			}
			body, err := updateID3Block(old, id3Opts)
			if err != nil {
				return err
			}
			if _, err := dst.Write(appendChunk(nil, "id3 ", body, false)); err != nil {
				return err
			}
		}
		end, err := dst.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if end-8 > math.MaxUint32 {
			return unsupportedLayout("WAVE RIFF size limit")
		}
		_, err = dst.WriteAt(binary.LittleEndian.AppendUint32(nil, uint32(end-8)), 4)
		return err
	})
}
