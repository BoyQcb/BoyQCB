package core

// FindAdsByShortBlock 基于「DISCONTINUITY 包裹的孤立短块」识别广告
//
// 适用场景（如优质 yyzy）：整片只有少数几处 DISCONTINUITY，正片块动辄上百片、
// 时长近十分钟，而广告是复用素材的短插播，被单独切成 4~5 片、十几秒的小块。
// 此时 blockcut（依赖时长众数 ≥ 一半 + 尾片截断）与 sequence（依赖路径/序号）
// 均不成立，但「块极短」这一结构特征非常干净。
//
// 判据：
//  1. 全片至少 20 片、且至少 3 个块（否则分块无统计意义）
//  2. 短块 = 片数 ≤ 30 且总时长 ≤ 60s
//  3. 连续短块成段：连续 ≤2 个短块判广告；连续 ≥3 个视为正片分卷，整段跳过
//     （与 slicebatch 同款形态约束，防止把不同转码源的正片分卷误删）
//
// 安全性：全片无短块、或全是短块（说明分片本就很碎）时返回空。
func FindAdsByShortBlock(segs []Segment) [][2]int {
	n := len(segs)
	if n < 20 {
		return nil
	}

	// 1) 按 DISCONTINUITY 分块（Disc 标记表示该片段前有一次 DISCONTINUITY）
	type block struct {
		s, e int     // 0-based 闭区间
		cnt  int     // 片数
		dur  float64 // 块总时长
	}
	blocks := []block{}
	cur := block{s: 0}
	for i, sg := range segs {
		if sg.Disc && i > 0 {
			cur.e = i - 1
			blocks = append(blocks, cur)
			cur = block{s: i}
		}
		cur.cnt++
		cur.dur += sg.Dur
	}
	cur.e = n - 1
	blocks = append(blocks, cur)
	if len(blocks) < 3 {
		return nil
	}

	// 2) 标记短块（末块被视为片尾，不参与广告判定）
	short := make([]bool, len(blocks))
	nShort := 0
	eligible := len(blocks)
	if sbSkipTailBlock {
		eligible-- // 末块排除在判定之外
	}
	for i, b := range blocks {
		if i >= eligible {
			break
		}
		if b.cnt > 0 && b.cnt <= sbShortMaxSegs && b.dur <= sbShortMaxDur {
			short[i] = true
			nShort++
		}
	}
	if nShort == 0 || nShort == eligible {
		return nil // 无短块，或全片都很碎——无法据此判定
	}

	// 3) 连续短块成段，仅孤立短块（≤2 个连在一起）判广告
	hits := [][2]int{}
	for i := 0; i < len(blocks); {
		if !short[i] {
			i++
			continue
		}
		j := i
		for j < len(blocks) && short[j] {
			j++
		}
		if j-i <= sbRunMaxBlocks {
			hits = append(hits, [2]int{blocks[i].s + 1, blocks[j-1].e + 1})
		}
		i = j
	}
	if len(hits) == 0 {
		return nil
	}
	return mergeSpans(hits)
}

// 短块判定阈值
const (
	sbShortMaxSegs = 30   // 短块片数上限
	sbShortMaxDur  = 60.0 // 短块总时长上限（秒）
	sbRunMaxBlocks = 2    // 连续短块判广告的上限，超过则视为正片分卷
	// sbSkipTailBlock：末块视为片尾，不参与广告判定（已关闭）。
	// 实测优质站会在正片末尾再插一次与片中完全相同的广告素材（同指纹），
	// 排除末块会导致该处漏删，因此改为 false，末块同样参与广告判定。
	sbSkipTailBlock = false
)
