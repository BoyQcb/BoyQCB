package core

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// loadMatrixSites 从 data/rules.json 载入真实站点规则
func loadMatrixSites(t *testing.T) []Site {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "data", "rules.json"))
	if err != nil {
		t.Fatalf("读取 rules.json 失败: %v", err)
	}
	var f struct {
		Sites []Site `json:"sites"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析 rules.json 失败: %v", err)
	}
	if len(f.Sites) == 0 {
		t.Fatal("rules.json 中没有站点规则")
	}
	return f.Sites
}

func siteNameOf(s *Site) string {
	if s == nil {
		return "-"
	}
	return s.Name
}

type matrixCase struct {
	url    string
	expect string // 期望命中的站点名，"-" 表示期望不命中
	note   string
}

// matrixCases 匹配矩阵用例：前 20 条为各站点正常样例（期望命中本站），
// 后 8 条为跨站反例（URL 路径/参数里出现其它站点的泛化关键字，期望不被误命中）。
func matrixCases() []matrixCase {
	return []matrixCase{
		// ===== 正常样例：每条规则 2-3 个真实 host/URL =====
		{"https://vip.dytt555.com/20240101/abc/index.m3u8", "电影天堂", "dytt 前缀 vip.dytt"},
		{"https://vip.dytt888.com/hls/index.m3u8", "电影天堂", "dytt 前缀 vip.dytt（任意数字后缀）"},
		{"https://svip.xgplay8.com/2024/abc/index.m3u8", "西瓜", "xg 前缀 svip.xgplay"},
		{"https://v.lzcdn28.com/2024/abc/index.m3u8", "量子", "lz 前缀 v.lzcdn28.com"},
		{"https://vip.lz-cdn14.com/2024/abc/index.m3u8", "量子", "lz 正则 lz-cdn\\d* 命中 host"},
		{"https://www.tvkuu.com/2024/abc/index.m3u8", "量子", "lz 前缀 www.tvkuu.com"},
		{"https://fengbao12.com/2024/abc/index.m3u8", "暴风", "fengbao 前缀 fengbao12.com"},
		{"https://v.baofeng9.com/2024/abc/index.m3u8", "暴风", "fengbao 正则 baofeng\\d* 命中 host"},
		{"https://s3.bfllvip.com/2024/abc/index.m3u8", "暴风", "fengbao 前缀 s3.bfllvip.com"},
		{"https://play.modujx17.com/2024/abc/index.m3u8", "魔都", "modujx 前缀"},
		{"https://v14.wsyzym3u8.com/2024/abc/index.m3u8", "无水印", "wsyzym 前缀"},
		{"https://www.mtime.com/2024/abc/index.m3u8", "无水印", "wsyzym 前缀 www.mtime.com"},
		{"https://morricone.cn/2024/abc/index.m3u8", "无水印", "wsyzym 前缀 morricone.cn"},
		{"https://v14.qqqrst.com/2024/abc/index.m3u8", "闪电", "sdzy 前缀"},
		{"https://v3.qqqrst.com/2024/abc/index.m3u8", "闪电", "sdzy 前缀"},
		{"https://v6.wujinwsym3u8.com/2024/abc/index.m3u8", "无尽", "wujin 前缀"},
		{"https://yzzy.play-cdn17.com/2024/abc/index.m3u8", "优质", "yyzy 前缀"},
		{"https://svip.high24-playback.com/2024/abc/index.m3u8", "优质", "yyzy 前缀"},
		{"https://cdn16.yzzy-kb-cdn.com/2024/abc/index.m3u8", "优质", "yyzy 前缀"},

		// ===== 跨站反例：路径里含其它站点的泛化关键字 =====
		{"https://cdn.example.com/mtime/index.m3u8", "-", "反例：路径含 mtime，不应命中无水印"},
		{"https://cdn.example.com/lzcdn28/index.m3u8", "-", "反例：路径含 lzcdn，不应命中量子"},
		{"https://cdn.example.com/play-cdn17/index.m3u8", "-", "反例：路径含 play-cdn，不应命中优质"},
		{"https://yzzy.play-cdn17.com/2024/lzcdn/index.m3u8", "优质", "反例：host 属优质，路径含 lzcdn，不应被量子抢走"},
		{"https://v14.qqqrst.com/2024/baofeng/ad.m3u8", "闪电", "反例：host 属闪电，路径含 baofeng，不应被暴风抢走"},
		{"https://cdn16.yzzy-kb-cdn.com/2024/wujinwsym3u8/index.m3u8", "优质", "反例：host 属优质，路径含 wujinwsym3u，不应被无尽抢走"},
		{"https://svip.xgplay8.com/2024/qqqrst/index.m3u8", "西瓜", "反例：host 属西瓜，路径含 qqqrst，不应被闪电抢走"},
		{"https://v14.wsyzym3u8.com/2024/lzcdn/index.m3u8", "无水印", "反例：host 属无水印，路径含 lzcdn，不应被量子抢走"},
	}
}

// TestFindSiteMatrix 跑匹配矩阵：同一批 URL 分别用「修复前（旧）逻辑」与「当前 FindSite」跑，
// 输出对照表，并统计各自与期望不符的条数。
func TestFindSiteMatrix(t *testing.T) {
	sites := loadMatrixSites(t)
	p := NewParser(nil, sites, nil, "")
	oldBad, newBad := 0, 0
	t.Logf("%-60s | %-8s | %-8s | %-8s | %s", "URL", "修复前", "修复后", "期望", "说明")
	t.Logf("%s", "--------------------------------------------------------------+----------+----------+----------+--------")
	for _, c := range matrixCases() {
		oldGot := siteNameOf(oldFindSite(sites, c.url))
		newGot := siteNameOf(p.FindSite(c.url))
		if oldGot != c.expect {
			oldBad++
		}
		if newGot != c.expect {
			newBad++
		}
		t.Logf("%-60s | %-8s | %-8s | %-8s | %s", c.url, oldGot, newGot, c.expect, c.note)
	}
	t.Logf("矩阵用例 %d 条：修复前不符 %d 条，修复后不符 %d 条", len(matrixCases()), oldBad, newBad)
	if newBad > 0 {
		t.Errorf("修复后仍有 %d 条与期望不符", newBad)
	}
}

// oldFindSite 修复前的 FindSite 实现（按数组顺序遍历 + 正则对整条 URL 匹配），仅用于矩阵对照
func oldFindSite(sites []Site, link string) *Site {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return nil
	}
	host := strings.ToLower(u.Host)
	for i := range sites {
		s := &sites[i]
		if !s.Enabled {
			continue
		}
		for _, pre := range s.PrefixList() {
			if strings.HasPrefix(host, pre) {
				return s
			}
		}
		for _, rg := range s.RegexList() {
			if re, rerr := regexp.Compile(rg); rerr == nil && re.MatchString(link) {
				return s
			}
		}
	}
	return nil
}

// TestFindSiteSortOrder 验证 FindSite 按 sort 升序遍历（与界面排序一致），而非 rules.json 数组顺序
func TestFindSiteSortOrder(t *testing.T) {
	sites := []Site{
		{ID: "a", Name: "A-数组在前但sort大", URLPrefixes: []string{"abc.example.com"}, Enabled: true, Sort: 5},
		{ID: "b", Name: "B-数组在后但sort小", URLRegexes: []string{"abc"}, Enabled: true, Sort: 2},
	}
	p := NewParser(nil, sites, nil, "")
	got := siteNameOf(p.FindSite("https://abc.example.com/2024/index.m3u8"))
	t.Logf("双命中 URL 实际命中：%s（期望 B-数组在后但sort小）", got)
	if got != "B-数组在后但sort小" {
		t.Errorf("FindSite 未按 sort 升序遍历，实际命中 %s", got)
	}
}

// TestFindSiteRegexScope 验证正则默认只匹配 host，仅显式 ^ 或含 :// 时才匹配整条 URL
func TestFindSiteRegexScope(t *testing.T) {
	sites := []Site{
		{ID: "s1", Name: "泛化短串", URLRegexes: []string{"mtime\\d*"}, Enabled: true, Sort: 1},
		{ID: "s2", Name: "整条URL锚点", URLRegexes: []string{"^https?://cdn\\.example\\.com/live/"}, Enabled: true, Sort: 2},
		{ID: "s3", Name: "含协议正则", URLRegexes: []string{"https?://live\\.other\\.com"}, Enabled: true, Sort: 3},
	}
	p := NewParser(nil, sites, nil, "")
	cases := []struct {
		url    string
		expect string
	}{
		{"https://www.mtime.com/a.m3u8", "泛化短串"},           // host 命中
		{"https://cdn.example.com/mtime/a.m3u8", "-"},          // 仅路径含 mtime，不命中
		{"https://cdn.example.com/live/123.m3u8", "整条URL锚点"}, // ^ 锚点正则仍按整 URL 匹配
		{"https://live.other.com/x.m3u8", "含协议正则"},           // 含 :// 的正则仍按整 URL 匹配
	}
	bad := 0
	for _, c := range cases {
		got := siteNameOf(p.FindSite(c.url))
		if got != c.expect {
			bad++
			t.Logf("FAIL %s -> %s（期望 %s）", c.url, got, c.expect)
		} else {
			t.Logf("OK   %s -> %s", c.url, got)
		}
	}
	if bad > 0 {
		t.Errorf("正则作用域用例 %d 条不符", bad)
	}
}
