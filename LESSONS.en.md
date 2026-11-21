# Development lessons

## TAG-001: Vorbis numbers use both combined and separate forms

- Scope: FLAC/OGG metadata reading; songloft-org/songloft#571, baseline `64e12e525cc5c9a5ee66826c98dbaed42534af8b`.
- Evidence: the old reader directly called Atoi, returning 0 for `TRACKNUMBER=02/12` and `DISCNUMBER=1/2`, and ignored totaltracks/totaldiscs aliases. An isolated old vorbis.go fails combined, aliases and override regressions: `/tmp/songloft-571-negative/vorbis.log`.
- Rule / done right: parse number/total first, then override the total with a valid separate field; accept tracktotal/totaltracks and disctotal/totaldiscs. The new reader, MP3/FLAC/M4A/OGG/APE/AIFF disc writing and preservation for old callers pass; `go test ./...` evidence: `/tmp/songloft-571-tag.log`.
- Seen 1× (2026-11-16). Status: this batch's fix and verification; final commits and CI are reported on songloft-org/songloft#571, with no release acceptance. Chinese: [LESSONS.md](LESSONS.md).

## TAG-002 · Reserve space for enlarged Matroska seek positions

- Scope: incremental Matroska metadata changes in the worktree based on `efb3ff6022919b6b0e6b3162fbfa9f7402806a6a`.
- A generated MKV with video, two audio tracks, two chapters, and a text attachment stored a short seek position for its original Tags element. Appending replacement tags moved the target beyond that integer width.
- Reuse the Void immediately following SeekHead to preserve its location while expanding the position. Recompute the index CRC and retain media offsets. FFprobe confirmed matching audio and video packet hashes and seek results, unchanged chapters and second audio track, and both the old attachment and new cover.
- Seen 1× (2026-11-21).
