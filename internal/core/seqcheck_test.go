package core

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestFindAdsBySequenceReal 用真实站点 m3u8 验证序列检测（需联网）
func TestFindAdsBySequenceReal(t *testing.T) {
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120 Safari/537.36"
	cases := []struct {
		name      string
		link      string
		indexMode string
	}{
		{"量子 lzcdn", "https://v.lzcdn27.com/20260828/16162_6a1052c9/index.m3u8", "auto"},
		{"暴风 fengbao", "https://fengbao12.com/video/shixiongtaiwenjian/4f9d1c809944/index.m3u8", "auto"},
		{"魔都 modujx", "https://play.modujx17.com/20260828/ZhszFT3k/index.m3u8", "auto"},
	}
	client := &http.Client{Timeout: 40 * time.Second}
	for _, c := range cases {
		pl, err := ResolvePlaylistOpt(client, c.link, ua, 40*time.Second, c.indexMode, "first", "")
		if err != nil {
			t.Fatalf("%s 索引解析失败: %v", c.name, err)
		}
		// 网络偶发 EOF，重试 3 次
		content := ""
		for r := 0; r < 3; r++ {
			content, err = Fetch(client, pl.URL, ua, 40*time.Second)
			if err == nil {
				break
			}
			time.Sleep(800 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("%s 列表抓取失败: %v", c.name, err)
		}
		segs, _ := ParseSegments(content)
		t.Logf("=== %s 子列表=%s 总片段=%d", c.name, pl.URL, len(segs))

		hits := FindAdsBySequence(segs)
		ads := 0
		for i, h := range hits {
			ads += h[1] - h[0] + 1
			if i < 12 {
				t.Logf("  广告区间 [%d, %d] 共%d段 首段=%s", h[0], h[1], h[1]-h[0]+1, shortName(segs[h[0]].Name))
			}
		}
		if len(hits) > 12 {
			t.Logf("  ...（其余 %d 处已省略）", len(hits)-12)
		}
		t.Logf("  -> 检出广告 %d 处 / %d 段（占比 %.2f%%）", len(hits), ads, float64(ads)*100/float64(len(segs)))
		if len(hits) == 0 {
			t.Errorf("%s 未检出广告", c.name)
		}
	}
}

func shortName(s string) string {
	if len(s) > 60 {
		return "..." + s[len(s)-57:]
	}
	return s
}

// TestSegSeqKey 验证序号提取
func TestSegSeqKey(t *testing.T) {
	cases := []struct {
		in     string
		key    string
		num    int64
		hasNum bool
	}{
		{"0000066.ts", "", 66, true},
		{"76d1a0f7f35000066.ts", "", 35000066, true},
		{"https://bf.modujx17.com/20260828/ZhszFT3k/2296kb/hls/NArkAZNq.ts", "https://bf.modujx17.com/20260828/ZhszFT3k/2296kb/hls/", 0, false},
		{"/video/adjump/time/17873200017900000000.ts", "/video/adjump/time/", 900000000, true},
	}
	for _, c := range cases {
		k, n, ok := segSeqKey(c.in)
		if k != c.key || n != c.num || ok != c.hasNum {
			t.Errorf("segSeqKey(%q) = (%q,%d,%v), 期望 (%q,%d,%v)", c.in, k, n, ok, c.key, c.num, c.hasNum)
		}
	}
	fmt.Println("segSeqKey 用例通过")
}
