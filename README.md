# MP3/MP4/OGG/FLAC metadata library (read + write)

[![GoDoc](https://pkg.go.dev/badge/github.com/hanxi/tag)](https://pkg.go.dev/github.com/hanxi/tag)

This package provides MP3 (ID3v1,2.{2,3,4}), MP4 (AAC, M4A, ALAC), OGG, FLAC, WAV, Monkey's Audio (APE), AIFF/AIFF-C, DSF (DSD), and Matroska (.mka/.mkv) metadata detection, parsing and artwork extraction. It also supports **writing** tags back to MP3 (ID3v2.3), FLAC (Vorbis Comment + Picture), M4A/MP4/M4B/MOV (iTunes atoms), OGG (Vorbis Comment), APE (APEv2 footer), WAV (RIFF LIST INFO), and AIFF/AIF (ID3v2.3 chunk).

> Forked from upstream and extended with encoding detection improvements plus multi-format tag writers used by Songloft.

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
    Language:    "eng",
    Style:       "Synth Pop",
    Track:       "3/12",
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
| `.m4a` / `.mp4` / `.m4b` / `.mov` | ✅ iTunes atoms | ©nam / ©ART / aART / ©alb / ©day / ©gen / ©lyr / covr |
| `.ogg` / `.oga` | ✅ Vorbis Comment | TITLE / ARTIST / ALBUMARTIST / ALBUM / DATE / GENRE / LYRICS + METADATA_BLOCK_PICTURE |
| `.ape` | ✅ APEv2 | Title / Artist / Album / Year / Genre / Lyrics / Cover Art (Front) |
| `.wav` | ✅ RIFF LIST INFO | INAM / IART / IPRD / ICRD / IGNR / ICMT |
| `.aif` / `.aiff` | ✅ ID3v2.3 (ID3 chunk) | TIT2 / TPE1 / TPE2 / TALB / TYER / TCON / USLT / APIC + NAME / AUTH |

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
	Language() string
	Style() string

	Track() (int, int) // Number, Total
	Disc() (int, int) // Number, Total

	Picture() *Picture // Artwork
	Lyrics() string
	Comment() string

	Raw() map[string]interface{} // NB: raw tag names are not consistent across formats.

	Duration() time.Duration // Audio duration (from stream headers, not tags)
	BitRate() int            // Bits per second
	SampleRate() int         // Samples per second
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
