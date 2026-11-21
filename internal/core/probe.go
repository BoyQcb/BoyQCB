package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProbeCandidate 一个可疑广告区间
type ProbeCandidate struct {
	Start         int       `json:"start"`          // 起始片段序号（1-based）
	End           int       `json:"end"`            // 结束片段序号（1-based）
	Segs          int       `json:"segs"`           // 片段数
	Dur           float64   `json:"dur"`            // 区间总时长（秒）
	Pattern       []float64 `json:"pattern"`        // 时长序列（可直接作为特征码）
	Score         float64   `json:"score"`          // 可信度评分
	Reasons       []string  `json:"reasons"`        // 命中依据
	RepeatCount   int       `json:"repeat_count"`   // 相同模式出现次数
	HashConfirmed bool      `json:"hash_confirmed"` // 是否经内容哈希确认
	SeqHit        bool      `json:"seq_hit"`        // 是否由 TS 序列连贯性检出（最高可信度）
}

// ProbeSuggest 依据探测结果给出的建站建议
type ProbeSuggest struct {
	Name            string      `json:"name"`
	URLPrefix       string      `json:"url_prefix"`
	URLRegex        string      `json:"url_regex"`
	URLPrefixes     []string    `json:"url_prefixes,omitempty"`
	URLRegexes      []string    `json:"url_regexes,omitempty"`
	CacheSubdir     string      `json:"cache_subdir"`
	ParseType       string      `json:"parse_type"`
	DetectMode      string      `json:"detect_mode"`
	Fingerprint     []float64   `json:"fingerprint"`
	FingerprintSets [][]float64 `json:"fingerprint_sets"`
	IndexMode       string      `json:"index_mode"`
	StreamPick      string      `json:"stream_pick"`
	FixedSub        string      `json:"fixed_sub"`
	Host            string      `json:"host"`
	Note            string      `json:"note"`
}

// ProbeResult 探测结果
type ProbeResult struct {
	Ok           bool             `json:"ok"`
	Msg          string           `json:"msg"`
	InputURL     string           `json:"input_url"`
	Host         string           `json:"host"`
	IsMaster     bool             `json:"is_master"`
	IndexMode    string           `json:"index_mode"`
	Streams      []StreamInfo     `json:"streams"`
	PickedURI    string           `json:"picked_uri"`
	PlaylistURL  string           `json:"playlist_url"`
	BaseURL      string           `json:"base_url"`
	TotalSegs    int              `json:"total_segs"`
	TotalDur     float64          `json:"total_dur"`
	DiscCount    int              `json:"disc_count"`
	DurHistogram []DurStat        `json:"dur_histogram"`
	Candidates   []ProbeCandidate `json:"candidates"`
	Suggest      ProbeSuggest     `json:"suggest"`
	HashUsed     bool             `json:"hash_used"`
	HashOK       int              `json:"hash_ok"`
	ElapsedMs    int64            `json:"elapsed_ms"`
}

// DurStat 时长分布项
type DurStat struct {
	Dur   float64 `json:"dur"`
	Count int     `json:"count"`
}

type cand struct {
	start, end     int
	score          float64
	reasons        map[string]bool
	repeat         int
	hashConfirmed  bool
	seqHit         bool
	durSum         float64
	pattern        []float64
	hashCandidates [][2]int
}

func (c *cand) add(reason string, score float64) {
	if c.reasons == nil {
		c.reasons = map[string]bool{}
	}
	if !c.reasons[reason] {
		c.reasons[reason] = true
		c.score += score
	}
}

// Probe 对一个 m3u8 地址做结构与广告探测，用于新增站点向导。
// hashCheck=true 时会对片段做采样哈希比对（能发现无时长特征的广告，但较慢）。
func Probe(client *http.Client, rawURL, ua string, timeout time.Duration,
	mode, pick, fixedSub string, hashCheck bool, sampleSize, hashConcurrency int) *ProbeResult {

	start := time.Now()
	res := &ProbeResult{Ok: false, InputURL: rawURL, ElapsedMs: 0}

	rawURL = strings.TrimSpace(rawURL)
	if !isHTTPLink(rawURL) {
		res.Msg = "地址必须为 http(s) 链接"
		return res
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		res.Msg = "地址解析失败"
		return res
	}
	res.Host = strings.ToLower(u.Hostname())

	pl, err := ResolvePlaylistOpt(client, rawURL, ua, timeout, mode, pick, fixedSub)
	if err != nil {
		res.Msg = "抓取失败：" + err.Error()
		return res
	}
	res.IsMaster = pl.Master
	res.Streams = pl.Streams
	if pl.Picked != nil {
		res.PickedURI = pl.Picked.URI
	}
	res.PlaylistURL = pl.URL
	res.BaseURL = pl.Base
	if pl.IsSingle {
		res.IndexMode = "single"
	} else {
		res.IndexMode = "auto"
	}

	segs := ParseSegmentsRaw(pl.Content)
	if len(segs) == 0 {
		res.Msg = "未解析到任何视频片段，请确认该地址可访问且为标准 m3u8"
		return res
	}
	res.TotalSegs = len(segs)
	for _, s := range segs {
		res.TotalDur += s.Dur
		if s.Disc {
			res.DiscCount++
		}
	}
	res.DurHistogram = durHistogram(segs, 8)
	res.TotalDur = math.Round(res.TotalDur*100) / 100

	// 1) 时长维度探测
	cands := detectByDuration(segs)

	// 2) 片段路径异常检测（广告片段常来自与正片不同的目录/命名）
	uriBase := res.BaseURL
	if uriBase == "" {
		uriBase = rawURL
	}
	cands = append(cands, detectByURI(segs, uriBase)...)

	// 3) 内容哈希探测（可选，慢）
	res.HashUsed = hashCheck
	if hashCheck {
		hc, okCnt := detectByHash(client, segs, pl.Base, ua, sampleSize, hashConcurrency, timeout)
		res.HashOK = okCnt
		if len(hc) > 0 {
			cands = append(cands, hc...)
		}
	}

	// 4) TS 序列连贯性探测（广告插播的 TS 与正片不连贯，最可靠的判据）
	//    序列区间本身就是精确边界，不参与 merge，避免被邻近候选撑大
	seqCands := detectBySequence(segs)
	cands = append(mergeCands(cands), seqCands...)
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	limit := 8
	if len(cands) > limit {
		cands = cands[:limit]
	}
	const maxFP = 40
	for _, c := range cands {
		rs := make([]string, 0, len(c.reasons))
		for k := range c.reasons {
			rs = append(rs, k)
		}
		sort.Strings(rs)
		s, e := c.start-1, c.end
		if s < 0 {
			s = 0
		}
		if e > len(segs) {
			e = len(segs)
		}
		sum := 0.0
		pat := make([]float64, 0, e-s)
		for k := s; k < e; k++ {
			sum += segs[k].Dur
			if len(pat) < maxFP {
				pat = append(pat, round2(segs[k].Dur))
			}
		}
		res.Candidates = append(res.Candidates, ProbeCandidate{
			Start: c.start, End: c.end, Segs: e - s,
			Dur: round2(sum), Pattern: pat,
			Score: round2(c.score), Reasons: rs,
			RepeatCount: c.repeat, HashConfirmed: c.hashConfirmed, SeqHit: c.seqHit,
		})
	}

	res.Suggest = buildSuggest(res, segs, pick)
	res.Ok = true
	res.Msg = "探测完成"
	res.ElapsedMs = time.Since(start).Milliseconds()
	return res
}

// durHistogram 时长分布统计（取出现次数最多的前 N 种）
func durHistogram(segs []Segment, topN int) []DurStat {
	m := map[float64]int{}
	for _, s := range segs {
		m[math.Round(s.Dur*100)/100]++
	}
	list := make([]DurStat, 0, len(m))
	for d, c := range m {
		list = append(list, DurStat{Dur: d, Count: c})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Count > list[j].Count })
	if len(list) > topN {
		list = list[:topN]
	}
	return list
}

// modeDuration 全片时长的众数（正片通常高度规整）
func modeDuration(segs []Segment) float64 {
	m := map[float64]int{}
	for _, s := range segs {
		m[math.Round(s.Dur*100)/100]++
	}
	best, bn := 0.0, 0
	for d, c := range m {
		if c > bn {
			best, bn = d, c
		}
	}
	return best
}

// detectByDuration 时长维度探测：以“正片时长集合”为基准找连续异常段，
// 再按重复出现次数、DISCONTINUITY 包裹等证据打分。
func detectByDuration(segs []Segment) []cand {
	out := []cand{}
	n := len(segs)
	if n < 6 {
		return out
	}
	durs := make([]float64, n)
	freq := map[float64]int{}
	for i, s := range segs {
		d := round2(s.Dur)
		durs[i] = d
		freq[d]++
	}
	mode := modeDuration(segs)
	// 正片时长：出现占比 >= 3% 的时长，或与众数足够接近
	isNormal := func(d float64) bool {
		if freq[d]*100/n >= 3 {
			return true
		}
		return math.Abs(d-mode) <= math.Max(0.08, mode*0.15)
	}

	const minSegs, maxSegs = 2, 40
	const minDur, maxDur = 2.0, 180.0

	for i := 0; i < n; {
		if isNormal(durs[i]) {
			i++
			continue
		}
		j := i
		for j+1 < n && !isNormal(durs[j+1]) {
			j++
		}
		cnt := j - i + 1
		if cnt >= minSegs && cnt <= maxSegs {
			sum := sumDur(segs[i : j+1])
			if sum >= minDur && sum <= maxDur {
				lo, hi := durs[i], durs[i]
				for k := i; k <= j; k++ {
					if durs[k] < lo {
						lo = durs[k]
					}
					if durs[k] > hi {
						hi = durs[k]
					}
				}
				// 区间越短越可能是正片抖动，按长度降权
				lengthFactor := 1.0
				switch {
				case cnt >= 4:
					lengthFactor = 1.0
				case cnt == 3:
					lengthFactor = 0.7
				default:
					lengthFactor = 0.4
				}
				c := cand{start: segs[i].Idx, end: segs[j].Idx}
				base := float64(cnt)*4 + math.Min(20, sum/5)
				c.add(fmt.Sprintf("连续%d段时长异常（%.2f~%.2fs，正片众数%.2fs）", cnt, lo, hi, mode), base*lengthFactor)
				if rep := countPattern(durs, i, cnt); rep >= 2 {
					c.repeat = rep
					c.add(fmt.Sprintf("相同序列在全片出现%d次", rep), float64(rep-1)*15*lengthFactor)
				}
				if wrappedByDisc(segs, i, j) {
					c.add("被 DISCONTINUITY 包裹", 15*lengthFactor)
				}
				out = append(out, c)
			}
		}
		i = j + 1
	}
	return out
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// countPattern 统计 durs[s:s+L] 这段序列在全片出现的次数
func countPattern(durs []float64, s, L int) int {
	if L <= 0 || s < 0 || s+L > len(durs) || L > 64 {
		return 0
	}
	cnt := 0
	for k := 0; k+L <= len(durs); k++ {
		eq := true
		for x := 0; x < L; x++ {
			if durs[k+x] != durs[s+x] {
				eq = false
				break
			}
		}
		if eq {
			cnt++
		}
	}
	return cnt
}

// wrappedByDisc 区间是否被 DISCONTINUITY 前后包裹
func wrappedByDisc(segs []Segment, i, j int) bool {
	if !segs[i].Disc {
		return false
	}
	return j+1 < len(segs) && segs[j+1].Disc
}

// detectByHash 内容哈希探测：采样片段前 sampleSize 字节，找连续相同的片段序列。
// 同一段广告在片中被插入多次时，其片段内容哈希完全一致，可据此定位。
func detectByHash(client *http.Client, segs []Segment, baseURL, ua string,
	sampleSize, concurrency int, timeout time.Duration) ([]cand, int) {

	if sampleSize <= 0 {
		sampleSize = 4096
	}
	if concurrency <= 0 {
		concurrency = 12
	}
	n := len(segs)
	hashes := make([]string, n)
	sem := make(chan struct{}, concurrency)
	_ = sem
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			u := AbsFromDir(baseURL, segs[i].Name)
			data, err := FetchRange(client, u, ua, sampleSize, timeout)
			if err != nil || len(data) == 0 {
				return
			}
			sum := sha256.Sum256(data)
			hashes[i] = hex.EncodeToString(sum[:])[:16]
		}(i)
	}
	wg.Wait()

	pos := map[string][]int{}
	okCnt := 0
	for i, h := range hashes {
		if h != "" {
			pos[h] = append(pos[h], i)
			okCnt++
		}
	}
	out := []cand{}
	marked := make([]bool, n)
	minLen := 4
	for i := 0; i < n; i++ {
		if hashes[i] == "" || marked[i] {
			continue
		}
		for _, j := range pos[hashes[i]] {
			if j <= i {
				continue
			}
			L := 0
			for i+L < n && j+L < n && hashes[i+L] != "" && hashes[i+L] == hashes[j+L] {
				L++
			}
			if L < minLen {
				continue
			}
			for k := 0; k < L; k++ {
				marked[i+k] = true
				marked[j+k] = true
			}
			c1 := cand{start: segs[i].Idx, end: segs[i+L-1].Idx, hashConfirmed: true, repeat: 2}
			c1.add(fmt.Sprintf("内容哈希连续%d段完全一致", L), 60)
			out = append(out, c1)
			break
		}
	}
	return out, okCnt
}

// detectBySequence 序列连贯性探测：正片 TS 是一条按序号（或路径）连贯的链，
// 广告插播的 TS 会打断这条链。这是目前判别广告最可靠的信号，优先级高于时长与路径。
func detectBySequence(segs []Segment) []cand {
	out := []cand{}
	for _, h := range FindAdsBySequence(segs) {
		lo, hi := h[0], h[1]
		if lo < 0 || hi >= len(segs) || lo > hi {
			continue
		}
		c := cand{start: segs[lo].Idx, end: segs[hi].Idx, seqHit: true}
		n := hi - lo + 1
		sum := sumDur(segs[lo : hi+1])
		c.add(fmt.Sprintf("TS 序列不连贯（%d 段 / %.1fs，与正片链脱节）", n, sum), 100+float64(n)*4)
		out = append(out, c)
	}
	return out
}

// detectByURI 片段路径/域名异常检测：广告片段常来自与正片不同的目录，
// 甚至直接引用另一个域名上的素材（跨域名是最强的广告证据）。
func detectByURI(segs []Segment, baseURL string) []cand {
	out := []cand{}
	n := len(segs)
	if n < 20 {
		return out
	}
	selfHost := ""
	if u, err := url.Parse(baseURL); err == nil {
		selfHost = strings.ToLower(u.Hostname())
	}
	if selfHost == "" {
		// 无法得知正片片段域名时，域名维度无判别力，直接跳过避免误报
		return out
	}
	dirs := make([]string, n)
	hosts := make([]string, n)
	isAbs := make([]bool, n)
	dirCnt := map[string]int{}
	for i, s := range segs {
		name := strings.TrimSpace(s.Name)
		d := name
		if k := strings.LastIndex(d, "/"); k >= 0 {
			d = d[:k]
		} else {
			d = "."
		}
		dirs[i] = d
		dirCnt[d]++
		if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
			if u, err := url.Parse(name); err == nil {
				hosts[i] = strings.ToLower(u.Hostname())
				isAbs[i] = true
			}
		}
	}
	// (a) 跨域名片段：以全片主流域名为基准（很多站把成片放在独立源站域名上，
	//     全片同一外链域名属正常，不能一律判广告）
	eff := make([]string, n)
	hostCnt := map[string]int{}
	for i := 0; i < n; i++ {
		h := selfHost
		if isAbs[i] && hosts[i] != "" {
			h = hosts[i]
		}
		if h == "" {
			h = "?"
		}
		eff[i] = h
		hostCnt[h]++
	}
	mainHost, bestN := "", 0
	for h, c := range hostCnt {
		if c > bestN {
			mainHost, bestN = h, c
		}
	}
	if len(hostCnt) >= 2 {
		for i := 0; i < n; {
			if eff[i] == mainHost {
				i++
				continue
			}
			j := i
			for j+1 < n && eff[j+1] != mainHost && eff[j+1] == eff[i] {
				j++
			}
			c := j - i + 1
			if c >= 2 && c <= 60 {
				k := cand{start: segs[i].Idx, end: segs[j].Idx}
				k.add(fmt.Sprintf("片段域名 %s 与正片域名 %s 不同（%d 段）", eff[i], mainHost, c), 40+float64(c)*6)
				out = append(out, k)
			}
			i = j + 1
		}
	}
	// (b) 同域名下非主流目录（目录种类过多时无判别力，直接跳过）
	if len(dirCnt) >= 2 && len(dirCnt) <= 3 {
		mainDir, bestN := "", 0
		for d, c := range dirCnt {
			if c > bestN {
				mainDir, bestN = d, c
			}
		}
		for i := 0; i < n; {
			if dirs[i] == mainDir {
				i++
				continue
			}
			j := i
			for j+1 < n && dirs[j+1] != mainDir {
				j++
			}
			c := j - i + 1
			if c >= 2 && c <= 60 {
				k := cand{start: segs[i].Idx, end: segs[j].Idx}
				k.add(fmt.Sprintf("片段路径与正片不同（%d 段来自 %s）", c, dirs[i]), 10+float64(c)*6)
				out = append(out, k)
			}
			i = j + 1
		}
	}
	return out
}

// dominantRun 找出序列中占主导（出现次数过半）的时长值，并返回其最长连续段
func dominantRun(fp []float64) ([]float64, int) {
	if len(fp) < 3 {
		return nil, 0
	}
	cnt := map[float64]int{}
	for _, v := range fp {
		cnt[v]++
	}
	best, bn := 0.0, 0
	for v, c := range cnt {
		if c > bn || (c == bn && v < best) {
			best, bn = v, c
		}
	}
	if bn*2 < len(fp) {
		return nil, 0
	}
	run, bestRun := []float64{}, []float64{}
	for _, v := range fp {
		if v == best {
			run = append(run, v)
		} else {
			if len(run) > len(bestRun) {
				bestRun = run
			}
			run = nil
		}
	}
	if len(run) > len(bestRun) {
		bestRun = run
	}
	// 若最长连续段太短（如 3,3,5,3,3 被其它值隔断），退而取首末主导值之间的整段，
	// 去掉各广告位首尾各异的半段，让同一部片其它位置的广告也能命中
	if len(bestRun) < 3 {
		i, j := 0, len(fp)-1
		for i < len(fp) && fp[i] != best {
			i++
		}
		for j >= 0 && fp[j] != best {
			j--
		}
		if i < j && j-i+1 >= 4 && j-i+1 < len(fp) {
			bestRun = append([]float64{}, fp[i:j+1]...)
		}
	}
	return bestRun, bn
}

func sumDur(segs []Segment) float64 {
	s := 0.0
	for _, x := range segs {
		s += x.Dur
	}
	return s
}

// mergeCands 合并重叠候选，保留高分并累加评分
func mergeCands(list []cand) []cand {
	if len(list) == 0 {
		return list
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].start != list[j].start {
			return list[i].start < list[j].start
		}
		return list[i].end < list[j].end
	})
	merged := []cand{list[0]}
	for _, c := range list[1:] {
		last := &merged[len(merged)-1]
		ns, ne := c.start, c.end
		if last.start < ns {
			ns = last.start
		}
		if last.end > ne {
			ne = last.end
		}
		if c.start <= last.end+1 && c.end >= last.start && ne-ns+1 <= 40 {
			// 重叠：保留高分，合并依据
			if c.score > last.score {
				last.pattern = nil
			}
			for r := range c.reasons {
				last.add(r, 0)
			}
			if c.score > last.score {
				last.score = c.score
			}
			if c.repeat > last.repeat {
				last.repeat = c.repeat
			}
			if c.hashConfirmed {
				last.hashConfirmed = true
			}
			if c.end > last.end {
				last.end = c.end
			}
			if c.start < last.start {
				last.start = c.start
			}
		} else {
			merged = append(merged, c)
		}
	}
	return merged
}

// buildSuggest 生成建站建议（pick 为向导里用户选择的码率策略，需与实际选取保持一致）
func buildSuggest(res *ProbeResult, segs []Segment, pick string) ProbeSuggest {
	sg := ProbeSuggest{Host: res.Host, ParseType: "duration", StreamPick: "first", DetectMode: "fingerprint"}

	host := res.Host
	sg.URLPrefix = host

	// 缓存目录取通用主体（如 lzcdn27 → lzcdn），泛化正则兜底同系列编号域名
	main := HostMain(host)
	generic := GenericName(host)
	re := GenericRegex(host)
	sg.CacheSubdir = generic
	sg.URLRegex = re
	// 多值形式（供前端「可多个」的匹配前缀/正则列表直接使用）
	sg.URLPrefixes = []string{host}
	sg.URLRegexes = []string{re}
	sg.Name = fmt.Sprintf("%s(%s)", main, host)

	if res.IndexMode == "single" {
		sg.IndexMode = "single"
		sg.StreamPick = "first"
	} else {
		sg.IndexMode = "auto"
		if len(res.Streams) > 1 && (pick == "highest" || pick == "lowest") {
			sg.StreamPick = pick
		} else {
			sg.StreamPick = "first"
		}
	}

	// 序列连贯性命中：直接推荐 sequence 模式，无需任何指纹
	seqHits := []ProbeCandidate{}
	for _, c := range res.Candidates {
		if c.SeqHit {
			seqHits = append(seqHits, c)
		}
	}
	if len(seqHits) > 0 {
		sort.Slice(seqHits, func(i, j int) bool { return seqHits[i].Start < seqHits[j].Start })
		segs := 0
		parts := make([]string, 0, len(seqHits))
		for _, c := range seqHits {
			segs += c.Segs
			parts = append(parts, fmt.Sprintf("#%d-%d", c.Start, c.End))
		}
		sg.DetectMode = "sequence"
		sg.ParseType = "duration"
		sg.Fingerprint = nil
		sg.FingerprintSets = nil
		sg.Note = fmt.Sprintf("检出 %d 处 TS 序列不连贯的插播（%s，共 %d 段 / 全片 %d 段）：该站广告与正片 TS 不连贯，已推荐「序列连贯性」检测方式，无需填写特征码，保存后请在 M3U8 测试页核对剔除效果",
			len(seqHits), strings.Join(parts, "、"), segs, res.TotalSegs)
		return sg
	}

	if len(res.Candidates) > 0 {
		top := res.Candidates[0]
		sg.Fingerprint = top.Pattern
		notes := []string{fmt.Sprintf("可疑区间 #%d-#%d（%d 段 / %.1f 秒）", top.Start, top.End, top.Segs, top.Dur)}
		if top.HashConfirmed {
			notes = append(notes, "经内容哈希确认，建议用 hash/both 类型并维护哈希库")
			sg.ParseType = "both"
			sg.DetectMode = "both"
		} else {
			notes = append(notes, "按时长指纹定位，请核对区间无误后再启用")
			sg.ParseType = "duration"
		}
		if top.RepeatCount >= 2 {
			notes = append(notes, fmt.Sprintf("该序列在全片出现 %d 次", top.RepeatCount))
		}
		joined := strings.Join(top.Reasons, " ")
		if strings.Contains(joined, "与正片域名") {
			notes = append(notes, "片段域名与正片不同，基本可认定为广告素材，可信度很高")
		} else if strings.Contains(joined, "路径与正片不同") {
			notes = append(notes, "片段路径与正片不同，可信度较高")
		}
		if top.Segs > len(top.Pattern) {
			notes = append(notes, fmt.Sprintf("区间过长，特征码仅取前 %d 段", len(top.Pattern)))
		}
		// 通用化：区间首尾常残留半段，各广告位略有出入，取区间内占主导时长的最长连续段，
		// 让同一部片其它位置的广告也能命中
		if v, _ := dominantRun(top.Pattern); len(v) >= 3 && len(v) < len(top.Pattern) {
			top.Pattern = v
			sg.Fingerprint = v
			notes = append(notes, fmt.Sprintf("已自动截取区间内重复段（%d×%.2fs）作为通用指纹", len(v), v[0]))
		}
		// 区分度校验：若整段时长都贴近正片众数，时长指纹会把正片误判成广告，必须留空
		mode := 0.0
		if len(res.DurHistogram) > 0 {
			mode = res.DurHistogram[0].Dur
		}
		distinct := false
		oddCnt := 0
		for _, v := range top.Pattern {
			if math.Abs(v-mode) > 0.15 {
				oddCnt++
			}
		}
		if len(top.Pattern) > 0 && oddCnt*2 >= len(top.Pattern) {
			distinct = true
		}
		if !distinct && !top.HashConfirmed {
			sg.Fingerprint = nil
			sg.ParseType = "hash"
			sg.DetectMode = "hash"
			sg.FixedSub = ""
			notes = append(notes, fmt.Sprintf("注意：该区间片段时长都在 %.2fs 左右、与正片一致，时长指纹无法区分，已留空指纹并改用内容哈希校验，请在 M3U8 测试页验证剔除效果", mode))
		}
		if distinct {
			// 同一部片多处广告的首尾时长常不同，收集评分最高的几组互不相同的指纹，
			// 一并写入规则才能覆盖多处广告（如 lzcdn 这类每处广告时长都随机的站点）
			sets := [][]float64{}
			seen := map[string]bool{}
			for _, c := range res.Candidates {
				pat := append([]float64{}, c.Pattern...)
				if len(pat) == 0 {
					continue
				}
				if v, _ := dominantRun(pat); len(v) >= 3 && len(v) < len(pat) {
					pat = v
				}
				odd := 0
				for _, v := range pat {
					if math.Abs(v-mode) > 0.15 {
						odd++
					}
				}
				if odd*2 < len(pat) { // 与正片时长无区分度的组直接丢弃
					continue
				}
				key := fmt.Sprintf("%v", pat)
				if seen[key] {
					continue
				}
				seen[key] = true
				sets = append(sets, pat)
				if len(sets) >= 4 {
					break
				}
			}
			if len(sets) > 1 {
				sg.FingerprintSets = sets
				sg.Fingerprint = sets[0]
				notes = append(notes, fmt.Sprintf("该片多处广告时长不一致，已给出 %d 组指纹以覆盖各广告位，请在 M3U8 测试页核对", len(sets)))
			}
		}
		sg.Note = strings.Join(notes, "；")
	} else if res.HashUsed {
		sg.ParseType = "duration"
		sg.Note = fmt.Sprintf("已采样 %d/%d 个片段做内容比对，未发现重复出现的片段：该片广告可能只出现一次，或该源本身无广告。建议换一部广告会重复插播的影片再探一次，或在 M3U8 测试页人工比对。", res.HashOK, res.TotalSegs)
	} else {
		sg.ParseType = "duration"
		sg.Note = "未检出明显广告区间（可能该源无广告，或广告片段时长与正片一致）。可勾选「内容哈希探测」再试，或在 M3U8 测试页人工比对。"
	}
	return sg
}
