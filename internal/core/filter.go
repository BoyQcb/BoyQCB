package core

import (
	"math"
	"sort"
	"strings"
)

// FindAds 滑动匹配广告时长指纹，返回命中区间（1-based 片段索引 [start,end]）。
// 对应 PHP find_ads。
func FindAds(segs []Segment, fp []float64, tol float64) [][2]int {
	hits := [][2]int{}
	n := len(fp)
	total := len(segs)
	if n == 0 || n > total {
		return hits
	}
	for i := 0; i+n <= total; i++ {
		ok := true
		for k := 0; k < n; k++ {
			if math.Abs(segs[i+k].Dur-fp[k]) > tol {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, [2]int{segs[i].Idx, segs[i+n-1].Idx})
		}
	}
	return hits
}

// BuildClean 剔除广告片段（含紧邻 DISCONTINUITY 边界行）并绝对化片段地址。
// 返回净化后的 m3u8 文本与被剔除的广告片段数。对应 PHP build_clean。
func BuildClean(lines []string, segs []Segment, hits [][2]int, baseURL string) (string, int) {
	// 片段序号 -> 行号
	lineSeg := map[int]int{}
	idx := 0
	for i, l := range lines {
		s := strings.TrimSpace(l)
		if s != "" && !strings.HasPrefix(s, "#") {
			idx++
			lineSeg[idx] = i
		}
	}

	// 需删除的行集合（广告命中区间 + 入口 DISC：EXTINF/ts 整行 + 区间首段前的 DISC 标签一并删除）
	remove := map[int]bool{}
	for _, h := range hits {
		a, b := h[0], h[1]
		sl, el := lineSeg[a], lineSeg[b]
		// 向前延伸：先跳过命中首段自身的 EXTINF 行（最多一个，EXTINF 与 ts 一一对应），
		// 再吞掉入口所有连续 DISC（DISC-DISC 双 DISC 广告入口两个都删）；
		// 遇到 ts 行（前一段正片）或其他标签即停止，避免吞掉块内/块外正片。
		for sl-1 >= 0 && strings.HasPrefix(strings.TrimSpace(lines[sl-1]), "#EXTINF:") {
			sl--
		}
		for sl-1 >= 0 && strings.HasPrefix(strings.TrimSpace(lines[sl-1]), "#EXT-X-DISCONTINUITY") {
			sl--
		}
		// 向后不延伸：命中区间外的 EXTINF 属于下一段，不得删除（DISC 也保留给下一块）。
		for j := sl; j <= el; j++ {
			remove[j] = true
		}
	}

	// 生成输出：片段地址绝对化
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if remove[i] {
			continue
		}
		s := strings.TrimSpace(l)
		if s != "" && !strings.HasPrefix(s, "#") {
			if !httpRe.MatchString(s) {
				// baseURL 已是目录（Playlist.Base），必须用 AbsFromDir，否则会被多剥一层路径
				s = AbsFromDir(baseURL, s)
			}
			out = append(out, s)
		} else {
			out = append(out, l)
		}
	}

	// 统计被剔除的广告片段数（仅片段行）
	removedSegs := 0
	for _, seg := range segs {
		if ln, ok := lineSeg[seg.Idx]; ok && remove[ln] {
			removedSegs++
		}
	}
	return strings.Join(out, "\n"), removedSegs
}

// FindAdsMulti 按多组指纹依次匹配并合并结果，用于同一站点存在多种广告形态的场景
// （部分资源站每处广告的首尾时长都不同，单一指纹只能命中一处）。
func FindAdsMulti(segs []Segment, fps [][]float64, tol float64) [][2]int {
	all := [][2]int{}
	for _, fp := range fps {
		if len(fp) == 0 {
			continue
		}
		all = append(all, FindAds(segs, fp, tol)...)
	}
	return MergeHits(all)
}

// FindAdsBoundary 滑动匹配广告时长指纹，并要求命中窗口具备「广告边界特征」才保留。
// 模型为「入口锚点 + 指纹连续扩展」：
//  1. 锚点：时长匹配窗口且入口存在显著时长突变（> bdTol），或触及流末尾且入口紧邻 DISCONTINUITY；
//  2. 扩展：从锚点向左右扩展所有「时长指纹连续匹配」的相邻窗口，合并为完整广告块。
//
// 设计动机（真实站点踩坑）：
//  - 部分源站每 5 段就插一个 DISCONTINUITY（常规分块），DISC 不能单独作为边界，否则正片 4s 块全被误删；
//  - 出口突变也不可靠：正片块末尾紧邻过渡段（如 647-651 后接 2.00s 段）会被误放行；
//  - 广告块起点通常在正片块之后有显著时长跳变（如 6.92s→4.00s），以此为锚可区分正片 4s 连续区。
// 用途：结构模式（sequence 等）下正片时长与广告指纹同构时（如正片大量 4.0s 段而广告也是 4.0s 段），
// 普通滑动匹配会把成片正片误删；边界确认要求命中点确实处于插播边界，显著降低误删。
//
// 低区分度指纹（与正片同构、全片匹配窗口数 >=3，如 [4,4,4,4,4] 命中 40 处）要求「入口时长突变 + 入口 DISC」
// 同时成立才作锚点：正片 4s 连续区入口突变≈0，即使被 DISC 包裹也不会命中；而插播广告边界两特征同时成立。
// 高区分度指纹（全片仅 1~2 处匹配，如 [1.92,3.84,7.44]）无需 DISC 佐证，入口时长突变即可，避免漏检唯一广告。
func FindAdsBoundary(segs []Segment, fp []float64, tol float64) [][2]int {
	n := len(fp)
	total := len(segs)
	if n == 0 || n > total {
		return nil
	}
	// 边界时长突变阈值：正片内部自然抖动（如 4.0 vs 3.96 / 4.16 vs 4.0）不算突变，
	// 广告与正片交界处的时长差异（如 6.92 vs 4.0、5.0 vs 4.0）才算；
	// 下限 1.05s：把「入口突变 1.00s」的正片误命中（5.00→4.00）排除在外。
	bdTol := 3 * tol
	if bdTol < 1.05 {
		bdTol = 1.05
	}
	match := func(i int) bool {
		for k := 0; k < n; k++ {
			if math.Abs(segs[i+k].Dur-fp[k]) > tol {
				return false
			}
		}
		return true
	}
	// 0) 指纹区分度：统计全片普通匹配窗口数，判断是否与正片同构
	plainCnt := 0
	for i := 0; i+n <= total; i++ {
		if match(i) {
			plainCnt++
		}
	}
	lowDistinct := plainCnt >= 3
	// 1) 锚点
	anchors := []int{}
	for i := 0; i+n <= total; i++ {
		if !match(i) {
			continue
		}
		if lowDistinct {
			if isStrictAdAnchor(segs, i, n, bdTol) {
				anchors = append(anchors, i)
			}
		} else if isAdAnchor(segs, i, n, bdTol) {
			anchors = append(anchors, i)
		}
	}
	if len(anchors) == 0 {
		return nil
	}
	// 2) 锚点去重，并沿「指纹连续匹配窗口」向左右扩展成完整广告块
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

// isAdAnchor 判断命中窗口 [i, i+n) 是否为广告块锚点：
// 1. 入口时长突变：窗口首段与前一段时长差 > bdTol（广告块起点；正片 4s 连续区内部不满足）；
// 2. 流末尾特例：窗口触及最后一段（片尾广告），入口紧邻 DISCONTINUITY 即保留。
// DISC 本身不作为锚点（常规分块源 DISC 密集），但可作为流末尾特例的佐证。
func isAdAnchor(segs []Segment, i, n int, bdTol float64) bool {
	if i > 0 && math.Abs(segs[i-1].Dur-segs[i].Dur) > bdTol {
		return true // 入口时长突变
	}
	if i+n == len(segs) && i > 0 && segs[i].Disc {
		return true // 流末尾 + 入口 DISC
	}
	return false
}

// isStrictAdAnchor 低区分度指纹的严格锚点：要求「入口时长突变」与「入口 DISC」同时成立。
// 正片与广告时长同构的源站里，正片 4s 连续区入口时长差≈0（即使被 DISC 包裹也不满足突变），
// 只有插播广告边界才会同时具备两个特征，从而把同构正片块（如 420-426）排除。
func isStrictAdAnchor(segs []Segment, i, n int, bdTol float64) bool {
	if i > 0 && math.Abs(segs[i-1].Dur-segs[i].Dur) > bdTol && segs[i].Disc {
		return true // 入口时长突变 + 入口 DISC
	}
	if i+n == len(segs) && i > 0 && segs[i].Disc {
		return true // 流末尾 + 入口 DISC
	}
	return false
}

// countFPMatches 统计指纹在全片的普通匹配窗口数。
// 用于区分指纹与正片的同构程度：>=3 视为「与正片时长同构」（如正片大量 4s 段时
// [4,4,4,4,4] 命中数十处），必须依赖边界锚点防误删；1~2 处为高区分度，
// 时长序列几乎不可能在正片里连续出现，本身就是强信号。
func countFPMatches(segs []Segment, fp []float64, tol float64) int {
	n := len(fp)
	total := len(segs)
	if n == 0 || n > total {
		return 0
	}
	cnt := 0
	for i := 0; i+n <= total; i++ {
		ok := true
		for k := 0; k < n; k++ {
			if math.Abs(segs[i+k].Dur-fp[k]) > tol {
				ok = false
				break
			}
		}
		if ok {
			cnt++
		}
	}
	return cnt
}

// FindAdsMultiBoundary 按多组指纹依次匹配并合并结果，供结构模式补充指纹判据使用。
//
// 高区分度指纹（全片仅 1~2 处匹配）直接普通滑动匹配：这类指纹与正片不可能同构，
// 若仍走 FindAdsBoundary 的锚点确认，会把「入口无时长突变」的插播广告漏删
// （如广告块紧接在 4.0s 正片后、入口突变≈0，且不在流末尾 → 无锚点 → 命中被丢弃），
// 这正是测试页规则学习写入的指纹再次解析仍不命中的根因。
// 低区分度指纹（>=3 处匹配，与正片同构）保留边界确认，避免成片正片被误删。
func FindAdsMultiBoundary(segs []Segment, fps [][]float64, tol float64) [][2]int {
	all := [][2]int{}
	for _, fp := range fps {
		if len(fp) == 0 {
			continue
		}
		if countFPMatches(segs, fp, tol) < 3 {
			all = append(all, FindAds(segs, fp, tol)...)
		} else {
			all = append(all, FindAdsBoundary(segs, fp, tol)...)
		}
	}
	return MergeHits(all)
}

// ApplyKeepProtect 用「保护指纹（正片指纹）」撤销命中区间：
// 若某个待剔除区间内包含任一组保护指纹的连续匹配，则判定为正片误判，放弃剔除。
// 这是测试页把「误判（keep）」标记学习回规则后的生效点，优先级最高。
func ApplyKeepProtect(segs []Segment, site *Site, hits [][2]int, tol float64) [][2]int {
	keeps := site.KeepFPList()
	if len(keeps) == 0 || len(hits) == 0 {
		return hits
	}
	out := make([][2]int, 0, len(hits))
	for _, h := range hits {
		a, b := h[0]-1, h[1]-1 // 段号 1-based → 切片下标
		if a < 0 {
			a = 0
		}
		if b >= len(segs) {
			b = len(segs) - 1
		}
		if a > b {
			continue
		}
		sub := segs[a : b+1]
		blocked := false
		for _, kp := range keeps {
			if len(FindAds(sub, kp, tol)) > 0 {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, h)
		}
	}
	return out
}

// MergeHits 合并重叠或相邻的命中区间并排序
func MergeHits(hits [][2]int) [][2]int {
	if len(hits) == 0 {
		return hits
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i][0] == hits[j][0] {
			return hits[i][1] < hits[j][1]
		}
		return hits[i][0] < hits[j][0]
	})
	out := [][2]int{hits[0]}
	for _, h := range hits[1:] {
		last := &out[len(out)-1]
		if h[0] <= last[1]+1 { // 相邻视为同一段广告
			if h[1] > last[1] {
				last[1] = h[1]
			}
			continue
		}
		out = append(out, h)
	}
	return out
}
