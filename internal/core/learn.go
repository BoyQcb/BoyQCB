// 规则学习：把用户手动标记的片段转成可直接写回站点规则的产物
//
// 标记 → 按段号归并成连续区间 → 每区间生成一条时长指纹（[]float64）
// 抓取标记段 TS 的前 N 字节 → 生成内容哈希组（[]HashSeg）

package core

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

// LearnSeg 参与学习的一个片段（与 store.MarkSeg 字段一致，避免 core 反向依赖 store）
type LearnSeg struct {
	Index int     `json:"index"`
	Dur   float64 `json:"dur"`
	URL   string  `json:"url"`
	Kind  string  `json:"kind"`
	WasAd bool    `json:"was_ad"`
}

// LearnSpan 归并后的连续区间
type LearnSpan struct {
	From  int        `json:"from"`
	To    int        `json:"to"`
	Kind  string     `json:"kind"`
	Segs  []LearnSeg `json:"segs"`
	Durs  []float64  `json:"durs"`  // 时长指纹（该区间的时长序列）
	Total float64    `json:"total"` // 区间总时长（秒）
}

// LearnStats 标记统计
type LearnStats struct {
	Marked  int `json:"marked"`  // 标记总数
	Missed  int `json:"missed"`  // 漏判：用户判广告、系统判正片
	Wrong   int `json:"wrong"`   // 误判：用户判正片、系统判广告
	Suspect int `json:"suspect"` // 疑似
}

// GroupSpans 把段号按连续性归并成区间（同类型才合并）
func GroupSpans(segs []LearnSeg) []LearnSpan {
	if len(segs) == 0 {
		return nil
	}
	cp := append([]LearnSeg(nil), segs...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Index < cp[j].Index })

	spans := []LearnSpan{}
	cur := []LearnSeg{cp[0]}
	for i := 1; i < len(cp); i++ {
		prev := cur[len(cur)-1]
		if cp[i].Kind == prev.Kind && cp[i].Index == prev.Index+1 {
			cur = append(cur, cp[i])
			continue
		}
		spans = append(spans, mkSpan(cur))
		cur = []LearnSeg{cp[i]}
	}
	spans = append(spans, mkSpan(cur))
	return spans
}

func mkSpan(segs []LearnSeg) LearnSpan {
	durs := make([]float64, 0, len(segs))
	total := 0.0
	for _, s := range segs {
		durs = append(durs, round2(s.Dur))
		total += s.Dur
	}
	return LearnSpan{
		From:  segs[0].Index,
		To:    segs[len(segs)-1].Index,
		Kind:  segs[0].Kind,
		Segs:  segs,
		Durs:  durs,
		Total: round2(total),
	}
}

// StatsFromMarks 统计标记构成
func StatsFromMarks(segs []LearnSeg) LearnStats {
	st := LearnStats{}
	for _, s := range segs {
		st.Marked++
		switch s.Kind {
		case MarkAd:
			if !s.WasAd {
				st.Missed++
			}
		case MarkKeep:
			if s.WasAd {
				st.Wrong++
			}
		default:
			st.Suspect++
		}
	}
	return st
}

// 标记类型常量（与 store 包保持一致，避免 core 依赖 store）
const (
	MarkAd      = "ad"
	MarkKeep    = "keep"
	MarkSuspect = "suspect"
)

// FingerprintExists 判断候选指纹是否已存在于站点规则（容差内逐项比对）
func FingerprintExists(exist [][]float64, fp []float64, tol float64) bool {
	for _, e := range exist {
		if len(e) != len(fp) {
			continue
		}
		ok := true
		for i := range e {
			d := e[i] - fp[i]
			if d < 0 {
				d = -d
			}
			if d > tol {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// FingerprintInSpan 判断指纹 fp 是否为时长序列 span 的连续子序列（容差内逐项比对）：
// 用于把「误判（keep）」标记区间反查到造成误判的既有广告指纹。
func FingerprintInSpan(fp, span []float64, tol float64) bool {
	if len(fp) == 0 || len(span) < len(fp) {
		return false
	}
	for i := 0; i+len(fp) <= len(span); i++ {
		ok := true
		for j := range fp {
			d := span[i+j] - fp[j]
			if d < 0 {
				d = -d
			}
			if d > tol {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// TrimFP 限制指纹长度：过长的时长序列跨影片复用率低，取前 n 段即可
func TrimFP(fp []float64, n int) []float64 {
	if n <= 0 || len(fp) <= n {
		return fp
	}
	out := make([]float64, n)
	copy(out, fp[:n])
	return out
}

// BuildHashGroup 并发抓取若干片段的采样内容，生成哈希组
// 返回（哈希组，失败的段号）
func BuildHashGroup(client *http.Client, segs []LearnSeg, ua string, sampleSize int, timeout time.Duration) ([]HashSeg, []int) {
	if client == nil || len(segs) == 0 {
		return nil, nil
	}
	if sampleSize <= 0 {
		sampleSize = 262144
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Index < segs[j].Index })

	out := make([]HashSeg, len(segs))
	failed := []int{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 并发上限 4：源站单请求约 1s，太多并发易触发限流
	sem := make(chan struct{}, 4)
	for i, s := range segs {
		wg.Add(1)
		go func(i int, s LearnSeg) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h, err := FetchSample(client, s.URL, ua, sampleSize, timeout)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || h == "" {
				failed = append(failed, s.Index)
				return
			}
			out[i] = HashSeg{Sample: h, Size: sampleSize}
		}(i, s)
	}
	wg.Wait()

	// 去掉采集失败的空位，保持段序
	group := make([]HashSeg, 0, len(out))
	for _, h := range out {
		if h.Sample != "" {
			group = append(group, h)
		}
	}
	sort.Ints(failed)
	return group, failed
}
