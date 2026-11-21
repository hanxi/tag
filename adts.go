package tag

import (
	"bufio"
	"errors"
	"io"
	"os"
	"time"
)

var adtsSampleRates = [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

func isADTSHeader(h []byte) bool {
	return len(h) >= 7 && h[0] == 0xff && h[1]&0xf6 == 0xf0 && int(h[2]>>2&15) < len(adtsSampleRates)
}

// adtsStart checks the stream signature after an optional leading ID3v2 block.
func adtsStart(r io.ReadSeeker) (int64, error) {
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	defer r.Seek(pos, io.SeekStart)
	h, err := readAt(r, 0, 10)
	if err != nil {
		return 0, err
	}
	if isADTSHeader(h) {
		return 0, nil
	}
	n, err := id3BlockSize(h)
	if err != nil || n > maxMetadataSize {
		return 0, errors.New("invalid ADTS prefix")
	}
	b, err := readAt(r, int64(n), 7)
	if err != nil || !isADTSHeader(b) {
		return 0, errors.New("missing ADTS frame")
	}
	return int64(n), nil
}

type metadataADTS struct {
	*metadataID3v2
	rate, bitrate int
	duration      time.Duration
}

func (m metadataADTS) FileType() FileType      { return AAC }
func (m metadataADTS) SampleRate() int         { return m.rate }
func (m metadataADTS) BitRate() int            { return m.bitrate }
func (m metadataADTS) Duration() time.Duration { return m.duration }

func readADTS(r io.ReadSeeker) (Metadata, error) {
	start, err := adtsStart(r)
	if err != nil {
		return nil, err
	}
	tags, err := readOptionalID3(r, 0, start)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(r, 32<<10)
	var header [7]byte
	var totalBytes, samples uint64
	rate := 0
	for {
		_, err := io.ReadFull(br, header[:])
		if err == io.EOF && samples > 0 {
			break
		}
		if err != nil {
			return nil, err
		}
		if !isADTSHeader(header[:]) {
			return nil, errors.New("invalid ADTS frame")
		}
		n := int(header[3]&3)<<11 | int(header[4])<<3 | int(header[5]>>5)
		minSize := 7
		if header[1]&1 == 0 {
			minSize = 9
		}
		if n <= minSize {
			return nil, errors.New("invalid ADTS frame length")
		}
		frameRate := adtsSampleRates[header[2]>>2&15]
		if rate != 0 && rate != frameRate {
			return nil, errors.New("ADTS sample rate changes midstream")
		}
		rate = frameRate
		if _, err := br.Discard(n - 7); err != nil {
			return nil, io.ErrUnexpectedEOF
		}
		totalBytes += uint64(n)
		samples += 1024 * uint64(1+header[6]&3)
	}
	duration := sampleDuration(samples, rate)
	bitrate := 0
	if duration > 0 {
		bitrate = int(float64(totalBytes) * 0.008 / duration.Seconds())
	}
	return metadataADTS{tags, rate, bitrate, duration}, nil
}

func writeADTS(path string, opts WriteOptions) error {
	if !hasTagEdits(opts) {
		return ErrUnsupportedWrite
	}
	return atomicRewrite(path, AAC, func(src, dst *os.File, size int64) error {
		start, err := adtsStart(src)
		if err != nil {
			return err
		}
		old, err := readID3Block(src, 0, start)
		if err != nil {
			return err
		}
		edited, err := updateID3Block(old, opts)
		if err != nil {
			return err
		}
		if _, err := dst.Write(edited); err != nil {
			return err
		}
		return copySection(dst, src, start, size-start)
	})
}
