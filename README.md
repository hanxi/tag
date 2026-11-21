# MP3/MP4/OGG/FLAC metadata library (read + write)

[![GoDoc](https://pkg.go.dev/badge/github.com/hanxi/tag)](https://pkg.go.dev/github.com/hanxi/tag)

This package provides MP3 (ID3v1,2.{2,3,4}), MP4 (AAC, M4A, ALAC), OGG, Opus, FLAC, WAV, Monkey's Audio (APE), AIFF/AIFF-C, DSF (DSD), and Matroska (.mka/.mkv) metadata detection, parsing and artwork extraction. The table below includes the additional audio and video containers and the boundaries of their writers.

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

Container dispatch uses file contents. Incremental writers preserve unspecified fields and use `WriteOptions.ClearFields` for explicit clears. `ErrUnsupportedWrite` identifies fields or layouts that require application-level storage.

| Container | Read metadata | Native write |
|---|---|---|
| MP3, FLAC, Ogg/Opus, APE, AIFF | Text, lyrics, artwork | Existing ID3, Vorbis, APEv2 and AIFF writers |
| MP4/M4A/M4B/M4V/3GP | iTunes text, numbers, lyrics, artwork | Existing iTunes metadata writer |
| QuickTime MOV | Legacy and iTunes text, numbers, lyrics, artwork | iTunes metadata in supported unfragmented layouts |
| DSF, ADTS AAC | ID3v2 text, numbers, lyrics, artwork | Incremental ID3v2.3/2.4 |
| WavPack | APEv2 text, numbers, lyrics, artwork | Incremental APEv2 |
| WAV | INFO titles, artists, albums, years, genres, tracks and comments. ID3v2 fields, lyrics and artwork | RIFF INFO and ID3v2.3/2.4 |
| DSDIFF | DIIN title/artist, compatible ID3 and DSD/DST stream parameters | DIIN title/artist with preserved index offsets |
| Matroska/WebM | Primary audio tags, numbers and lyrics. Matroska cover attachments | Primary audio tags. Matroska supports attaching or replacing a cover |
| ASF | Content Description and mapped Extended Content Description, including front cover | Existing mapped text or integer slots in the original header extent |
| AVI | RIFF INFO fields, including OpenDML | RIFF INFO text and track in a compatible single RIFF segment |
| FLV | AMF0 `onMetaData` fields | Existing strings with unchanged byte length |
| RealMedia | CONT title, author, copyright and comment | Title and author within the original CONT object |
| DTS/DTS-HD, MPEG-TS/PS | DTS stream parameters, MPEG service and stream descriptors | Application-level storage |

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
