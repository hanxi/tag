package tag

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
)

const (
	mkvTrackUID         = 0x73C5
	mkvTargetTrack      = 0x63C5
	mkvTargetEdition    = 0x63C9
	mkvTargetChapter    = 0x63C4
	mkvTargetAttachment = 0x63C6
	mkvFileUID          = 0x46AE
	ebmlCRC             = 0xBF
	ebmlVoid            = 0xEC
	mkvSeekHead         = 0x114D9B74
	mkvSeek             = 0x4DBB
	mkvSeekID           = 0x53AB
	mkvSeekPos          = 0x53AC
)

type ebmlElement struct {
	id               uint64
	start, data, end int64
	sizeWidth        int
	unknown          bool
}

func walkEBML(r io.ReadSeeker, start, end int64, visit func(ebmlElement) error) error {
	for start < end {
		if _, err := r.Seek(start, io.SeekStart); err != nil {
			return err
		}
		id, idWidth, err := readVINT(r, true)
		if err != nil {
			return err
		}
		size, width, err := readVINT(r, false)
		if err != nil {
			return err
		}
		offset := start + int64(idWidth+width)
		unknown := size == (uint64(1)<<(7*width))-1
		if offset > end || !unknown && size > uint64(end-offset) {
			return io.ErrUnexpectedEOF
		}
		stop := end
		if !unknown {
			stop = offset + int64(size)
		}
		if err := visit(ebmlElement{id, start, offset, stop, width, unknown}); err != nil {
			return err
		}
		start = stop
	}
	return nil
}

func matroskaTarget(r io.ReadSeeker, start, end int64) (uint64, bool, error) {
	var uid uint64
	var count int
	eligible := true
	err := walkEBML(r, start, end, func(e ebmlElement) error {
		if e.unknown {
			return unsupportedLayout("unknown Matroska target size")
		}
		switch e.id {
		case mkvTargetTrack:
			count++
			if count > 1 {
				eligible = false
			}
			n, err := elementUint(r, e)
			uid = n
			return err
		case mkvTargetEdition, mkvTargetChapter, mkvTargetAttachment:
			eligible = false
		}
		return nil
	})
	return uid, eligible, err
}

func elementUint(r io.ReadSeeker, e ebmlElement) (uint64, error) {
	if _, err := r.Seek(e.data, io.SeekStart); err != nil {
		return 0, err
	}
	return readEBMLUint(r, e.end-e.data)
}

func canonicalMatroskaField(name string) string {
	switch name = strings.ToLower(strings.TrimSpace(name)); name {
	case "performer":
		return "artist"
	case "part_number", "tracknumber":
		return "track"
	case "disc":
		return "discnumber"
	case "unsynced_lyrics":
		return "lyrics"
	case "lang":
		return "language"
	case "albumartist", "album artist":
		return "album_artist"
	case "date_released", "date_recorded", "date":
		return "year"
	}
	return name
}

func isMatroskaCover(file, mime string) bool {
	name := strings.ToLower(path.Base(file))
	switch strings.TrimSuffix(name, path.Ext(name)) {
	case "cover", "small_cover", "cover_land", "small_cover_land":
		return strings.HasPrefix(strings.ToLower(mime), "image/") || extFromMatroskaCover(mime, file) != ""
	}
	return false
}

func ebmlSize(n uint64, width int) []byte {
	if width == 0 {
		for width = 1; width <= 8 && n >= (uint64(1)<<(7*width))-1; width++ {
		}
	}
	if width > 8 || n >= (uint64(1)<<(7*width))-1 {
		return nil
	}
	return uintBytes(n|uint64(1)<<(7*width), width)
}

func uintBytes(n uint64, width int) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)
	if width == 0 {
		width = 8
		for width > 1 && buf[8-width] == 0 {
			width--
		}
	}
	return buf[8-width:]
}

func ebmlEncode(id uint64, body []byte) []byte {
	b := append(uintBytes(id, 0), ebmlSize(uint64(len(body)), 0)...)
	return append(b, body...)
}

func ebmlAddCRC(body []byte) []byte {
	return append(ebmlEncode(ebmlCRC, binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(body))), body...)
}

func editEBMLMaster(src io.ReadSeeker, start, end int64, visit func(ebmlElement) ([]byte, bool, error), tail []byte) ([]byte, error) {
	var body []byte
	var crc bool
	err := walkEBML(src, start, end, func(e ebmlElement) error {
		if e.unknown || e.end-e.start > int64(maxMetadataSize-len(body)) {
			return unsupportedLayout("EBML metadata layout")
		}
		if e.id == ebmlCRC {
			if crc || e.end-e.data != 4 {
				return unsupportedLayout("EBML metadata CRC")
			}
			crc = true
			return nil
		}
		b, changed, err := visit(e)
		if err != nil {
			return err
		}
		if !changed {
			b, err = readAt(src, e.start, int(e.end-e.start))
		}
		body = append(body, b...)
		return err
	})
	if err != nil {
		return nil, err
	}
	body = append(body, tail...)
	if crc {
		body = ebmlAddCRC(body)
	}
	return body, nil
}

func matroskaEdits(opts WriteOptions) map[string]string {
	edits := make(map[string]string)
	aliases := map[string]string{FieldTrack: "part_number", FieldDisc: "discnumber", FieldYear: "date_released"}
	for _, f := range opts.ClearFields {
		if f == FieldPicture {
			continue
		}
		if a := aliases[f]; a != "" {
			f = a
		}
		edits[strings.ToUpper(f)] = ""
	}
	for k, v := range map[string]string{
		"TITLE": opts.Title, "ARTIST": opts.Artist, "ALBUM": opts.Album, "ALBUM_ARTIST": opts.AlbumArtist,
		"GENRE": opts.Genre, "LANGUAGE": opts.Language, "STYLE": opts.Style, "PART_NUMBER": opts.Track,
		"DISCNUMBER": opts.Disc, "LYRICS": opts.Lyrics,
	} {
		if v != "" {
			edits[k] = v
		}
	}
	if opts.Year > 0 {
		edits["DATE_RELEASED"] = strconv.Itoa(opts.Year)
	}
	for k, v := range opts.CustomTags {
		if k != "" && v != "" {
			edits[strings.ToUpper(k)] = v
		}
	}
	return edits
}

func editMatroskaTags(src io.ReadSeeker, old ebmlElement, track uint64, edits map[string]string) ([]byte, error) {
	var keys []string
	var simple []byte
	matching := make(map[string]bool)
	for k := range edits {
		keys = append(keys, k)
		matching[canonicalMatroskaField(k)] = true
	}
	slices.Sort(keys)
	for _, k := range keys {
		b := append(ebmlEncode(mkvIDTagName, []byte(k)), ebmlEncode(mkvIDTagString, []byte(edits[k]))...)
		simple = append(simple, ebmlEncode(mkvIDSimpleTag, b)...)
	}
	target := append(ebmlEncode(0x68CA, []byte{30}), ebmlEncode(mkvTargetTrack, uintBytes(track, 0))...)
	tag := ebmlEncode(mkvIDTag, append(ebmlEncode(mkvIDTargets, target), simple...))
	body, err := editEBMLMaster(src, old.data, old.end, func(e ebmlElement) ([]byte, bool, error) {
		if e.id != mkvIDTag || len(edits) == 0 {
			return nil, false, nil
		}
		var selected bool
		err := walkEBML(src, e.data, e.end, func(child ebmlElement) error {
			if child.id != mkvIDTargets {
				return nil
			}
			uid, eligible, err := matroskaTarget(src, child.data, child.end)
			selected = eligible && uid == track
			return err
		})
		if err != nil || !selected {
			return nil, false, err
		}
		var remain bool
		b, err := editEBMLMaster(src, e.data, e.end, func(child ebmlElement) ([]byte, bool, error) {
			if child.id != mkvIDSimpleTag {
				return nil, false, nil
			}
			var name string
			var nested bool
			err := walkEBML(src, child.data, child.end, func(field ebmlElement) error {
				if field.id == mkvIDSimpleTag {
					nested = true
				}
				if field.id != mkvIDTagName {
					return nil
				}
				if field.end-field.data > maxMetadataSize {
					return unsupportedLayout("oversized tag name")
				}
				value, err := readAt(src, field.data, int(field.end-field.data))
				name = canonicalMatroskaField(string(value))
				return err
			})
			if err != nil {
				return nil, false, err
			}
			if matching[name] {
				if nested {
					return nil, false, unsupportedLayout("nested Matroska tag being edited")
				}
				return nil, true, nil
			}
			remain = true
			return nil, false, nil
		}, nil)
		if err != nil || !remain {
			return nil, true, err
		}
		return ebmlEncode(mkvIDTag, b), true, nil
	}, tag)
	return ebmlEncode(mkvIDTags, body), err
}

func editMatroskaAttachments(src io.ReadSeeker, old ebmlElement, picture *Picture) ([]byte, error) {
	var found bool
	var retained int
	edit := func(e ebmlElement) ([]byte, bool, error) {
		if e.id != mkvIDAttachedFile {
			return nil, false, nil
		}
		var name, mime string
		err := walkEBML(src, e.data, e.end, func(child ebmlElement) error {
			if child.id != mkvIDFileName && child.id != mkvIDFileMimeType {
				return nil
			}
			if child.end-child.data > maxMetadataSize {
				return unsupportedLayout("oversized attachment name")
			}
			b, err := readAt(src, child.data, int(child.end-child.data))
			if child.id == mkvIDFileName {
				name = string(b)
			} else {
				mime = string(b)
			}
			return err
		})
		if err != nil {
			return nil, false, err
		}
		if !isMatroskaCover(name, mime) {
			retained++
			return nil, false, nil
		}
		if found {
			return nil, false, unsupportedLayout("multiple Matroska covers")
		}
		found = true
		if picture == nil {
			// Conservatively preserve the attachment UID for files that may reference it.
			return nil, false, unsupportedLayout("clearing a Matroska attachment with a persistent UID")
		}
		ext := extFromMatroskaCover(picture.MIMEType, picture.Ext)
		if ext == "" {
			return nil, false, unsupportedLayout("Matroska cover media type")
		}
		b, err := editEBMLMaster(src, e.data, e.end, func(child ebmlElement) ([]byte, bool, error) {
			switch child.id {
			case mkvIDFileName:
				return ebmlEncode(child.id, []byte("cover."+ext)), true, nil
			case mkvIDFileMimeType:
				return ebmlEncode(child.id, []byte(picture.MIMEType)), true, nil
			case mkvIDFileData:
				return ebmlEncode(child.id, picture.Data), true, nil
			}
			return nil, false, nil
		}, nil)
		retained++
		return ebmlEncode(e.id, b), true, err
	}
	body, err := editEBMLMaster(src, old.data, old.end, edit, nil)
	if err != nil {
		return nil, err
	}
	if !found && picture != nil {
		ext := extFromMatroskaCover(picture.MIMEType, picture.Ext)
		if ext == "" {
			return nil, unsupportedLayout("Matroska cover media type")
		}
		uid := make([]byte, 8)
		if _, err := rand.Read(uid); err != nil {
			return nil, err
		}
		uid[0] |= 0x80
		file := ebmlEncode(mkvIDFileName, []byte("cover."+ext))
		file = append(file, ebmlEncode(mkvIDFileMimeType, []byte(picture.MIMEType))...)
		file = append(file, ebmlEncode(mkvFileUID, uid)...)
		file = append(file, ebmlEncode(mkvIDFileData, picture.Data)...)
		body = append(body, ebmlEncode(mkvIDAttachedFile, file)...)
		if len(body) >= 6 && body[0] == ebmlCRC {
			binary.LittleEndian.PutUint32(body[2:6], crc32.ChecksumIEEE(body[6:]))
		}
		retained++
	}
	if retained == 0 {
		return nil, nil
	}
	return ebmlEncode(mkvIDAttachments, body), nil
}

type matroskaReplacement struct {
	old      ebmlElement
	id       uint64
	data     []byte
	position uint64
}

func writeMatroska(path string, opts WriteOptions) error {
	return atomicRewrite(path, UnknownFileType, func(src, dst *os.File, size int64) error {
		metadata, err := ReadMatroskaMeta(src)
		if err != nil {
			return err
		}
		m := metadata.(*metadataMatroska)
		var segment, tags, attachments, segmentCRC ebmlElement
		var seeks []ebmlElement
		seekEnds := make(map[int64]int64)
		err = walkEBML(src, 0, size, func(e ebmlElement) error {
			if e.id != mkvIDSegment {
				return nil
			}
			if segment.id != 0 || e.end != size {
				return unsupportedLayout("multiple or non-final Matroska segments")
			}
			segment = e
			var previous ebmlElement
			return walkEBML(src, e.data, e.end, func(child ebmlElement) error {
				if child.id == ebmlVoid && previous.id == mkvSeekHead {
					seekEnds[previous.start] = child.end
				}
				previous = child
				if child.unknown {
					return unsupportedLayout("unknown-sized Matroska child")
				}
				switch child.id {
				case mkvIDTags:
					if tags.id != 0 {
						return unsupportedLayout("multiple Matroska tag containers")
					}
					tags = child
				case mkvIDAttachments:
					if attachments.id != 0 {
						return unsupportedLayout("multiple Matroska attachment containers")
					}
					attachments = child
				case mkvSeekHead:
					seeks = append(seeks, child)
				case ebmlCRC:
					if segmentCRC.id != 0 || child.end-child.data != 4 {
						return unsupportedLayout("Matroska segment CRC")
					}
					segmentCRC = child
				}
				return nil
			})
		})
		if err != nil {
			return err
		}
		edits := matroskaEdits(opts)
		var changes []matroskaReplacement
		if len(edits) > 0 {
			if m.trackUID == 0 {
				return unsupportedLayout("Matroska primary audio track UID")
			}
			b, err := editMatroskaTags(src, tags, m.trackUID, edits)
			if err != nil {
				return err
			}
			changes = append(changes, matroskaReplacement{old: tags, id: mkvIDTags, data: b})
		}
		if m.docType != "webm" && (opts.Picture != nil || slices.Contains(opts.ClearFields, FieldPicture)) {
			b, err := editMatroskaAttachments(src, attachments, opts.Picture)
			if err != nil {
				return err
			}
			if len(b) > 0 {
				changes = append(changes, matroskaReplacement{old: attachments, id: mkvIDAttachments, data: b})
			}
		}
		if len(changes) == 0 {
			return ErrUnsupportedWrite
		}
		if err := copySection(dst, src, 0, size); err != nil {
			return err
		}
		end := size
		for i, change := range changes {
			changes[i].position = uint64(end - segment.data)
			if _, err := dst.Write(change.data); err != nil {
				return err
			}
			end += int64(len(change.data))
			if change.old.id != 0 {

				header := ebmlVoidHeader(change.old.end - change.old.start)
				if header == nil {
					return unsupportedLayout("Matroska metadata void")
				}
				if _, err := dst.WriteAt(header, change.old.start); err != nil {
					return err
				}

			}
		}
		if !segment.unknown {
			b := ebmlSize(uint64(end-segment.data), segment.sizeWidth)
			if b == nil {
				return unsupportedLayout("Matroska segment size width")
			}
			if _, err := dst.WriteAt(b, segment.data-int64(segment.sizeWidth)); err != nil {
				return err
			}
		}
		for i, seek := range seeks {
			end := max(seek.end, seekEnds[seek.start])
			if err := updateMatroskaSeek(src, dst, seek, end, segment, changes, i == 0); err != nil {
				return err
			}
		}
		return updateMatroskaCRC(dst, segmentCRC, segment.data, end)
	})
}

func ebmlVoidHeader(length int64) []byte {
	for width := 1; width <= 8 && int64(1+width) <= length; width++ {
		if n := ebmlSize(uint64(length-1-int64(width)), width); n != nil {
			return append([]byte{ebmlVoid}, n...)
		}
	}
	return nil
}

func updateMatroskaSeek(src, dst *os.File, seek ebmlElement, limit int64, segment ebmlElement, changes []matroskaReplacement, primary bool) error {
	changed := false
	var additions []byte
	if primary {
		for _, c := range changes {
			if c.old.id != 0 {
				continue
			}
			id := ebmlEncode(mkvSeekID, uintBytes(c.id, 0))
			position := ebmlEncode(mkvSeekPos, uintBytes(c.position, 0))
			additions = append(additions, ebmlEncode(mkvSeek, append(id, position...))...)
		}
		changed = len(additions) > 0
	}
	body, err := editEBMLMaster(src, seek.data, seek.end, func(entry ebmlElement) ([]byte, bool, error) {
		if entry.id != mkvSeek {
			return nil, false, nil
		}
		var id uint64
		var position ebmlElement
		err := walkEBML(src, entry.data, entry.end, func(field ebmlElement) error {
			switch field.id {
			case mkvSeekID:
				var err error
				id, err = elementUint(src, field)
				return err
			case mkvSeekPos:
				if position.id != 0 {
					return unsupportedLayout("duplicate Matroska seek position")
				}
				position = field
			}
			return nil
		})
		if err != nil || position.id == 0 {
			return nil, false, err
		}
		for _, change := range changes {
			if id != change.id {
				continue
			}
			if change.old.id == 0 {
				return nil, false, unsupportedLayout("dangling Matroska metadata reference")
			}
			old, err := elementUint(src, position)
			if err != nil {
				return nil, false, err
			}
			if old != uint64(change.old.start-segment.data) {
				return nil, false, unsupportedLayout("Matroska seek target")
			}
			value := uintBytes(change.position, 0)
			oldWidth := position.end - position.data
			if oldWidth > int64(len(value)) && oldWidth <= 8 {
				value = uintBytes(change.position, int(oldWidth))
			}
			newEntry, err := editEBMLMaster(src, entry.data, entry.end, func(field ebmlElement) ([]byte, bool, error) {
				if field.id == mkvSeekPos {
					return ebmlEncode(mkvSeekPos, value), true, nil
				}
				return nil, false, nil
			}, nil)
			changed = true
			return ebmlEncode(entry.id, newEntry), true, err
		}
		return nil, false, nil
	}, additions)
	if err != nil || !changed {
		return err
	}

	replacement := ebmlEncode(mkvSeekHead, body)
	padding := limit - seek.start - int64(len(replacement))
	if padding == 1 {
		// A one-byte remainder cannot hold a Void. Extend the element size field.
		id := uintBytes(mkvSeekHead, 0)
		width := len(replacement) - len(body) - len(id) + 1
		size := ebmlSize(uint64(len(body)), width)
		if size == nil {
			return unsupportedLayout("Matroska SeekHead size")
		}
		replacement = append(append(id, size...), body...)
		padding = 0
	}
	if padding < 0 {
		return unsupportedLayout("insufficient space for Matroska seek index")
	}
	if _, err := dst.WriteAt(replacement, seek.start); err != nil {
		return err
	}
	if padding > 0 {
		header := ebmlVoidHeader(padding)
		if header == nil {
			return unsupportedLayout("Matroska SeekHead padding")
		}
		_, err = dst.WriteAt(header, seek.start+int64(len(replacement)))
	}
	return err
}

func updateMatroskaCRC(dst *os.File, crc ebmlElement, start, end int64) error {
	if crc.id == 0 {
		return nil
	}
	if crc.end-crc.data != 4 {
		return fmt.Errorf("invalid Matroska CRC element")
	}
	hash := crc32.NewIEEE()
	if err := copySection(hash, dst, start, crc.start-start); err != nil {
		return err
	}
	if err := copySection(hash, dst, crc.end, end-crc.end); err != nil {
		return err
	}
	_, err := dst.WriteAt(binary.LittleEndian.AppendUint32(nil, hash.Sum32()), crc.data)
	return err
}
