package core

import (
	"fmt"
	"math"
	"os"
	"testing"
)

func TestZZVerifyBoundary3(t *testing.T) {
	path := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\test_2000k.m3u8`
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	segs := ParseSegmentsRaw(string(raw))
	fmt.Printf("总段数: %d\n", len(segs))

	// 打印 415-430 与 448-462 区域上下文（确认 420-426 与 454-456 的性质）
	for _, r := range [][2]int{{415, 430}, {448, 462}} {
		fmt.Printf("\n=== 区域 %d-%d ===\n", r[0], r[1])
		for i := r[0]; i <= r[1] && i <= len(segs); i++ {
			s := segs[i-1]
			d := ""
			if s.Disc {
				d = " [DISC]"
			}
			fmt.Printf("  #%d  dur=%.2f%s\n", s.Idx, s.Dur, d)
		}
	}

	// 完整模拟：用户正确标注规则（136-140=[4,4,4,4,4]；454-456=[1.92,3.84,7.44]）
	site := &Site{ID: "t", DetectMode: "sequence", Fingerprint: []float64{4, 4, 4, 4, 4}, Fingerprints: [][]float64{{1.92, 3.84, 7.44}}}
	p := newTestParser()
	hits := p.detectAds(segs, site, "")
	fmt.Printf("\n完整检测命中(正确规则): %v（%d 组/%d 段）\n", hits, len(hits), countSegsH3(hits))

	// 对照组：旧 654-656 指纹
	site2 := &Site{ID: "t", DetectMode: "sequence", Fingerprint: []float64{4, 4, 4, 4, 4}, Fingerprints: [][]float64{{4, 5.48, 2.68}}}
	hits2 := p.detectAds(segs, site2, "")
	fmt.Printf("完整检测命中(旧654指纹): %v（%d 组/%d 段）\n", hits2, len(hits2), countSegsH3(hits2))

	// 边界突变阈值分析：5-9(1.00) 136-140(2.92) 420-426(3.12) 454-456(2.96)
	bd := []struct {
		name string
		tol  float64
	}{
		{"bdTol=0.8(当前)", 0.8}, {"bdTol=1.05", 1.05}, {"bdTol=1.15", 1.15}, {"bdTol=2.0", 2.0},
	}
	for _, b := range bd {
		h := [][2]int{}
		for _, fp := range append([][]float64{{4, 4, 4, 4, 4}}, site.Fingerprints...) {
			h = append(h, findAdsBoundaryWithTol(segs, fp, 0.15, b.tol)...)
		}
		fmt.Printf("  %s: %v\n", b.name, MergeHits(h))
	}

	// 新逻辑：FindAdsBoundary 直接验证（区分度锚点 + bdTol>=1.05）
	fmt.Printf("\n新 FindAdsBoundary 结果:\n")
	for _, fp := range [][]float64{{4, 4, 4, 4, 4}, {1.92, 3.84, 7.44}} {
		fmt.Printf("  指纹 %v: %v\n", fp, FindAdsBoundary(segs, fp, 0.15))
	}
	fmt.Printf("  合并(正确规则): %v\n", MergeHits(append(FindAdsBoundary(segs, []float64{4, 4, 4, 4, 4}, 0.15), FindAdsBoundary(segs, []float64{1.92, 3.84, 7.44}, 0.15)...)))

	// 各命中块入口/出口时长
	for _, r := range [][2]int{{5, 9}, {136, 140}, {420, 426}, {454, 456}} {
		pre, post := "-", "-"
		if r[0] > 1 {
			pre = fmt.Sprintf("%.2f", segs[r[0]-2].Dur)
		}
		if r[1] < len(segs) {
			post = fmt.Sprintf("%.2f", segs[r[1]].Dur)
		}
		fmt.Printf("  [%d-%d] 入口前=%-6s 段1=%.2f 段2=%.2f ... 段n=%.2f 出口后=%s\n",
			r[0], r[1], pre, segs[r[0]-1].Dur, segs[r[0]].Dur, segs[r[1]-1].Dur, post)
	}

	// DISC 分布细节：块首前 DISC（入口）/ 块尾后 DISC（出口）
	// 注意：segs 为 0-based，#n 段 = segs[n-1]
	for _, r := range [][2]int{{5, 9}, {136, 140}, {420, 426}, {454, 456}} {
		if r[0]-1 < 0 || r[0]-1 >= len(segs) || r[1] >= len(segs) {
			continue
		}
		fmt.Printf("  [%d-%d] 入口DISC(#%d前)=%v 出口DISC(#%d前)=%v 首段Disc=%v 尾段Disc=%v\n",
			r[0], r[1], r[0], segs[r[0]-1].Disc, r[1]+1, segs[r[1]].Disc,
			segs[r[0]-1].Disc, segs[r[1]-1].Disc)
	}
}

func findAdsBoundaryWithTol(segs []Segment, fp []float64, tol, bdTol float64) [][2]int {
	n := len(fp)
	total := len(segs)
	if n == 0 || n > total {
		return nil
	}
	match := func(i int) bool {
		for k := 0; k < n; k++ {
			if math.Abs(segs[i+k].Dur-fp[k]) > tol {
				return false
			}
		}
		return true
	}
	anchors := []int{}
	for i := 0; i+n <= total; i++ {
		if !match(i) {
			continue
		}
		if isAdAnchor(segs, i, n, bdTol) {
			anchors = append(anchors, i)
		}
	}
	if len(anchors) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(anchors))
	hits := [][2]int{}
	for _, a := range anchors {
		if seen[a] {
			continue
		}
		seen[a] = true
		lo, hi := a, a+n-1
		for lo > 0 && match(lo-1) {
			lo--
			seen[lo] = true
		}
		for hi+1 < total && match(hi-n+2) {
			hi++
			seen[hi-n+2] = true
		}
		hits = append(hits, [2]int{segs[lo].Idx, segs[hi].Idx})
	}
	return hits
}

func countSegsH3(hits [][2]int) int {
	n := 0
	for _, h := range hits {
		n += h[1] - h[0] + 1
	}
	return n
}
