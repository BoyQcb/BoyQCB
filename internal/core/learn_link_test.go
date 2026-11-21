package core

import (
	"strconv"
	"testing"
)

// 构造：正片 20 段（时长 10.0，序号连续，同域名），中间 10-14 段为广告（时长序列 5.0/3.0/5.0/3.0/1.6）
// 广告段与正片序号连续、域名相同 → FindAdsBySequence 无法识别（模拟"结构检测漏判"）
func buildM3U8() string {
	s := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:12\n"
	for i := 1; i <= 20; i++ {
		dur := 10.0
		switch i {
		case 10:
			dur = 5.0
		case 11:
			dur = 3.0
		case 12:
			dur = 5.0
		case 13:
			dur = 3.0
		case 14:
			dur = 1.6
		}
		s += "#EXTINF:" + ftoa(dur) + ",\n2026/01/01/" + strconv.Itoa(i) + ".ts\n"
	}
	return s + "#EXT-X-ENDLIST\n"
}

func ftoa(v float64) string {
	switch v {
	case 10.0:
		return "10.000000"
	case 5.0:
		return "5.000000"
	case 3.0:
		return "3.000000"
	case 1.6:
		return "1.600000"
	}
	return "1.000000"
}

func itoa(i int) string { return string(rune('0' + i%10)) }

func newTestParser() *Parser {
	st := &Settings{MatchTolerance: 0.05, UseHashCheck: false, FetchTimeout: 5, CacheTTL: 60}
	return NewParser(st, nil, nil, "")
}

// 用例 1：结构模式（sequence）下，站点配置的时长指纹应生效（修复前被完全忽略）
func TestFingerprintWorksUnderSequenceMode(t *testing.T) {
	segs := ParseSegmentsRaw(buildM3U8())
	if len(segs) != 20 {
		t.Fatalf("解析段数异常: %d", len(segs))
	}
	site := &Site{ID: "t", DetectMode: "sequence", Fingerprint: []float64{5.0, 3.0, 5.0, 3.0, 1.6}}

	seqHits := FindAdsBySequence(segs)
	if len(seqHits) != 0 {
		t.Fatalf("前置条件不成立：结构检测本应漏判，实际命中 %v", seqHits)
	}

	p := newTestParser()
	hits := p.detectAds(segs, site, "")
	if len(hits) != 1 || hits[0][0] != 10 || hits[0][1] != 14 {
		t.Fatalf("结构模式下时长指纹未生效，命中: %v", hits)
	}
}

// 用例 2：保护指纹应撤销误判区间（keep 标记学习回规则后生效）
func TestKeepFingerprintBlocksRemoval(t *testing.T) {
	segs := ParseSegmentsRaw(buildM3U8())
	site := &Site{
		ID:               "t",
		DetectMode:       "sequence",
		Fingerprint:      []float64{5.0, 3.0, 5.0, 3.0, 1.6},
		KeepFingerprints: [][]float64{{5.0, 3.0, 5.0}},
	}
	p := newTestParser()
	hits := p.detectAds(segs, site, "")
	if len(hits) != 0 {
		t.Fatalf("保护指纹未撤销命中，仍将剔除: %v", hits)
	}
}

// 用例 3：补充命中剔除量超过 30% 时应被安全闸门拦下（URI 序号连续，排除结构检测干扰）
func TestFingerprintSafetyGuard(t *testing.T) {
	// 全片 10 段，全部满足指纹 1.0 → 剔除 100% 明显不可信，应放弃补充
	s := "#EXTM3U\n"
	for i := 1; i <= 10; i++ {
		s += "#EXTINF:1.000000,\n" + strconv.Itoa(i) + ".ts\n"
	}
	segs := ParseSegmentsRaw(s + "#EXT-X-ENDLIST\n")
	if h := FindAdsBySequence(segs); len(h) != 0 {
		t.Fatalf("前置条件不成立：结构检测本应漏判，实际命中 %v", h)
	}
	site := &Site{ID: "t", DetectMode: "sequence", Fingerprint: []float64{1.0}}
	p := newTestParser()
	hits := p.detectAds(segs, site, "")
	if len(hits) != 0 {
		t.Fatalf("安全闸门失效，过量剔除: %v", hits)
	}
}

// 用例 4：未配置 detect_mode 的旧规则行为不变（指纹仍为主判据）
func TestLegacyModeUnchanged(t *testing.T) {
	segs := ParseSegmentsRaw(buildM3U8())
	site := &Site{ID: "t", DetectMode: "", ParseType: "both", Fingerprint: []float64{5.0, 3.0, 5.0, 3.0, 1.6}}
	p := newTestParser()
	hits := p.detectAds(segs, site, "")
	if len(hits) != 1 || hits[0][0] != 10 || hits[0][1] != 14 {
		t.Fatalf("旧规则模式行为改变，命中: %v", hits)
	}
}
