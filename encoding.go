// Copyright 2025, hanxi
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"io"
	"unicode/utf8"

	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/transform"
)

// fixEncoding 尝试修复可能的编码问题
// 主要处理 GBK/GB2312/GB18030 等中文编码被错误解码为 UTF-8 的情况
func fixEncoding(b []byte) string {
	// 空字节数组直接返回空字符串
	if len(b) == 0 {
		return ""
	}

	// 如果字节数组是纯 ASCII，直接转换为字符串返回
	if isPureASCII(b) {
		return string(b)
	}

	// 如果是有效的 UTF-8 且不包含乱码特征，直接返回
	if isValidUTF8WithoutMojibake(b) {
		return string(b)
	}

	// 尝试修复乱码（GBK 等编码被错误解码为 UTF-8）
	fixed := tryFixChineseEncoding(b)
	if fixed != "" {
		return fixed
	}

	// 如果无法修复，直接转换为字符串（可能包含乱码）
	return string(b)
}

// isPureASCII 检查字节数组是否只包含 ASCII 字符
func isPureASCII(b []byte) bool {
	for _, c := range b {
		if c > 127 {
			return false
		}
	}
	return true
}

// isValidUTF8WithoutMojibake 检查是否是有效的 UTF-8 且不含乱码特征
func isValidUTF8WithoutMojibake(b []byte) bool {
	// 首先检查是否是有效的 UTF-8
	if !utf8.Valid(b) {
		return false
	}

	// 统计 Latin-1 Supplement 区域的字符数量
	latinSupplementCount := 0
	totalRunes := 0

	s := string(b)
	for _, r := range s {
		totalRunes++
		// Latin-1 Supplement 区域的字符 (U+00C0 - U+00FF) 经常出现在乱码中
		if r >= 0x00C0 && r <= 0x00FF {
			latinSupplementCount++
		}
	}

	// 如果 Latin-1 Supplement 字符占比超过 20%，认为是乱码
	if totalRunes > 0 {
		ratio := float64(latinSupplementCount) / float64(totalRunes)
		if ratio > 0.2 {
			return false
		}
	}

	// 检查是否有连续 3 个以上的 Latin-1 Supplement 字符
	consecutiveCount := 0
	maxConsecutive := 0
	for _, r := range s {
		if r >= 0x00C0 && r <= 0x00FF {
			consecutiveCount++
			if consecutiveCount > maxConsecutive {
				maxConsecutive = consecutiveCount
			}
		} else {
			consecutiveCount = 0
		}
	}

	if maxConsecutive >= 3 {
		return false
	}

	return true
}

// tryFixChineseEncoding 尝试修复中文乱码
func tryFixChineseEncoding(b []byte) string {
	// 常见的中文编码列表
	charsets := []string{"GBK", "GB18030", "GB2312"}

	for _, charset := range charsets {
		encodingObj, err := ianaindex.IANA.Encoding(charset)
		if err != nil || encodingObj == nil {
			continue
		}

		// 直接使用原始字节进行解码
		decoded, err := io.ReadAll(transform.NewReader(bytes.NewReader(b), encodingObj.NewDecoder()))
		if err != nil {
			continue
		}

		fixedText := string(decoded)

		// 检查修复后的文本是否包含有效中文字符
		if containsValidChinese(fixedText) {
			return fixedText
		}
	}

	return ""
}

// containsValidChinese 检查文本是否包含有效的中文字符
func containsValidChinese(text string) bool {
	chineseCount := 0
	for _, r := range text {
		// 检查是否是中文字符（基本汉字、扩展A、兼容汉字）
		if (r >= 0x4E00 && r <= 0x9FFF) || // 基本汉字
			(r >= 0x3400 && r <= 0x4DBF) || // 扩展A
			(r >= 0xF900 && r <= 0xFAFF) { // 兼容汉字
			chineseCount++
		}
	}

	// 至少包含一个中文字符
	return chineseCount > 0
}
