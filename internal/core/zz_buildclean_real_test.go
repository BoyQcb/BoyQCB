package core

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestZZBuildCleanReal 用真实测试流验证整块删除模型：
// 广告块（136-140、454-456）应连同入口 DISC 一起删除；正片块（5-9、420-426）不得误删。
func TestZZBuildCleanReal(t *testing.T) {
	path := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\test_2000k.m3u8`
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	segs, lines := ParseSegments(string(raw))
	fmt.Printf("总段数: %d, 总行数: %d\n", len(segs), len(lines))

	site := &Site{ID: "t", DetectMode: "sequence", Fingerprint: []float64{4, 4, 4, 4, 4}, Fingerprints: [][]float64{{1.92, 3.84, 7.44}}}
	p := newTestParser()
	hits := p.detectAds(segs, site, "")
	fmt.Printf("完整检测命中: %v（%d 组/%d 段）\n", hits, len(hits), countSegsH3(hits))

	clean, removed := BuildClean(lines, segs, hits, "http://base/")
	fmt.Printf("removed=%d\n", removed)

	// 输出到中间产物目录供人工核对
	out := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\clean_2000k.m3u8`
	os.WriteFile(out, []byte(clean), 0644)
	fmt.Printf("clean 输出: %s\n", out)

	// 广告 ts（136-140、454-456）必须被删除
	segsRaw := ParseSegmentsRaw(string(raw))
	var adNames []string
	var keepNames []string
	for _, r := range [][2]int{{136, 140}, {454, 456}} {
		for i := r[0]; i <= r[1]; i++ {
			adNames = append(adNames, segsRaw[i-1].Name)
		}
	}
	// 正片 ts（5-9、420-426）不得被删
	for _, r := range [][2]int{{5, 9}, {420, 426}} {
		for i := r[0]; i <= r[1]; i++ {
			keepNames = append(keepNames, segsRaw[i-1].Name)
		}
	}
	for _, u := range adNames {
		if strings.Contains(clean, u) {
			t.Errorf("广告 ts 未被删除: %s", u)
		}
	}
	for _, u := range keepNames {
		if !strings.Contains(clean, u) {
			t.Errorf("正片 ts 被误删: %s", u)
		}
	}
	// 入口 DISC 应随广告块一起删除：2 组广告各带走 1 个入口 DISC
	origDisc := strings.Count(string(raw), "#EXT-X-DISCONTINUITY")
	cleanDisc := strings.Count(clean, "#EXT-X-DISCONTINUITY")
	if origDisc-cleanDisc != 2 {
		t.Errorf("期望删除 2 个入口 DISC，实际删除 %d 个（原始 %d -> clean %d）", origDisc-cleanDisc, origDisc, cleanDisc)
	}
	// removed 应等于广告段数（8），不含正片
	if removed != 8 {
		t.Errorf("期望 removed=8（2 组广告 8 段），实际 %d", removed)
	}
}
