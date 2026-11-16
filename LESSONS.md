# 开发经验

## TAG-001：Vorbis 编号既有合写也有拆分格式

- 范围：FLAC/OGG 元数据读取；songloft-org/songloft#571，基线 `64e12e525cc5c9a5ee66826c98dbaed42534af8b`。
- 证据：旧读取器直接 Atoi，`TRACKNUMBER=02/12` 和 `DISCNUMBER=1/2` 读成 0，totaltracks/totaldiscs 别名也丢失。隔离旧 vorbis.go 的回归在 combined、aliases、override 场景失败，日志 `/tmp/songloft-571-negative/vorbis.log`。
- 规则与 Done right：编号先按 number/total 拆分，再使用有效的独立 total 字段覆盖；接受 tracktotal/totaltracks、disctotal/totaldiscs。新读取器及 MP3/FLAC/M4A/OGG/APE/AIFF 碟号写入、旧调用者保留回归通过，`go test ./...` 日志 `/tmp/songloft-571-tag.log`。
- Seen 1× (2026-11-16)。状态：本批修复与验证；最终提交及 CI 见 songloft-org/songloft#571 交付记录，非发布验收。英文版：[LESSONS.en.md](LESSONS.en.md)。
