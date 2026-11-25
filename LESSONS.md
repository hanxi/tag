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

## TAG-003：用容器边界与解码结果验证标签回写

- 范围：[songloft-org/songloft#596](https://github.com/songloft-org/songloft/issues/596)，基线 `146a98e`。
- 证据：Ogg 标签跨页后要保留媒体包的内容和位置语义。真实 APEv1 样本升级标签后，FFprobe 最后一个包的范围随尾部标签长度变化，标签之前的 1,477,032 字节和解码 PCM 的 SHA-256 均一致。
- 验证：Ogg Vorbis/Opus 写入超过一页的歌词后再次缩短，用 FFprobe 比较全部音频包。APE 保留未编辑的原始标签项，使用音频区段、PCM 与独立标签读取验证。AIFF/AIFF-C 覆盖音频包与标题回读。
- Seen 1× (2026-11-25)。
