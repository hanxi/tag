package tag

import (
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

type metadataLegacy struct {
	*metadataAPE
	kind   FileType
	format Format
}

func (m metadataLegacy) FileType() FileType { return m.kind }
func (m metadataLegacy) Format() Format     { return m.format }
func (m metadataLegacy) BitRate() int       { return 0 }

func newLegacyMetadata(kind FileType, format Format) metadataLegacy {
	return metadataLegacy{&metadataAPE{}, kind, format}
}

func textEdits(opts WriteOptions) map[string]string {
	values := map[string]string{
		FieldTitle: opts.Title, FieldArtist: opts.Artist, FieldAlbum: opts.Album,
		FieldAlbumArtist: opts.AlbumArtist, FieldGenre: opts.Genre, FieldLanguage: opts.Language,
		FieldStyle: opts.Style, FieldTrack: opts.Track, FieldDisc: opts.Disc, FieldLyrics: opts.Lyrics,
	}
	if opts.Year > 0 {
		values[FieldYear] = strconv.Itoa(opts.Year)
	}
	for k, v := range values {
		if v == "" {
			delete(values, k)
		}
	}
	for _, k := range opts.ClearFields {
		values[k] = ""
	}
	return values
}

func (m *metadataLegacy) setField(field, value string) {
	key := field
	if field == FieldAlbumArtist {
		key = "album artist"
	}
	m.applyAPEItem(key, 0, []byte(value))
}

type textSlot struct {
	field    string
	offset   int64
	size     int
	encoding string
}

type legacyParser func(io.ReadSeeker) (metadataLegacy, []textSlot, error)

func writeFixedText(path string, kind FileType, opts WriteOptions, parse legacyParser, supported ...string) error {
	edits := textEdits(opts)
	for key := range edits {
		if !slices.Contains(supported, key) {
			delete(edits, key)
		}
	}
	if len(edits) == 0 {
		return ErrUnsupportedWrite
	}
	return atomicRewrite(path, kind, func(src, dst *os.File, size int64) error {
		_, slots, err := parse(src)
		if err != nil {
			return err
		}
		seen := make(map[string]bool)
		for _, slot := range slots {
			value, edited := edits[slot.field]
			if !edited {
				continue
			}
			if strings.ContainsRune(value, 0) {
				return errors.New("embedded NUL in text metadata")
			}
			b := []byte(value)
			switch slot.encoding {
			case "unsupported":
				return unsupportedLayout("existing field uses an unsupported encoding")
			case "integer":
				n, err := strconv.ParseUint(value, 10, 8*slot.size)
				if err != nil {
					return unsupportedLayout("ASF field needs an integer")
				}
				b = make([]byte, slot.size)
				for i := range b {
					b[i] = byte(n >> (8 * i))
				}
			case "utf16":
				b = encodeUTF16LE(value)
				if len(b)+2 > slot.size {
					return unsupportedLayout("insufficient ASF metadata space")
				}
			case "exact":
				if len(b) != slot.size {
					return unsupportedLayout("metadata edit changes block length")
				}
			default:
				if len(b) > slot.size {
					return unsupportedLayout("insufficient metadata space")
				}
			}
			if len(seen) == 0 {
				if err := copySection(dst, src, 0, size); err != nil {
					return err
				}
			}
			seen[slot.field] = true
			data := make([]byte, slot.size)
			copy(data, b)
			if _, err := dst.WriteAt(data, slot.offset); err != nil {
				return err
			}
		}
		if len(seen) != len(edits) {
			return unsupportedLayout("missing native metadata slot")
		}
		return nil
	})
}
