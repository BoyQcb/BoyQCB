package core

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var extinfRe = regexp.MustCompile(`#EXTINF:([\d.]+)`)
var httpRe = regexp.MustCompile(`(?i)^https?://`)
var streamInfRe = regexp.MustCompile(`(?i)^#EXT-X-STREAM-INF:(.*)$`)
var bwRe = regexp.MustCompile(`(?i)BANDWIDTH=(\d+)`)
var resRe = regexp.MustCompile(`(?i)RESOLUTION=([\dx]+)`)
var codecRe = regexp.MustCompile(`(?i)CODECS="([^"]*)"`)
var m3u8Re = regexp.MustCompile(`(?i)\.m3u8($|\?)`)

// StreamInfo 主索引中的一条码率流
type StreamInfo struct {
	Index      int    `json:"index"`
	Bandwidth  int    `json:"bandwidth"`
	Resolution string `json:"resolution"`
	Codecs     string `json:"codecs"`
	URI        string `json:"uri"` // 原始写法（可能相对 / 根绝对 / 完整 URL）
}

// Playlist 定位到的实际片段列表
type Playlist struct {
	URL      string       // 片段索引列表地址
	Base     string       // 基础目录
	Content  string       // 列表内容
	Master   bool         // 原始 URL 是否为主索引（含 STREAM-INF）
	IsSingle bool         // 是否为单级列表（master 自身即媒体列表）
	Streams  []StreamInfo // 主索引码率列表
	Picked   *StreamInfo  // 实际选中的码率流
}

// ResolvePlaylist 兼容旧签名：自动跟随主索引，取第一条码率流。
func ResolvePlaylist(client *http.Client, masterURL, ua string, timeout time.Duration) (*Playlist, error) {
	return ResolvePlaylistOpt(client, masterURL, ua, timeout, "", "first", "")
}

// ResolvePlaylistOpt 读取 index.m3u8 定位实际片段列表。
//
// mode 索引模式：
//   - "" / "auto"  ：自动。含 #EXT-X-STREAM-INF 则跟随子索引，否则视为单级媒体列表
//   - "single"     ：强制单级，把 masterURL 本身当作媒体列表（如 fengbao12）
//   - "fixed"      ：按 fixedSub 指定固定子路径（相对 / 根绝对 / 完整 URL 均可）
//
// pick 码率选择策略（主索引多条流时）：first / highest / lowest
func ResolvePlaylistOpt(client *http.Client, masterURL, ua string, timeout time.Duration,
	mode, pick, fixedSub string) (*Playlist, error) {

	content, err := Fetch(client, masterURL, ua, timeout)
	if err != nil {
		return nil, err
	}
	base := DirOf(masterURL)
	single := func(c string) *Playlist {
		return &Playlist{URL: masterURL, Base: base, Content: c, Master: false, IsSingle: true}
	}

	// 固定子路径模式
	if mode == "fixed" && strings.TrimSpace(fixedSub) != "" {
		sub := AbsURL(masterURL, fixedSub)
		sc, err := Fetch(client, sub, ua, timeout)
		if err != nil {
			return nil, err
		}
		return &Playlist{URL: sub, Base: DirOf(sub), Content: sc, Master: true}, nil
	}
	if mode == "single" {
		return single(content), nil
	}

	streams := parseStreams(content)
	if len(streams) == 0 {
		return single(content), nil
	}

	picked := chooseStream(streams, pick)
	sub := AbsURL(masterURL, picked.URI)
	sc, err := Fetch(client, sub, ua, timeout)
	if err != nil {
		// 子索引抓取失败时回退到单级，避免整链失败
		return single(content), nil
	}
	// 多级下钻：部分站点的子索引仍是主索引（最多再下钻 2 层）
	depth := 0
	for depth < 2 && len(parseStreams(sc)) > 0 && len(ParseSegmentsRaw(sc)) == 0 {
		subStreams := parseStreams(sc)
		p2 := chooseStream(subStreams, pick)
		sub2 := AbsURL(sub, p2.URI)
		sc2, err2 := Fetch(client, sub2, ua, timeout)
		if err2 != nil {
			break
		}
		sub, sc, picked = sub2, sc2, p2
		depth++
	}

	return &Playlist{
		URL: sub, Base: DirOf(sub), Content: sc,
		Master: true, IsSingle: false, Streams: streams, Picked: &picked,
	}, nil
}

// parseStreams 从主索引内容解析 #EXT-X-STREAM-INF 及其后的 URI 行
func parseStreams(content string) []StreamInfo {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	out := []StreamInfo{}
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if m := streamInfRe.FindStringSubmatch(l); m != nil {
			attrs := m[1]
			si := StreamInfo{Index: len(out)}
			if b := bwRe.FindStringSubmatch(attrs); b != nil {
				if v, err := strconv.Atoi(b[1]); err == nil {
					si.Bandwidth = v
				}
			}
			if r := resRe.FindStringSubmatch(attrs); r != nil {
				si.Resolution = r[1]
			}
			if c := codecRe.FindStringSubmatch(attrs); c != nil {
				si.Codecs = c[1]
			}
			// URI 在随后的首个非空非注释行
			for j := i + 1; j < len(lines) && j <= i+3; j++ {
				s := strings.TrimSpace(lines[j])
				if s == "" || strings.HasPrefix(s, "#") {
					continue
				}
				si.URI = s
				i = j
				break
			}
			if si.URI != "" {
				out = append(out, si)
			}
		}
	}
	return out
}

// chooseStream 按策略选择码率流
func chooseStream(streams []StreamInfo, pick string) StreamInfo {
	if len(streams) == 0 {
		return StreamInfo{}
	}
	switch strings.ToLower(strings.TrimSpace(pick)) {
	case "highest":
		best := streams[0]
		for _, s := range streams {
			if s.Bandwidth > best.Bandwidth {
				best = s
			}
		}
		return best
	case "lowest":
		best := streams[0]
		for _, s := range streams {
			if s.Bandwidth < best.Bandwidth {
				best = s
			}
		}
		return best
	default:
		return streams[0]
	}
}

// ParseSegmentsRaw 仅返回片段数量（用于判断是否为媒体列表）
func ParseSegmentsRaw(content string) []Segment {
	segs, _ := ParseSegments(content)
	return segs
}

// ParseSegments 解析片段列表，返回片段数组与原始行。
// 对应 PHP parse_segments：跟踪 EXTINF 时长与 DISCONTINUITY 标记。
func ParseSegments(content string) ([]Segment, []string) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	segs := make([]Segment, 0, len(lines)/4)
	dur := 0.0
	idx := 0
	disc := false
	discCnt := 0 // 当前片段前连续 DISCONTINUITY 行数（相邻 DISC 间无 ts 才累加）
	for _, l := range lines {
		if strings.HasPrefix(l, "#EXT-X-DISCONTINUITY") {
			disc = true
			discCnt++
		}
		if strings.HasPrefix(l, "#EXTINF:") {
			if m := extinfRe.FindStringSubmatch(l); m != nil {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil {
					dur = v
				}
			}
		}
		s := strings.TrimSpace(l)
		if s != "" && !strings.HasPrefix(s, "#") {
			idx++
			segs = append(segs, Segment{Idx: idx, Dur: dur, Name: s, Disc: disc, DiscPair: discCnt >= 2})
			disc = false
			discCnt = 0
		}
	}
	return segs, lines
}

// HasStreamInf 判断内容是否为主索引
func HasStreamInf(content string) bool {
	return len(parseStreams(content)) > 0
}

// IsMediaPlaylist 判断内容是否为媒体列表（含 EXTINF 片段）
func IsMediaPlaylist(content string) bool {
	return len(ParseSegmentsRaw(content)) > 0
}

// IsM3U8Link 判断地址是否指向 m3u8
func IsM3U8Link(s string) bool {
	return m3u8Re.MatchString(strings.ToLower(s))
}
