// Copyright 2026 songloft contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"os"
	"strings"
	"testing"
)

// TestMatroskaIdentify 校验 EBML magic 被正确识别为 Matroska / MKA。
func TestMatroskaIdentify(t *testing.T) {
	f, err := os.Open("testdata/with_tags/sample.mka")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	format, fileType, err := Identify(f)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if format != Matroska {
		t.Errorf("format = %q, want %q", format, Matroska)
	}
	if fileType != MKA {
		t.Errorf("fileType = %q, want %q", fileType, MKA)
	}
}

// TestMatroskaReadTags 用小体积 fixture（含 title/artist/album/genre/date/comment/lyrics + 封面）
// 校验 tag.ReadFrom 能读出容器级 Matroska 标签与封面附件。
func TestMatroskaReadTags(t *testing.T) {
	f, err := os.Open("testdata/with_tags/sample.mka")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	m, err := ReadFrom(f)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	if m.Format() != Matroska {
		t.Errorf("Format() = %q, want %q", m.Format(), Matroska)
	}
	if m.FileType() != MKA {
		t.Errorf("FileType() = %q, want %q", m.FileType(), MKA)
	}
	if got, want := m.Title(), "Matroska Title"; got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
	if got, want := m.Artist(), "Matroska Artist"; got != want {
		t.Errorf("Artist() = %q, want %q", got, want)
	}
	if got, want := m.Album(), "Matroska Album"; got != want {
		t.Errorf("Album() = %q, want %q", got, want)
	}
	if got, want := m.Genre(), "Rock"; got != want {
		t.Errorf("Genre() = %q, want %q", got, want)
	}
	if got, want := m.Year(), 2005; got != want {
		t.Errorf("Year() = %d, want %d", got, want)
	}
	if got, want := m.Comment(), "hello comment"; got != want {
		t.Errorf("Comment() = %q, want %q", got, want)
	}
	if lyr := m.Lyrics(); !strings.Contains(lyr, "line one") || !strings.Contains(lyr, "line two") {
		t.Errorf("Lyrics() = %q, want to contain 'line one' and 'line two'", lyr)
	}

	// 采样率来自 Tracks > TrackEntry > Audio > SamplingFrequency
	if sr := m.SampleRate(); sr != 44100 {
		t.Errorf("SampleRate() = %d, want 44100", sr)
	}

	// 时长来自 Info > Duration * TimestampScale（fixture 约 1s，容忍误差）
	if d := m.Duration().Seconds(); d < 0.5 || d > 3 {
		t.Errorf("Duration() = %.3fs, want ~1s", d)
	}

	// 封面附件
	pic := m.Picture()
	if pic == nil {
		t.Fatal("Picture() = nil, want cover attachment")
	}
	if pic.MIMEType != "image/jpeg" {
		t.Errorf("Picture().MIMEType = %q, want image/jpeg", pic.MIMEType)
	}
	if pic.Ext != "jpg" {
		t.Errorf("Picture().Ext = %q, want jpg", pic.Ext)
	}
	if len(pic.Data) == 0 {
		t.Error("Picture().Data is empty")
	}
	// JPEG 魔数 FF D8 FF
	if len(pic.Data) >= 3 && !(pic.Data[0] == 0xFF && pic.Data[1] == 0xD8 && pic.Data[2] == 0xFF) {
		t.Errorf("Picture().Data does not start with JPEG magic: % x", pic.Data[:3])
	}
}

// TestMatroskaWriteUnsupported 校验写入路径对 .mka 返回 ErrUnsupportedWrite（只读，不支持写）。
func TestMatroskaWriteUnsupported(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/out.mka"
	if err := os.WriteFile(path, []byte{0x1A, 0x45, 0xDF, 0xA3}, 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	err := WriteTag(path, WriteOptions{Title: "x"})
	if err == nil {
		t.Fatal("WriteTag(.mka) = nil, want ErrUnsupportedWrite")
	}
	if !strings.Contains(err.Error(), ErrUnsupportedWrite.Error()) {
		t.Errorf("WriteTag(.mka) err = %v, want ErrUnsupportedWrite", err)
	}
}

// TestMatroskaRealSample 用 issue 作者上传的真实样本（/tmp/hey_jude.mka）做端到端读取验证。
// 该样本体积较大不入库；环境中不存在时跳过。
func TestMatroskaRealSample(t *testing.T) {
	const path = "/tmp/hey_jude.mka"
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("real sample not present (%s): %v", path, err)
	}
	defer f.Close()

	m, err := ReadFrom(f)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if got, want := m.Title(), "Hey Jude"; got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
	if got, want := m.Artist(), "The Beatles"; got != want {
		t.Errorf("Artist() = %q, want %q", got, want)
	}
	if got, want := m.Album(), "Hey Jude"; got != want {
		t.Errorf("Album() = %q, want %q", got, want)
	}
	if lyr := m.Lyrics(); lyr == "" {
		t.Error("Lyrics() empty, want non-empty bilingual lyrics")
	}
}
