# MP3/MP4/OGG/FLAC metadata library (read + write)

[![GoDoc](https://pkg.go.dev/badge/github.com/hanxi/tag)](https://pkg.go.dev/github.com/hanxi/tag)

This package provides MP3 (ID3v1,2.{2,3,4}) and MP4 (ACC, M4A, ALAC), OGG and FLAC metadata detection, parsing and artwork extraction. It also supports **writing** tags back to MP3 (ID3v2.3) and FLAC (Vorbis Comment + Picture block); M4A/OGG writers are TODO and return `ErrUnsupportedWrite` for now.

> Forked from upstream and extended with encoding detection improvements plus MP3 (ID3v2.3) / FLAC (Vorbis Comment + PICTURE) writers used by MiMusic.

## Reading

Detect and parse tag metadata from an `io.ReadSeeker` (i.e. an `*os.File`):

```go
m, err := tag.ReadFrom(f)
if err != nil {
	log.Fatal(err)
}
log.Print(m.Format()) // The detected format.
log.Print(m.Title())  // The title of the track (see Metadata interface for more details).
```

## Writing

Write tags to an existing audio file (atomic rewrite via a sibling temp file):

```go
err := tag.WriteTag("song.mp3", tag.WriteOptions{
    Title:       "Sample Title",
    Artist:      "Sample Artist",
    AlbumArtist: "Sample Artist",
    Album:       "Sample Album",
    Year:        2024,
    Genre:       "Pop",
    Lyrics:      "[00:00.00]...",        // UTF-8
    Picture: &tag.Picture{
        MIMEType: "image/jpeg",
        Data:     coverBytes,
    },
})
```

Format dispatch is by file extension:

| Extension | Status | Frames / blocks written |
|-----------|--------|--------------------------|
| `.mp3` | ✅ ID3v2.3 | TIT2 / TPE1 / TPE2 / TALB / TYER / TCON / USLT / APIC |
| `.flac` | ✅ Vorbis Comment + PICTURE | TITLE / ARTIST / ALBUMARTIST / ALBUM / DATE / GENRE / LYRICS + Picture(Front) |
| `.m4a` / `.mp4` / `.m4b` | ⚠️ TODO | Returns `ErrUnsupportedWrite` |
| `.ogg` / `.oga` | ⚠️ TODO | Returns `ErrUnsupportedWrite` |

Other extensions return `ErrUnsupportedWrite`. Callers should treat tag-write failures as non-fatal (log + continue).

Parsed metadata is exported via a single interface (giving a consistent API for all supported metadata formats).

```go
// Metadata is an interface which is used to describe metadata retrieved by this package.
type Metadata interface {
	Format() Format
	FileType() FileType

	Title() string
	Album() string
	Artist() string
	AlbumArtist() string
	Composer() string
	Genre() string
	Year() int

	Track() (int, int) // Number, Total
	Disc() (int, int) // Number, Total

	Picture() *Picture // Artwork
	Lyrics() string
	Comment() string

	Raw() map[string]interface{} // NB: raw tag names are not consistent across formats.
}
```

## Audio Data Checksum (SHA1)

This package also provides a metadata-invariant checksum for audio files: only the audio data is used to
construct the checksum.

[https://pkg.go.dev/github.com/hanxi/tag#Sum](https://pkg.go.dev/github.com/hanxi/tag#Sum)

## Tools

There are simple command-line tools which demonstrate basic tag extraction and summing:

```console
$ go install github.com/hanxi/tag/cmd/tag@latest
$ cd $GOPATH/bin
$ ./tag sample.m4a
Metadata Format: MP4
Title: Sample Title
Album: Sample Album
Artist: Sample Artist
Year: 2024
Track: 1 of 10
Disc: 1 of 1
Picture: Picture{Ext: jpeg, MIMEType: image/jpeg, Type: , Description: , Data.Size: 12345}

$ ./sum sample.m4a
2ae208c5f00a1f21f5fac9b7f6e0b8e52c06da29
```
