package core

import (
	"fmt"
	"strings"
	"testing"
)

// TestZZDiscRetain 复现用户片段：删除广告块时保留正片前的出口 DISC
func TestZZDiscRetain(t *testing.T) {
	// 用户原始片段：正片 b8e4 -> [DISC A + 5段广告(内部含 DISC B)] -> DISC C -> 正片 63fd/1c3b
	raw := `#EXTINF:4.000,
b8e4bca1c7832de7023c1bec22794b8d.ts
#EXT-X-DISCONTINUITY
#EXTINF:5.567,
2f427501c5121ceba75a3030f5439077.ts
#EXTINF:2.933,
2a86ccb5b3df05fbcf1289fccf8de89a.ts
#EXT-X-DISCONTINUITY
#EXTINF:5.700,
b438089bb68d54cd2d2547b90acc1ef4.ts
#EXTINF:3.333,
4600be313c0d38eb2995553219b454e3.ts
#EXTINF:1.533,
3b1d05c3e44f00cd71834b82e138108e.ts
#EXT-X-DISCONTINUITY
#EXTINF:4.000,
63fda788a15b6b1aa0746187ffd67c86.ts
#EXTINF:4.000,
1c3bd4a1824d628f84e14b7e33d037fd.ts
`
	segs, lines := ParseSegments(raw)
	fmt.Printf("解析段数=%d\n", len(segs))
	for _, s := range segs {
		d := ""
		if s.Disc {
			d = " [DISC]"
		}
		fmt.Printf("  #%d dur=%.3g %s%s\n", s.Idx, s.Dur, s.Name, d)
	}

	// 广告命中 [75-79]：即段 3-7（b8e4=1, 2f42=2, 2a86=3, b438=4, 4600=5, 3b1d=6, 63fd=7, 1c3b=8）
	hits := [][2]int{{2, 6}}
	clean, removed := BuildClean(lines, segs, hits, "http://x/")
	fmt.Printf("removed=%d\n--- clean 输出 ---\n%s\n", removed, clean)

	// 断言：广告段与入口 DISC A、块内 DISC B 删除；出口 DISC C 保留；正片保留
	for _, bad := range []string{"2f427501c5121ceba75a3030f5439077.ts", "2a86ccb5b3df05fbcf1289fccf8de89a.ts", "b438089bb68d54cd2d2547b90acc1ef4.ts", "4600be313c0d38eb2995553219b454e3.ts", "3b1d05c3e44f00cd71834b82e138108e.ts"} {
		if strings.Contains(clean, bad) {
			t.Errorf("广告 ts 未删除: %s", bad)
		}
	}
	discCount := strings.Count(clean, "#EXT-X-DISCONTINUITY")
	if discCount != 1 {
		t.Errorf("期望保留 1 个出口 DISC，实际 %d 个:\n%s", discCount, clean)
	}
	for _, keep := range []string{"b8e4bca1c7832de7023c1bec22794b8d.ts", "63fda788a15b6b1aa0746187ffd67c86.ts", "1c3bd4a1824d628f84e14b7e33d037fd.ts"} {
		if !strings.Contains(clean, keep) {
			t.Errorf("正片 ts 被误删: %s", keep)
		}
	}
	// removed 应=5（仅广告段，不含正片）
	if removed != 5 {
		t.Errorf("期望 removed=5，实际 %d", removed)
	}
}
