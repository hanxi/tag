// Copyright 2026 songloft contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// copyFixture 把 testdata 文件复制到临时目录,返回临时路径
func copyFixture(t *testing.T, fixture string) string {
	t.Helper()
	src, err := os.Open(fixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer src.Close()

	dst, err := os.CreateTemp(t.TempDir(), "tag-write-*"+filepath.Ext(fixture))
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatalf("copy: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return dst.Name()
}

// readBackMetadata 用 ReadFrom 读回写入后的 metadata
func readBackMetadata(t *testing.T, path string) Metadata {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	m, err := ReadFrom(f)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	return m
}

func TestWriteID3v2_RoundTrip_NoExistingTag(t *testing.T) {
	path := copyFixture(t, "testdata/without_tags/sample.mp3")

	opts := WriteOptions{
		Title:       "My Title",
		Artist:      "My Artist",
		AlbumArtist: "Album Artist",
		Album:       "My Album",
		Year:        2026,
		Genre:       "Rock",
		Lyrics:      "Line 1\nLine 2 中文",
		Picture: &Picture{
			MIMEType: "image/jpeg",
			Data:     []byte{0xff, 0xd8, 0xff, 0xe0, 0xde, 0xad, 0xbe, 0xef},
		},
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
	if got := m.Album(); got != opts.Album {
		t.Errorf("Album: got %q, want %q", got, opts.Album)
	}
	if got := m.AlbumArtist(); got != opts.AlbumArtist {
		t.Errorf("AlbumArtist: got %q, want %q", got, opts.AlbumArtist)
	}
	if got := m.Year(); got != opts.Year {
		t.Errorf("Year: got %d, want %d", got, opts.Year)
	}
	if got := m.Genre(); got != opts.Genre {
		t.Errorf("Genre: got %q, want %q", got, opts.Genre)
	}
	if got := m.Lyrics(); got != opts.Lyrics {
		t.Errorf("Lyrics: got %q, want %q", got, opts.Lyrics)
	}
	if pic := m.Picture(); pic == nil {
		t.Error("Picture: got nil, want non-nil")
	} else if string(pic.Data) != string(opts.Picture.Data) {
		t.Errorf("Picture data mismatch: got %d bytes, want %d bytes", len(pic.Data), len(opts.Picture.Data))
	}
}

func TestWriteID3v2_RoundTrip_OverwriteExistingTag(t *testing.T) {
	path := copyFixture(t, "testdata/with_tags/sample.id3v23.mp3")

	opts := WriteOptions{
		Title:  "Replaced Title",
		Artist: "Replaced Artist",
		Album:  "Replaced Album",
		Year:   1999,
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
	if got := m.Album(); got != opts.Album {
		t.Errorf("Album: got %q, want %q", got, opts.Album)
	}
	if got := m.Year(); got != opts.Year {
		t.Errorf("Year: got %d, want %d", got, opts.Year)
	}
}

func TestWriteFLAC_RoundTrip(t *testing.T) {
	path := copyFixture(t, "testdata/without_tags/sample.flac")

	opts := WriteOptions{
		Title:  "FLAC Title",
		Artist: "FLAC Artist",
		Album:  "FLAC Album",
		Year:   2026,
		Genre:  "Classical",
		Lyrics: "FLAC Lyrics\n第二行",
		Picture: &Picture{
			MIMEType: "image/png",
			Data:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		},
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
	if got := m.Album(); got != opts.Album {
		t.Errorf("Album: got %q, want %q", got, opts.Album)
	}
	if got := m.Year(); got != opts.Year {
		t.Errorf("Year: got %d, want %d", got, opts.Year)
	}
	if got := m.Genre(); got != opts.Genre {
		t.Errorf("Genre: got %q, want %q", got, opts.Genre)
	}
	if pic := m.Picture(); pic == nil {
		t.Error("Picture: got nil, want non-nil")
	} else if string(pic.Data) != string(opts.Picture.Data) {
		t.Errorf("Picture data mismatch: got %d bytes, want %d bytes", len(pic.Data), len(opts.Picture.Data))
	}
}

func TestWriteFLAC_WithID3v2Prefix(t *testing.T) {
	path := copyFixture(t, "testdata/with_id3v2_prefix/sample.flac")

	opts := WriteOptions{
		Title:  "New Title",
		Artist: "New Artist",
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	// 写入后 ID3v2 前缀应被剥离，文件以 fLaC 开头
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		t.Fatalf("read magic: %v", err)
	}
	f.Close()
	if string(magic[:]) != "fLaC" {
		t.Errorf("after write, file should start with fLaC, got %q", string(magic[:]))
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
}

func TestWriteTag_UnsupportedFormat(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "unsupported-*.xyz")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	tmp.Close()

	err = WriteTag(tmp.Name(), WriteOptions{Title: "x"})
	if err == nil {
		t.Fatal("WriteTag: expected error, got nil")
	}
	if !errors.Is(err, ErrUnsupportedWrite) {
		t.Errorf("expected ErrUnsupportedWrite, got %v", err)
	}
}

func TestWriteMP4_RoundTrip_NoExistingTag(t *testing.T) {
	path := copyFixture(t, "testdata/without_tags/sample.m4a")

	opts := WriteOptions{
		Title:       "M4A Title",
		Artist:      "M4A Artist",
		AlbumArtist: "Album Artist",
		Album:       "M4A Album",
		Year:        2026,
		Genre:       "Pop",
		Lyrics:      "M4A Lyrics\n第二行",
		Picture: &Picture{
			MIMEType: "image/jpeg",
			Data:     []byte{0xff, 0xd8, 0xff, 0xe0, 0xde, 0xad, 0xbe, 0xef},
		},
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
	if got := m.Album(); got != opts.Album {
		t.Errorf("Album: got %q, want %q", got, opts.Album)
	}
	if got := m.AlbumArtist(); got != opts.AlbumArtist {
		t.Errorf("AlbumArtist: got %q, want %q", got, opts.AlbumArtist)
	}
	if got := m.Year(); got != opts.Year {
		t.Errorf("Year: got %d, want %d", got, opts.Year)
	}
	if got := m.Genre(); got != opts.Genre {
		t.Errorf("Genre: got %q, want %q", got, opts.Genre)
	}
	if got := m.Lyrics(); got != opts.Lyrics {
		t.Errorf("Lyrics: got %q, want %q", got, opts.Lyrics)
	}
	if pic := m.Picture(); pic == nil {
		t.Error("Picture: got nil, want non-nil")
	} else if string(pic.Data) != string(opts.Picture.Data) {
		t.Errorf("Picture data mismatch: got %d bytes, want %d bytes", len(pic.Data), len(opts.Picture.Data))
	}
}

func TestWriteMP4_RoundTrip_OverwriteExistingTag(t *testing.T) {
	path := copyFixture(t, "testdata/with_tags/sample.m4a")

	opts := WriteOptions{
		Title:  "Replaced Title",
		Artist: "Replaced Artist",
		Album:  "Replaced Album",
		Year:   1999,
		Lyrics: "New lyrics",
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
	if got := m.Album(); got != opts.Album {
		t.Errorf("Album: got %q, want %q", got, opts.Album)
	}
	if got := m.Year(); got != opts.Year {
		t.Errorf("Year: got %d, want %d", got, opts.Year)
	}
	if got := m.Lyrics(); got != opts.Lyrics {
		t.Errorf("Lyrics: got %q, want %q", got, opts.Lyrics)
	}
}

func TestWriteOGG_RoundTrip_NoExistingTag(t *testing.T) {
	path := copyFixture(t, "testdata/without_tags/sample.ogg")

	opts := WriteOptions{
		Title:       "OGG Title",
		Artist:      "OGG Artist",
		AlbumArtist: "Album Artist",
		Album:       "OGG Album",
		Year:        2026,
		Genre:       "Rock",
		Lyrics:      "OGG Lyrics\n第二行",
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
	if got := m.Album(); got != opts.Album {
		t.Errorf("Album: got %q, want %q", got, opts.Album)
	}
	if got := m.AlbumArtist(); got != opts.AlbumArtist {
		t.Errorf("AlbumArtist: got %q, want %q", got, opts.AlbumArtist)
	}
	if got := m.Year(); got != opts.Year {
		t.Errorf("Year: got %d, want %d", got, opts.Year)
	}
	if got := m.Genre(); got != opts.Genre {
		t.Errorf("Genre: got %q, want %q", got, opts.Genre)
	}
	if got := m.Lyrics(); got != opts.Lyrics {
		t.Errorf("Lyrics: got %q, want %q", got, opts.Lyrics)
	}
}

func TestWriteOGG_RoundTrip_OverwriteExistingTag(t *testing.T) {
	path := copyFixture(t, "testdata/with_tags/sample.ogg")

	opts := WriteOptions{
		Title:  "Replaced Title",
		Artist: "Replaced Artist",
		Album:  "Replaced Album",
		Year:   1999,
		Lyrics: "New OGG lyrics",
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
	if got := m.Album(); got != opts.Album {
		t.Errorf("Album: got %q, want %q", got, opts.Album)
	}
	if got := m.Year(); got != opts.Year {
		t.Errorf("Year: got %d, want %d", got, opts.Year)
	}
	if got := m.Lyrics(); got != opts.Lyrics {
		t.Errorf("Lyrics: got %q, want %q", got, opts.Lyrics)
	}
}

func TestWriteOGG_RoundTrip_MultiPage(t *testing.T) {
	path := copyFixture(t, "testdata/with_tags/sample.multipage.ogg")

	opts := WriteOptions{
		Title:  "Multipage Title",
		Artist: "Multipage Artist",
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if got := m.Artist(); got != opts.Artist {
		t.Errorf("Artist: got %q, want %q", got, opts.Artist)
	}
}

func TestWriteOGG_WithPicture(t *testing.T) {
	path := copyFixture(t, "testdata/without_tags/sample.ogg")

	opts := WriteOptions{
		Title:  "With Pic",
		Artist: "Artist",
		Picture: &Picture{
			MIMEType: "image/jpeg",
			Data:     []byte{0xff, 0xd8, 0xff, 0xe0, 0xde, 0xad, 0xbe, 0xef},
		},
	}
	if err := WriteTag(path, opts); err != nil {
		t.Fatalf("WriteTag: %v", err)
	}

	m := readBackMetadata(t, path)
	if got := m.Title(); got != opts.Title {
		t.Errorf("Title: got %q, want %q", got, opts.Title)
	}
	if pic := m.Picture(); pic == nil {
		t.Error("Picture: got nil, want non-nil")
	} else if string(pic.Data) != string(opts.Picture.Data) {
		t.Errorf("Picture data mismatch: got %d bytes, want %d bytes", len(pic.Data), len(opts.Picture.Data))
	}
}
