package core

import (
	"fmt"
	"strings"
	"testing"
)

// TestBuildClean_DiscBlock 验证「入口 DISC + 块内全部 ts」整块删除模型：
// hits 命中块内 ts 时，删除入口 DISC + 块内全部 EXTINF/ts，保留块尾 DISC（下一块入口）。
func TestBuildClean_DiscBlock(t *testing.T) {
	// 模拟用户给出的测试流片段：正片1 + [DISC + 5个广告ts + DISC] + 正片2
	lines := []string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXTINF:4.000000,",
		"seg_prev_1.ts",
		"#EXTINF:4.000000,",
		"seg_prev_2.ts",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:4.000000,",
		"ad_1.ts",
		"#EXTINF:4.000000,",
		"ad_2.ts",
		"#EXTINF:4.000000,",
		"ad_3.ts",
		"#EXTINF:4.000000,",
		"ad_4.ts",
		"#EXTINF:4.000000,",
		"ad_5.ts",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:4.000000,",
		"seg_next_1.ts",
		"#EXTINF:4.000000,",
		"seg_next_2.ts",
	}
	// 段号从 1 开始：seg_prev_1=1, seg_prev_2=2, ad_1=3, ad_2=4, ad_3=5, ad_4=6, ad_5=7, seg_next_1=8, seg_next_2=9
	segs := []Segment{
		{Idx: 1, Dur: 4}, {Idx: 2, Dur: 4},
		{Idx: 3, Dur: 4}, {Idx: 4, Dur: 4}, {Idx: 5, Dur: 4}, {Idx: 6, Dur: 4}, {Idx: 7, Dur: 4},
		{Idx: 8, Dur: 4}, {Idx: 9, Dur: 4},
	}
	hits := [][2]int{{3, 7}} // 广告块 5 段

	clean, removed := BuildClean(lines, segs, hits, "http://base/")
	fmt.Printf("=== BuildClean 整块删除 ===\nremoved=%d\n--- 输出 ---\n%s\n", removed, clean)

	// 期望：入口 DISC、ad_1..ad_5 的 EXTINF+ts 全部删除；块尾 DISC 与后续正片保留
	for _, bad := range []string{"ad_1.ts", "ad_2.ts", "ad_3.ts", "ad_4.ts", "ad_5.ts"} {
		if strings.Contains(clean, bad) {
			t.Errorf("广告片段 %s 未被删除", bad)
		}
	}
	// 入口 DISC 数量应减少 1（块尾 DISC 保留给下一块）
	discCount := strings.Count(clean, "#EXT-X-DISCONTINUITY")
	if discCount != 1 {
		t.Errorf("期望保留 1 个 DISC（块尾），实际 %d 个:\n%s", discCount, clean)
	}
	for _, keep := range []string{"seg_prev_1.ts", "seg_prev_2.ts", "seg_next_1.ts", "seg_next_2.ts"} {
		if !strings.Contains(clean, keep) {
			t.Errorf("正片片段 %s 被误删", keep)
		}
	}
}
