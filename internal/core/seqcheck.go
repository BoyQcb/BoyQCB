package core

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// tailNumPattern 匹配文件名中最后一段连续数字：xxxx0000066.ts -> 0000066
func tailNumPattern() *regexp.Regexp {
	return regexp.MustCompile(`(\d+)\.([A-Za-z0-9]+)$`)
}

// seqNumRe 包级复用，避免逐片段重复编译
var seqNumRe = tailNumPattern()

// FindAdsBySequence 基于 TS 片段序列连贯性识别广告（核心判据：广告插播的 TS 与正片不连贯）
//
// 判据分两类：
//  1. 路径/域名不同：广告 TS 来自别的域名或目录（如 modujx 正片在 bf.*/2296kb/hls/，广告跳到 /20260811/.../10138kb/hls/）
//  2. 序号断裂：文件名序号不连续递增（如 lzcdn 正片 000000→000695，广告跳到 418696）
//
// 处理要点：随机文件名（如 NArkAZNq.ts）尾部也带数字，必须先判定「该路径下的片段是否总体有序」，
// 只有有序序列才用序号判连贯，否则整条路径视为同一族，避免把随机名正片误判成广告。
//
// 安全性：正片族不足总段数一半、或广告占比超 30% 时判定不可信，直接返回空（宁可不删，不可错删）。
func FindAdsBySequence(segs []Segment) [][2]int {
	n := len(segs)
	if n < 3 {
		return nil
	}
	keys := make([]string, n)
	nums := make([]int64, n)
	hasN := make([]bool, n)
	for i, s := range segs {
		keys[i], nums[i], hasN[i] = segSeqKey(s.Name)
	}

	// 1) 按路径分组，判定各组是否为「有序序列」（相邻序号 +1 的占比 ≥ 80%）
	type keyStat struct {
		idxs    []int
		ordered bool
	}
	stats := map[string]*keyStat{}
	for i := range segs {
		st, ok := stats[keys[i]]
		if !ok {
			st = &keyStat{}
			stats[keys[i]] = st
		}
		st.idxs = append(st.idxs, i)
	}
	for _, st := range stats {
		if len(st.idxs) < 4 {
			st.ordered = false
			continue
		}
		cont := 0
		for j := 1; j < len(st.idxs); j++ {
			a, b := st.idxs[j-1], st.idxs[j]
			if hasN[a] && hasN[b] && nums[b] == nums[a]+1 {
				cont++
			}
		}
		st.ordered = cont*5 >= (len(st.idxs)-1)*4 // ≥ 80%
	}
	// 2) 切成连续链
	chains := []*seqChain{}
	cs := 0
	curOrdered := stats[keys[0]].ordered
	for i := 1; i <= n; i++ {
		brk := true
		if i < n {
			same := keys[i] == keys[cs]
			if same {
				if curOrdered {
					same = hasN[i] && hasN[i-1] && nums[i] == nums[i-1]+1
				} else {
					same = true
				}
			}
			brk = !same
		}
		if brk {
			chains = append(chains, &seqChain{
				start: cs, end: i - 1, key: keys[cs], ordered: curOrdered,
				first: nums[cs], last: nums[i-1], count: i - cs,
			})
			if i < n {
				cs = i
				curOrdered = stats[keys[i]].ordered
			}
		}
	}

	// 3) 链并为族：有序路径按序号首尾相接，无序路径按路径合并
	type family struct {
		key     string
		ordered bool
		total   int
		last    int64
	}
	fams := []*family{}
	famOf := make([]int, len(chains))
	for i, c := range chains {
		placed := -1
		for j, f := range fams {
			if f.key != c.key {
				continue
			}
			if c.ordered && f.ordered {
				if c.first == f.last+1 {
					placed = j
					break
				}
				continue
			}
			if !c.ordered && !f.ordered {
				placed = j
				break
			}
		}
		if placed < 0 {
			fams = append(fams, &family{key: c.key, ordered: c.ordered})
			placed = len(fams) - 1
		}
		f := fams[placed]
		f.total += c.count
		if c.ordered {
			f.last = c.last
		}
		famOf[i] = placed
	}
	if len(fams) <= 1 {
		return nil // 全片同族，无插播
	}

	// 4) 段数最多的族 = 正片
	main := 0
	for i, f := range fams {
		if f.total > fams[main].total {
			main = i
		}
	}
	// 全片无序（随机名）时，若正片族与次大族差距不悬殊，说明不是典型插播，放弃
	mainTotal := fams[main].total
	if mainTotal*2 <= n {
		return nil // 正片不足一半，判定不可信
	}
	adsTotal := n - mainTotal
	if adsTotal*10 > n*3 {
		return nil // 广告占比超 30%，判定不可信
	}

	// 5) 非正片族的连续链即广告区间
	// chains 的 start/end 是 segs 下标（0-based），统一转成段号（1-based），
	// 与 FindAds / FindAdsMulti 返回语义一致，供 BuildClean 直接按 lineSeg[段号] 取行
	hits := [][2]int{}
	for i, c := range chains {
		if famOf[i] != main {
			hits = append(hits, [2]int{c.start + 1, c.end + 1})
		}
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a][0] < hits[b][0] })
	return hits
}

type seqChain struct {
	start, end  int    // 片段索引区间（0-based，左闭右闭）
	key         string // 路径 key
	ordered     bool   // 该路径是否为有序序列
	first, last int64  // 链首尾序号
	count       int
}

// segSeqKey 提取片段的 (路径key, 序号, 是否有序号)
// 路径 key：绝对 URL 取 scheme://host/dir/，相对路径取 /dir/，用于区分不同域名/目录来源
func segSeqKey(name string) (string, int64, bool) {
	dir := ""
	fn := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		dir = name[:i+1]
		fn = name[i+1:]
	}
	if i := strings.IndexByte(fn, '?'); i >= 0 {
		fn = fn[:i]
	}
	if i := strings.IndexByte(dir, '?'); i >= 0 {
		dir = dir[:i]
	}
	m := seqNumRe.FindStringSubmatch(fn)
	if m == nil {
		return dir, 0, false
	}
	num, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		// 超长数字串（如 20 位时间戳）溢出 int64：截取末 9 位，保持相对连续性
		s := m[1]
		if len(s) > 9 {
			s = s[len(s)-9:]
			num, err = strconv.ParseInt(s, 10, 64)
		}
		if err != nil {
			return dir, 0, false
		}
	}
	return dir, num, true
}

// SeqReason 供探测页展示序列检测依据
type SeqReason struct {
	Method string `json:"method"` // path / number
	Detail string `json:"detail"`
}

// DescribeSequenceHits 描述序列检测命中原因（探测页展示用）
func DescribeSequenceHits(segs []Segment, hits [][2]int) []SeqReason {
	out := []SeqReason{}
	for _, h := range hits {
		if h[0] < 0 || h[1] >= len(segs) || h[0] > h[1] {
			continue
		}
		key, _, _ := segSeqKey(segs[h[0]].Name)
		m := "path"
		d := "片段路径/域名与正片不同"
		if h[1] > h[0] {
			m = "path/number"
			d = "片段路径或序号与正片不连贯"
		}
		out = append(out, SeqReason{Method: m, Detail: d + "（" + trimName(key) + "）"})
	}
	return out
}

func trimName(s string) string {
	if len(s) > 48 {
		return "..." + s[len(s)-45:]
	}
	return s
}
