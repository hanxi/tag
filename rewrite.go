package tag

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const maxMetadataSize = 32 << 20

var rewriteLocks [256]sync.Mutex

func atomicRewrite(path string, kind FileType, write func(*os.File, *os.File, int64) error) error {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	var hash byte
	for i := range len(path) {
		hash = hash*31 + path[i]
	}
	lock := &rewriteLocks[hash]
	lock.Lock()
	defer lock.Unlock()

	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	before, err := src.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return ErrUnsupportedWrite
	}

	dst, err := os.CreateTemp(filepath.Dir(path), ".songloft-tag-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(dst.Name())
	defer dst.Close()
	if err := write(src, dst, before.Size()); err != nil {
		return err
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	meta, err := ReadFrom(dst)
	if err != nil {
		return fmt.Errorf("validate rewritten metadata: %w", err)
	}
	if kind != UnknownFileType && meta.FileType() != kind {
		return fmt.Errorf("rewritten file type %s, expected %s", meta.FileType(), kind)
	}
	if err := dst.Chmod(before.Mode().Perm()); err != nil {
		return err
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		return errors.New("source file changed during tag write")
	}
	if err := src.Close(); err != nil {
		return err
	}
	return os.Rename(dst.Name(), path)
}

func copySection(dst io.Writer, src *os.File, offset, size int64) error {
	_, err := io.CopyN(dst, io.NewSectionReader(src, offset, size), size)
	return err
}

func readAt(r io.ReadSeeker, offset int64, n int) ([]byte, error) {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return readBytes(r, uint(n))
}

func unsupportedLayout(name string) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedWrite, name)
}

// WriteOptions.ClearFields uses these public metadata field names. An empty
// option otherwise leaves a field untouched in the incremental writers.
const (
	FieldTitle       = "title"
	FieldArtist      = "artist"
	FieldAlbum       = "album"
	FieldAlbumArtist = "album_artist"
	FieldYear        = "year"
	FieldGenre       = "genre"
	FieldLanguage    = "language"
	FieldStyle       = "style"
	FieldTrack       = "track"
	FieldDisc        = "disc"
	FieldLyrics      = "lyrics"
	FieldPicture     = "picture"
)

func hasTagEdits(opts WriteOptions) bool {
	return len(textEdits(opts)) > 0 || opts.Picture != nil || len(opts.CustomTags) > 0
}
