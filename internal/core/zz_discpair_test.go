package core

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// buildSegs 由时长序列构造 Segment（Disc 由调用方补标）
func buildSegs(durs []float64) []Segment {
	segs := make([]Segment, len(durs))
	for i, d := range durs {
		segs[i] = Segment{Idx: i + 1, Dur: d, Name: fmt.Sprintf("seg_%04d.ts", i+1)}
	}
	return segs
}

// TestFindAdsByDiscPair_Synthetic 合成流验证：
// 正片全 2.0s，正片分卷双 DISC 入口但全众数时长（不判广告），
// 广告块双 DISC 入口 + 块内 2 段非众数（判广告）。
func TestFindAdsByDiscPair_Synthetic(t *testing.T) {
	segs := buildSegs([]float64{
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2, // 1-10 正片
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2, // 11-20 正片
		2.1, 2, 2, 1.93, 2, // 21-25 广告块（双 DISC 入口）
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2, // 26-35 正片
	})
	// 正片分卷：段11 前单 DISC；广告块：段21 前双 DISC；段26 前单 DISC（出口）
	for i := range segs {
		switch i {
		case 10: // 段11 前单 DISC
			segs[i].Disc = true
		case 20: // 段21 前双 DISC
			segs[i].Disc = true
			segs[i].DiscPair = true
		case 25: // 段26 前单 DISC
			segs[i].Disc = true
		}
	}
	hits := FindAdsByDiscPair(segs)
	if len(hits) != 1 || hits[0][0] != 21 || hits[0][1] != 25 {
		t.Fatalf("期望命中 [21,25]，实际 %v", hits)
	}
}

// TestFindAdsByDiscPair_PositiveBlock 正片分卷块双 DISC 但全众数，不得误删
func TestFindAdsByDiscPair_PositiveBlock(t *testing.T) {
	segs := buildSegs([]float64{
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2, // 1-10
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2, // 11-20
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2, // 21-30
	})
	// 段11、段21 前双 DISC（正片分卷），无广告
	for i := range segs {
		if i == 10 || i == 20 {
			segs[i].Disc = true
			segs[i].DiscPair = true
		}
	}
	hits := FindAdsByDiscPair(segs)
	if len(hits) != 0 {
		t.Fatalf("正片分卷被误判为广告: %v", hits)
	}
}

// TestFindAdsByDiscPair_TailPartial 片尾不满段（0.24s）孤例不得触发
func TestFindAdsByDiscPair_TailPartial(t *testing.T) {
	segs := buildSegs([]float64{
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 2,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 0.24, // 末段片尾
	})
	// 段21 前双 DISC（分卷），末块含 0.24 孤例
	for i := range segs {
		if i == 20 {
			segs[i].Disc = true
			segs[i].DiscPair = true
		}
	}
	hits := FindAdsByDiscPair(segs)
	if len(hits) != 0 {
		t.Fatalf("片尾孤例被误判为广告: %v", hits)
	}
}

// TestBuildClean_DiscPair 双 DISC 入口的广告块，入口两个 DISC 都应删除
func TestBuildClean_DiscPair(t *testing.T) {
	lines := []string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXTINF:2.000000,",
		"seg_prev_1.ts",
		"#EXTINF:2.000000,",
		"seg_prev_2.ts",
		"#EXT-X-DISCONTINUITY",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:2.100000,",
		"ad_1.ts",
		"#EXTINF:2.000000,",
		"ad_2.ts",
		"#EXTINF:1.933333,",
		"ad_3.ts",
		"#EXT-X-DISCONTINUITY",
		"#EXTINF:2.000000,",
		"seg_next_1.ts",
		"#EXTINF:2.000000,",
		"seg_next_2.ts",
	}
	segs := []Segment{
		{Idx: 1, Dur: 2}, {Idx: 2, Dur: 2},
		{Idx: 3, Dur: 2.1, Disc: true, DiscPair: true},
		{Idx: 4, Dur: 2},
		{Idx: 5, Dur: 1.933},
		{Idx: 6, Dur: 2, Disc: true},
		{Idx: 7, Dur: 2}, {Idx: 8, Dur: 2},
	}
	hits := [][2]int{{3, 5}}
	clean, removed := BuildClean(lines, segs, hits, "http://base/")
	fmt.Printf("=== BuildClean 双DISC入口 ===\nremoved=%d\n--- 输出 ---\n%s\n", removed, clean)
	// 广告行全部删除，入口两个 DISC 都删，保留块尾 DISC 与正片
	for _, bad := range []string{"ad_1.ts", "ad_2.ts", "ad_3.ts"} {
		if strings.Contains(clean, bad) {
			t.Errorf("广告片段 %s 未被删除", bad)
		}
	}
	// 应保留 1 个 DISC（块尾）+ 0 个残留入口 DISC
	if got := strings.Count(clean, "#EXT-X-DISCONTINUITY"); got != 1 {
		t.Errorf("期望保留 1 个 DISC（块尾），实际 %d 个:\n%s", got, clean)
	}
	for _, keep := range []string{"seg_prev_1.ts", "seg_prev_2.ts", "seg_next_1.ts", "seg_next_2.ts"} {
		if !strings.Contains(clean, keep) {
			t.Errorf("正片片段 %s 被误删", keep)
		}
	}
}

// TestFindAdsByDiscPair_RealSamples 真实样本回归：
// 用已抓取的 wsyzym 站 5 集媒体列表验证广告块命中（样本文件放 temp 目录）
func TestFindAdsByDiscPair_RealSamples(t *testing.T) {
	samples := []struct {
		file   string
		want   [][2]int // 期望广告区间（段号 1-based）
		discNr int      // 双 DISC 入口总数
	}{
		{"ep01.m3u8", [][2]int{{140, 145}}, 5},
		{"ep02.m3u8", nil, 5},
		{"ep03.m3u8", [][2]int{{142, 147}}, 11},
		{"ep05.m3u8", [][2]int{{157, 162}}, 11},
		{"test_ep04_media.m3u8", [][2]int{{157, 162}}, 8},
	}
	dir := "C:\\Users\\00429\\AppData\\Roaming\\Tencent\\Marvis\\User\\oAN1i2SK4ZPINuDQR_mXpaFUGrT4\\workspace\\conv_bcfbc1e6aa1d4ee9b344d24cd1e755fc\\temp"
	for _, sp := range samples {
		raw, err := os.ReadFile(dir + "\\" + sp.file)
		if err != nil {
			t.Skipf("样本 %s 不存在: %v", sp.file, err)
			continue
		}
		segs, _ := ParseSegments(string(raw))
		// 双 DISC 入口统计（ParseSegments 的 DiscPair）
		cnt := 0
		for _, s := range segs {
			if s.DiscPair {
				cnt++
			}
		}
		if cnt != sp.discNr {
			t.Errorf("%s: 双DISC入口数期望 %d 实际 %d", sp.file, sp.discNr, cnt)
		}
		hits := FindAdsByDiscPair(segs)
		fmt.Printf("=== %s 双DISC入口=%d 命中=%v ===\n", sp.file, cnt, hits)
		if len(sp.want) == 0 {
			if len(hits) != 0 {
				t.Errorf("%s: 期望无广告命中，实际 %v", sp.file, hits)
			}
			continue
		}
		if len(hits) != 1 || hits[0] != sp.want[0] {
			t.Errorf("%s: 期望命中 %v，实际 %v", sp.file, sp.want, hits)
		}
	}
}
