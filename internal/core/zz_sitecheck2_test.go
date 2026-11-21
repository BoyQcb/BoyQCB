package core

import (
	"fmt"
	"os"
	"testing"
)

// TestZZSiteCheckPhimgood 用 dytt 指纹验证 phimgood 站点的广告区间
func TestZZSiteCheckPhimgood(t *testing.T) {
	dir := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\sitecheck`
	cases := []struct {
		key string
	}{
		{key: "site2"},
		{key: "site4"},
	}
	p := newTestParser()
	// dytt 规则指纹（主+多组）
	dyttSite := &Site{ID: "dytt", DetectMode: "sequence", Fingerprint: []float64{5.567, 3.2, 5.367, 3.333, 1.6}, Fingerprints: [][]float64{{6.67, 2.13, 3.23, 3.73}}}
	for _, c := range cases {
		path := fmt.Sprintf(`%s\%s-level2.m3u8`, dir, c.key)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		segs, lines := ParseSegments(string(raw))
		fmt.Printf("\n########## %s (phimgood) 段数=%d ##########\n", c.key, len(segs))
		hits := p.detectAds(segs, dyttSite, "")
		fmt.Printf("用dytt规则检测命中: %v（%d 段）\n", hits, countSegsH3(hits))
		if len(hits) > 0 {
			clean, removed := BuildClean(lines, segs, hits, "http://x/")
			fmt.Printf("BuildClean removed=%d\n", removed)
			cleanSegs, _ := ParseSegments(clean)
			fmt.Printf("clean 后剩余段数=%d\n", len(cleanSegs))
		}
		for _, r := range hits {
			printRangeContext(segs, r)
		}
	}
}

// TestZZSiteCheckXG 检查 xg 站点 DISC 密集分布与时长指纹特征
func TestZZSiteCheckXG(t *testing.T) {
	dir := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\sitecheck`
	for _, c := range []struct{ key string }{{"site5"}, {"site6"}} {
		path := fmt.Sprintf(`%s\%s-level2.m3u8`, dir, c.key)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		segs, _ := ParseSegments(string(raw))
		fmt.Printf("\n########## %s (xgplay) 段数=%d ##########\n", c.key, len(segs))
		// DISC 间隔分布
		var discPos []int
		for _, s := range segs {
			if s.Disc {
				discPos = append(discPos, s.Idx)
			}
		}
		gaps := make(map[int]int)
		for i := 1; i < len(discPos); i++ {
			g := discPos[i] - discPos[i-1]
			gaps[g]++
		}
		fmt.Printf("DISC数=%d 间隔分布: %v\n", len(discPos), gaps)
		fmt.Printf("时长分布前15: %v\n", topDurStats(segs, 15))
		// xg 指纹 4.6/5.533/0.233 检测
		p := newTestParser()
		xgSite := &Site{ID: "xg", DetectMode: "sequence", Fingerprint: []float64{4.6, 5.533, 0.233}}
		hits := p.detectAds(segs, xgSite, "")
		fmt.Printf("用xg规则检测命中: %v（%d 段）\n", hits, countSegsH3(hits))
	}
}
