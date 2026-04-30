package tag

import (
	"os"
	"testing"
	"time"
)

func TestMP3VBRDuration(t *testing.T) {
	// 测试 VBR MP3 文件的时长计算
	// 这个测试文件应该有 Xing/LAME 头
	testFiles := []struct {
		name        string
		expectedDur time.Duration
		tolerance   time.Duration
	}{
		{
			name:        "testdata/with_tags/sample.vbr.mp3",
			expectedDur: 250 * time.Second,
			tolerance:   1 * time.Second,
		},
	}

	for _, tc := range testFiles {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.Open(tc.name)
			if err != nil {
				t.Skipf("test file not found: %v", err)
			}
			defer file.Close()

			meta, err := ReadFrom(file)
			if err != nil {
				t.Fatalf("failed to read metadata: %v", err)
			}

			duration := meta.Duration()
			if duration == 0 {
				t.Fatal("duration is zero")
			}

			// 检查时长是否在容忍范围内
			diff := duration - tc.expectedDur
			if diff < 0 {
				diff = -diff
			}
			if diff > tc.tolerance {
				t.Errorf("duration %v differs from expected %v by %v (tolerance: %v)",
					duration, tc.expectedDur, diff, tc.tolerance)
			}

			t.Logf("Duration: %v (%.3f seconds)", duration, duration.Seconds())
		})
	}
}

func TestMP3CBRDuration(t *testing.T) {
	// 测试 CBR MP3 文件的时长计算
	// CBR 文件没有 Xing/LAME 头，应该使用帧大小计算方法

	// 这里我们创建一个模拟的 CBR MP3 数据
	// 实际测试需要使用真实的 CBR MP3 文件
	t.Skip("需要真实的 CBR MP3 测试文件")
}
