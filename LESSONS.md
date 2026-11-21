# 开发经验

## TAG-001：Vorbis 编号既有合写也有拆分格式

- 范围：FLAC/OGG 元数据读取；songloft-org/songloft#571，基线 `64e12e525cc5c9a5ee66826c98dbaed42534af8b`。
- 证据：旧读取器直接 Atoi，`TRACKNUMBER=02/12` 和 `DISCNUMBER=1/2` 读成 0，totaltracks/totaldiscs 别名也丢失。隔离旧 vorbis.go 的回归在 combined、aliases、override 场景失败，日志 `/tmp/songloft-571-negative/vorbis.log`。
- 规则与 Done right：编号先按 number/total 拆分，再使用有效的独立 total 字段覆盖；接受 tracktotal/totaltracks、disctotal/totaldiscs。新读取器及 MP3/FLAC/M4A/OGG/APE/AIFF 碟号写入、旧调用者保留回归通过，`go test ./...` 日志 `/tmp/songloft-571-tag.log`。
- Seen 1× (2026-11-16)。状态：本批修复与验证；最终提交及 CI 见 songloft-org/songloft#571 交付记录，非发布验收。英文版：[LESSONS.en.md](LESSONS.en.md)。

## TAG-002：Matroska 索引位置扩宽时复用预留空间

- 范围：Matroska 增量标签写回，基线 `efb3ff6022919b6b0e6b3162fbfa9f7402806a6a`。
- 一份含视频、两条音轨、两个章节和文本附件的生成 MKV 采用较窄的 SeekPosition 指向旧 Tags。追加新标签后，位置超出了原字段宽度。
- 复用 SeekHead 后的 Void 扩宽位置字段，保持索引起点与媒体偏移，重算索引 CRC。FFprobe 复读确认音视频包哈希与拖动结果一致，章节和第二音轨保留，旧附件与新封面可读取。
- Seen 1× (2026-11-21)。
