// Copyright 2026 songloft contributors.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import "fmt"

// WriteMP4 writes iTunes-style metadata atoms into an M4A/MP4 file.
//
// 现状: 暂未实现。
//
// MP4 是 box 结构容器,iTunes metadata 位于 moov.udta.meta.ilst 下
// (©nam/©ART/©alb/©day/©lyr/covr 等)。修改这些 atoms 会改变 moov 大小,
// 进而需要:
//  1. 重新计算上层 box (moov / udta / meta / ilst) 的 size
//  2. 如果 moov 位于 mdat 之前(常见布局),mdat 偏移会变,
//     必须同步更新 stco/co64 中所有 chunk offset
//  3. 如果文件有 mfra/sidx 等索引 atom,也可能需要更新
//
// 直接自己实现复杂度高,后续计划引入 github.com/abema/go-mp4 处理。
//
// 当前调用方应捕获 ErrUnsupportedWrite,只记 warning 不影响主流程。
func WriteMP4(filePath string, opts WriteOptions) error {
	return fmt.Errorf("%w: mp4/m4a writer is a TODO (need to rebuild moov + update stco)", ErrUnsupportedWrite)
}
