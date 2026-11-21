package core

import (
	"math"
	"sort"
)

// FindAdsByBlockCut 基于「广告块尾部被截断」特征识别广告
//
// 适用场景（如闪电 sdzy）：广告与正片在同一目录、同一命名规则、时长也同为 2s，
// 序列检测（路径/序号）与时长指纹均无法区分，但广告片总长不是分片时长的整数倍，
// 拼接时最后一片必然被截断（如 1.28s / 0.88s / 0.12s），且广告块整体被
// #EXT-X-DISCONTINUITY 包裹——这是全片唯一可稳定区分的信号。
//
// 判据：
//  1. 分片时长存在明显众数（占比 ≥ 一半），否则放弃
//  2. 找出明显短于众数、且不是全片最后一片的片段（= 广告块尾片）
//  3. 块起点取该片段之前最近一次 DISCONTINUITY 之后的片段
//  4. 块长度限制 2~30 片（约 4~60s），超出判定不可信
//
// 安全性：宁可不删也不错删，特征不成立时返回空。
func FindAdsByBlockCut(segs []Segment) [][2]int {
	n := len(segs)
	if n < 10 {
		return nil
	}

	// 1) 时长众数
	cnt := make(map[float64]int, n/8+1)
	for _, s := range segs {
		if s.Dur <= 0 {
			continue
		}
		k := math.Round(s.Dur*1000) / 1000
		cnt[k]++
	}
	mode, mc := 0.0, 0
	for d, c := range cnt {
		if c > mc || (c == mc && d > mode) {
			mode, mc = d, c
		}
	}
	if mode <= 0 || mc*2 <= n {
		return nil // 时长不统一，无法按截断判定
	}

	// 2) 容差：至少 0.05s，或众数的 2%
	tol := 0.05
	if t := mode * 0.02; t > tol {
		tol = t
	}

	// 3) 定位被截断的块尾片（忽略全片最后一片，它天然可能不满）
	spans := [][2]int{}
	for i := 0; i < n-1; i++ {
		if segs[i].Dur >= mode-tol {
			continue
		}
		start := 0
		for j := i; j >= 0; j-- {
			if segs[j].Disc {
				start = j
				break
			}
		}
		size := i - start + 1
		if size < 2 || size > 30 {
			continue // 块过大，可能是正片正常切割，放弃
		}
		spans = append(spans, [2]int{start + 1, i + 1})
	}
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a][0] < spans[b][0] })
	return mergeSpans(spans)
}
