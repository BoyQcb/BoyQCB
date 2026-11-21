package core

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestZZBatchSiteCheck 批量验证 6 个站点二级 m3u8：
// 1) 解析段数与 DISC 分布；2) 用已有站点规则跑完整检测；
// 3) 无规则的站点输出结构特征（sequence/blockcut 命中）供人工判断。
func TestZZBatchSiteCheck(t *testing.T) {
	dir := `C:\Users\00429\AppData\Roaming\Tencent\Marvis\User\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\workspace\conv_8b99402c55f34d1da889375ab6dab354\temp\sitecheck`
	cases := []struct {
		key  string
		host string
		site *Site
	}{
		{key: "site1", host: "vip.dytt-tvs.com", site: &Site{ID: "dytt", DetectMode: "sequence", Fingerprint: []float64{5.567, 3.2, 5.367, 3.333, 1.6}, Fingerprints: [][]float64{{6.67, 2.13, 3.23, 3.73}}}},
		{key: "site2", host: "play.phimgood.com", site: nil},
		{key: "site3", host: "vip.dytt-see.com", site: &Site{ID: "dytt", DetectMode: "sequence", Fingerprint: []float64{5.567, 3.2, 5.367, 3.333, 1.6}, Fingerprints: [][]float64{{6.67, 2.13, 3.23, 3.73}}}},
		{key: "site4", host: "play.phimgood.com", site: nil},
		{key: "site5", host: "svip.xgplay17.com", site: &Site{ID: "xg", DetectMode: "sequence", Fingerprint: []float64{4.6, 5.533, 0.233}}},
		{key: "site6", host: "svip.xgplay17.com", site: &Site{ID: "xg", DetectMode: "sequence", Fingerprint: []float64{4.6, 5.533, 0.233}}},
	}
	p := newTestParser()
	for _, c := range cases {
		path := fmt.Sprintf(`%s\%s-level2.m3u8`, dir, c.key)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s 读取失败: %v", c.key, err)
		}
		content := string(raw)
		segs, lines := ParseSegments(content)
		disc := 0
		for _, s := range segs {
			if s.Disc {
				disc++
			}
		}
		fmt.Printf("\n########## %s | %s ##########\n", c.key, c.host)
		fmt.Printf("总段数=%d 总行数=%d DISC数=%d\n", len(segs), len(lines), disc)

		if c.site == nil {
			// 无规则站点：结构特征分析
			seqHits := FindAdsBySequence(segs)
			bcHits := FindAdsByBlockCut(segs)
			fmt.Printf("  [无规则] sequence命中=%v  blockcut命中=%v\n", seqHits, bcHits)
			// 时长众数统计
			fmt.Printf("  [无规则] 时长分布前20: %v\n", topDurStats(segs, 20))
			// DISC 位置（段号）
			var discPos []int
			for _, s := range segs {
				if s.Disc {
					discPos = append(discPos, s.Idx)
				}
			}
			fmt.Printf("  [无规则] DISC位置: %v\n", discPos)
			continue
		}

		hits := p.detectAds(segs, c.site, "")
		fmt.Printf("  检测命中: %v（%d 组/%d 段）\n", hits, len(hits), countSegsH3(hits))
		if len(hits) == 0 {
			fmt.Printf("  无广告命中，clean 输出=原样\n")
			continue
		}
		clean, removed := BuildClean(lines, segs, hits, "http://"+c.host+"/")
		fmt.Printf("  BuildClean removed=%d\n", removed)
		// 检查 clean 后剩余段数
		cleanSegs, _ := ParseSegments(clean)
		fmt.Printf("  clean 后剩余段数=%d\n", len(cleanSegs))
		// 检查删除后是否残留悬挂 DISCONTINUITY 或连续 DISC
		fmt.Printf("  clean DISC数=%d（原 %d）\n", countDisc(clean), disc)
		// 命中区间上下文打印
		for _, r := range hits {
			printRangeContext(segs, r)
		}
	}
}

func countDisc(content string) int {
	return strings.Count(content, "#EXT-X-DISCONTINUITY")
}

func topDurStats(segs []Segment, n int) []string {
	m := map[float64]int{}
	for _, s := range segs {
		m[s.Dur]++
	}
	type kv struct {
		k float64
		v int
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		arr = append(arr, kv{k, v})
	}
	for i := 0; i < len(arr); i++ {
		for j := i + 1; j < len(arr); j++ {
			if arr[j].v > arr[i].v || (arr[j].v == arr[i].v && arr[j].k < arr[i].k) {
				arr[i], arr[j] = arr[j], arr[i]
			}
		}
	}
	if len(arr) > n {
		arr = arr[:n]
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		out = append(out, fmt.Sprintf("%.3g×%d", e.k, e.v))
	}
	return out
}

func printRangeContext(segs []Segment, r [2]int) {
	lo := r[0] - 3
	if lo < 1 {
		lo = 1
	}
	hi := r[1] + 3
	if hi > len(segs) {
		hi = len(segs)
	}
	var sb strings.Builder
	for i := lo; i <= hi; i++ {
		s := segs[i-1]
		d := "  "
		if s.Disc {
			d = "D "
		}
		mark := "   "
		if i >= r[0] && i <= r[1] {
			mark = ">> "
		}
		sb.WriteString(fmt.Sprintf("  %s%s#%d dur=%.3g\n", mark, d, s.Idx, s.Dur))
	}
	fmt.Printf("  区间 [%d-%d] 上下文:\n%s", r[0], r[1], sb.String())
}
