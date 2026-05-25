// Copyright 2026 mimusic contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import "fmt"

// WriteOGG writes Vorbis Comment metadata to an Ogg Vorbis file.
//
// 现状: 暂未实现。
//
// Ogg 容器中元数据(Vorbis Comment + Setup header) 通常跨越多个 page。
// 修改 Vorbis Comment packet 后需要:
//  1. 重新分包(segment table)
//  2. 重新生成 page 头(包括 granule position、CRC32 等)
//  3. 维护 packet boundary 与 page boundary 的对齐(continued packets 需要 0xff 段)
//
// 网络歌曲很少是 OGG,优先级最低。
//
// 当前调用方应捕获 ErrUnsupportedWrite,只记 warning 不影响主流程。
func WriteOGG(filePath string, opts WriteOptions) error {
	return fmt.Errorf("%w: ogg writer is a TODO (need to re-page packets and recompute CRC)", ErrUnsupportedWrite)
}
