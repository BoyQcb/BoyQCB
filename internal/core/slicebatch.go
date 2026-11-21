package core

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// slicebatch 检测参数
const (
	sbClusterTol   = 2    // 同一切片作业内 mtime 抖动容忍（秒）：正片批量切片通常跨 1~2 秒
	sbMaxAdBlocks  = 3    // 广告区间最大块数（再多就是正片分卷）
	sbMaxAdDur     = 90.0 // 广告区间最大时长（秒）
	sbConcurrency  = 16   // 并发 HEAD 数
	sbMinOKRatio   = 0.6  // 有效批次响应占比下限，低于此认为信号不可用
	sbMinMainRatio = 0.3  // 正片（主簇）块数占比下限，低于此认为信号不可信
)

// segBlock 按 DISCONTINUITY 切分出的一个连续片段块
type segBlock struct {
	start, end int     // 1-based 闭区间
	dur        float64 // 块总时长
	url        string  // 块首段绝对地址（用于探测批次）
	batch      int64    // 切片批次时间（unix 秒）
	ok         bool     // 批次探测是否成功
	probeURLs  []string // 块内前几个片段的绝对地址（首段失败时顺延）
}

// FindAdsBySliceBatch 基于「切片批次」识别广告（闪电 sdzy 等"广告与正片同域同目录"的站点）
//
// 背景：这类站的广告与正片被拼进同一次切片作业之后的文件目录，域名、路径、命名规则、
// 分片时长（同为 2s）完全一致，序列检测 / 时长指纹 / 内容哈希均无法区分。
// 但广告素材与正片是两次独立的切片任务，产物文件的 mtime（体现在 ETag 前缀与
// Last-Modified）属于不同批次——这是唯一稳定的区分信号。
//
// 判据：
//  1. 按 DISCONTINUITY 分块，每块取首段发 HEAD，取批次时间（ETag 前缀 hex 优先，其次 Last-Modified）
//  2. 批次时间差 ≤ sbClusterTol 秒的归为同一切片作业；块数最多的簇 = 正片
//  3. 非正片簇的块合并为连续区间；只有「块数 ≤ 3 且总时长 ≤ 90s」的短插播才判广告
//     —— 连续十几块、几分钟的独立批次是正片分卷（不同转码源），不能误删
//
// 安全性：探测失败率高、主簇不明显、无异常批次时返回空，由调用方回退其它检测方式。
func FindAdsBySliceBatch(client *http.Client, segs []Segment, base, ua string, timeout time.Duration) [][2]int {
	if len(segs) < 10 {
		return nil
	}
	blocks := splitBlocks(segs, base)
	if len(blocks) < 5 {
		return nil
	}

	// 1) 并发探测每块的切片批次
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	limiter := make(chan struct{}, sbConcurrency)
	var wg sync.WaitGroup
	for i := range blocks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			limiter <- struct{}{}
			defer func() { <-limiter }()
			// 首段失败时顺延到块内后续片段，避免单个 5xx 让整块失去信号
			for _, u := range blocks[i].probeURLs {
				if u == "" {
					continue
				}
				if t, ok := headBatch(client, u, ua, timeout); ok {
					blocks[i].batch, blocks[i].ok = t, true
					return
				}
			}
		}(i)
	}
	wg.Wait()

	okCount := 0
	for _, b := range blocks {
		if b.ok {
			okCount++
		}
	}
	if okCount < 5 || float64(okCount) < float64(len(blocks))*sbMinOKRatio {
		return nil // 服务器未暴露批次信息，信号不可用
	}

	// 2) 批次聚类：相邻批次时间差 ≤ tol 的归为同一切片作业
	type cluster struct {
		times []int64
		idxs  []int // 块下标
	}
	order := make([]int, 0, len(blocks))
	for i := range blocks {
		if blocks[i].ok {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(a, b int) bool { return blocks[order[a]].batch < blocks[order[b]].batch })
	clusters := []*cluster{}
	for _, i := range order {
		cur := &cluster{times: []int64{blocks[i].batch}, idxs: []int{i}}
		clusters = append(clusters, cur)
	}
	// 合并相邻簇
	merged := []*cluster{clusters[0]}
	for _, c := range clusters[1:] {
		last := merged[len(merged)-1]
		if c.times[0]-last.times[len(last.times)-1] <= sbClusterTol {
			last.times = append(last.times, c.times...)
			last.idxs = append(last.idxs, c.idxs...)
			continue
		}
		merged = append(merged, c)
	}
	sort.Slice(merged, func(a, b int) bool { return len(merged[a].idxs) > len(merged[b].idxs) })
	main := merged[0]
	if float64(len(main.idxs)) < float64(okCount)*sbMinMainRatio {
		return nil // 没有占绝对多数的正片批次，判定不可信
	}
	mainSet := map[int]bool{}
	for _, i := range main.idxs {
		mainSet[i] = true
	}

	// 3) 非正片批次 → 合并连续区间 → 只保留短插播
	spans := [][2]int{}
	cur := [2]int{}
	curBlocks, curDur := 0, 0.0
	flush := func() {
		if curBlocks == 0 {
			return
		}
		if curBlocks <= sbMaxAdBlocks && curDur <= sbMaxAdDur {
			spans = append(spans, cur)
		}
		curBlocks, curDur = 0, 0.0
	}
	for i := range blocks {
		if blocks[i].ok && !mainSet[i] {
			if curBlocks > 0 && blocks[i].start == cur[1]+1 {
				cur[1] = blocks[i].end
				curBlocks++
				curDur += blocks[i].dur
				continue
			}
			flush()
			cur = [2]int{blocks[i].start, blocks[i].end}
			curBlocks, curDur = 1, blocks[i].dur
			continue
		}
		flush()
	}
	flush()

	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a][0] < spans[b][0] })
	return mergeSpans(spans)
}

// splitBlocks 按 DISCONTINUITY 切块，并为每块准备 1~3 个探测地址
func splitBlocks(segs []Segment, base string) []*segBlock {
	out := []*segBlock{}
	var cur *segBlock
	for i, s := range segs {
		idx := s.Idx
		if idx <= 0 {
			idx = i + 1
		}
		if s.Disc || cur == nil {
			cur = &segBlock{start: idx, end: idx, dur: s.Dur}
			out = append(out, cur)
		} else {
			cur.end = idx
			cur.dur += s.Dur
		}
		if len(cur.probeURLs) < 3 {
			cur.probeURLs = append(cur.probeURLs, AbsFromDir(base, s.Name))
		}
	}
	// 探测地址补全：块内片段数不足时上面的循环已覆盖，这里统一保证首地址存在
	for _, b := range out {
		if len(b.probeURLs) > 0 {
			b.url = b.probeURLs[0]
		}
	}
	return out
}

// headBatch 探测单个片段的切片批次时间：HEAD 优先，失败时用 Range GET 兜底
func headBatch(client *http.Client, url, ua string, timeout time.Duration) (int64, bool) {
	if url == "" {
		return 0, false
	}
	// 1) HEAD
	if req, err := http.NewRequest("HEAD", url, nil); err == nil {
		req.Header.Set("User-Agent", ua)
		if resp, err := client.Do(req); err == nil {
			code := resp.StatusCode
			t, ok := batchFromHeaders(resp.Header)
			resp.Body.Close()
			if ok && code == 200 {
				return t, true
			}
		}
	}
	// 2) Range GET（部分源站不支持 HEAD）
	if req, err := http.NewRequest("GET", url, nil); err == nil {
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Range", "bytes=0-0")
		if resp, err := client.Do(req); err == nil {
			code := resp.StatusCode
			t, ok := batchFromHeaders(resp.Header)
			resp.Body.Close()
			if ok && (code == 200 || code == 206) {
				return t, true
			}
		}
	}
	return 0, false
}

// batchFromHeaders 从响应头提取切片批次时间（unix 秒）
// nginx 的 ETag 形如 "6a92e8df-49f14"，前缀即文件 mtime 的十六进制
func batchFromHeaders(h http.Header) (int64, bool) {
	if et := strings.Trim(h.Get("ETag"), `"`); et != "" {
		prefix := et
		if i := strings.Index(prefix, "-"); i > 0 {
			prefix = prefix[:i]
		}
		if v, err := strconv.ParseInt(prefix, 16, 64); err == nil && v > 1000000000 && v < 4000000000 {
			return v, true
		}
	}
	if lm := h.Get("Last-Modified"); lm != "" {
		for _, layout := range []string{time.RFC1123, http.TimeFormat, time.RFC850, time.ANSIC} {
			if t, err := time.Parse(layout, lm); err == nil {
				return t.Unix(), true
			}
		}
	}
	return 0, false
}
