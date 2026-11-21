package core

import (
	"math"
	"sort"
)

// FindAdsByDiscPair 基于「连续双 DISCONTINUITY 入口 + 块内非众数时长」识别插播广告
//
// 适用场景（如无水印 wsyzym3u8.com 系）：正片为均匀切片（如全 2.0s），分卷标记会在
// 正片中间随机插入单个 DISCONTINUITY，而插播广告块统一为：
//
//	#EXT-X-DISCONTINUITY     ← 入口 DISC（连续两行）
//	#EXT-X-DISCONTINUITY
//	#EXTINF:2.033333,
//	...广告片段（约 5~6 段，块内存在偏离众数时长的段）...
//	#EXT-X-DISCONTINUITY     ← 出口 DISC
//
// 实测该站（v13/v14/v15.wsyzym3u8.com、wsyzyts.vip）多集样本：
//   - 广告块一律双 DISC 入口 + 出口单 DISC，块长 5~6 段，块内含 2~3 段偏离众数（2.0s）的时长
//     （如 2.1 / 2.067 / 1.933 / 1.833）；
//   - 正片分卷也会出现双 DISC（每几段一插），但块内全部为众数时长（2.0s）；
//   - 因此「连续双 DISC 入口」与「块内存在非众数时长」必须同时成立才判广告：
//     纯靠双 DISC 会把正片分卷整片误删，纯靠 2s 时长指纹会在正片全片滑动误命中。
//
// 判据：
//  1. 全片分片时长存在明显众数（占比 ≥ 40%），否则放弃；
//  2. 定位双 DISC 入口段（DiscPair），块 = 该段到下一个 DISC 段之前；
//  3. 块长度 2~30 段、总时长 ≤ 120s（广告块通常 5~6 段 / 约 12s）；
//  4. 块内偏离众数（> tol）的片段数 ≥ 2，判定为广告块。
//
// 安全性：正片分卷块（全众数时长，即使双 DISC 入口）不满足第 4 条；片尾不满段（如 0.24s）
// 孤例不满足 ≥2 条，均不会误删。块内全众数时长的广告（与正片同构）由时长指纹/内容哈希兜底。
func FindAdsByDiscPair(segs []Segment) [][2]int {
	n := len(segs)
	if n < 10 {
		return nil
	}

	// 1) 时长众数（与 blockcut 同款量化）
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
	// 众数占比放宽到 40%：广告块含非众数段，占比门槛过高会整体放弃
	if mode <= 0 || mc*5 <= n*2 {
		return nil
	}

	// 2) 容差：至少 0.05s，或众数的 2%
	tol := 0.05
	if t := mode * 0.02; t > tol {
		tol = t
	}

	// 3) 双 DISC 入口段 → 广告块候选
	type span struct{ a, b int } // 段下标（0-based）
	spans := []span{}
	for i := 0; i < n; i++ {
		if !segs[i].DiscPair {
			continue
		}
		// 块结束：下一个 Disc 段之前（含块尾自身的 DISC 归属下一段）
		end := n
		for j := i + 1; j < n; j++ {
			if segs[j].Disc {
				end = j
				break
			}
		}
		spans = append(spans, span{i, end - 1})
	}
	if len(spans) == 0 {
		return nil
	}

	// 4) 块长与时长异常校验
	hits := [][2]int{}
	for _, sp := range spans {
		size := sp.b - sp.a + 1
		if size < 2 || size > 30 {
			continue
		}
		total := 0.0
		off := 0 // 偏离众数的片段数
		for k := sp.a; k <= sp.b; k++ {
			total += segs[k].Dur
			if math.Abs(segs[k].Dur-mode) > tol {
				off++
			}
		}
		if total > 120 || off < 2 {
			continue
		}
		hits = append(hits, [2]int{segs[sp.a].Idx, segs[sp.b].Idx})
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a][0] < hits[b][0] })
	return mergeSpans(hits)
}
