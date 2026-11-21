package core

import "regexp"
import "strings"

var numSuffixRe = regexp.MustCompile(`\d+$`)

// HostMain 返回域名主体（小写二级名，保留尾部编号），如 v.lzcdn27.com → lzcdn27
func HostMain(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return strings.ToLower(parts[len(parts)-2])
	}
	return strings.ToLower(host)
}

// GenericName 返回去掉尾部编号的通用主体，如 lzcdn27 → lzcdn
func GenericName(host string) string {
	main := HostMain(host)
	g := numSuffixRe.ReplaceAllString(main, "")
	if g == "" {
		return main
	}
	return g
}

// GenericRegex 返回可兜底同系列编号域名的泛化正则，如 lzcdn27 → lzcdn\d*
func GenericRegex(host string) string {
	return regexp.QuoteMeta(GenericName(host)) + `\d*`
}

// SlugID 把域名转为可作站点 ID 的标识，如 s3.bfllvip.com → s3-bfllvip-com
func SlugID(host string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "site"
	}
	return out
}

// ---------- 域名族泛化（域名发现页「泛化建议」用） ----------
//
// 采集接口只能扫到当前在用的几个编号域名（如 cdn.yzzy30-play.com、cdn.yzzy31-play.com），
// 靠翻页永远穷举不完。这里按「域名骨架」把同族域名归并，只对编号位生成 \d+，
// 得到比 GenericRegex 精确得多的正则，避免误伤其它站点。

var digPlusRe = regexp.MustCompile(`\d+`)

// segToken 域名某一段的切分结果：数字块与非数字块交替
type segToken struct {
	num  bool
	text string
}

// tokenizeSeg 把 cdn.yzzy30-play.com 中的一段切分为 token，如 yzzy30-play → [yzzy][30][-play]
func tokenizeSeg(s string) []segToken {
	out := make([]segToken, 0, 4)
	last := 0
	for _, m := range digPlusRe.FindAllStringIndex(s, -1) {
		if m[0] > last {
			out = append(out, segToken{false, s[last:m[0]]})
		}
		out = append(out, segToken{true, s[m[0]:m[1]]})
		last = m[1]
	}
	if last < len(s) {
		out = append(out, segToken{false, s[last:]})
	}
	return out
}

// segTokensEqual 判断两段的结构是否一致（数字/非数字分布相同且非数字文本相同）
func segTokensEqual(a, b []segToken) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].num != b[i].num {
			return false
		}
		if !a[i].num && a[i].text != b[i].text {
			return false
		}
	}
	return true
}

// segRegex 由 token 序列生成该段的正则片段：数字位写 \d+，其余转义
func segRegex(t []segToken) string {
	var b strings.Builder
	for _, tk := range t {
		if tk.num {
			b.WriteString(`\d+`)
		} else {
			b.WriteString(regexp.QuoteMeta(tk.text))
		}
	}
	return b.String()
}

// HostSkeleton 返回域名骨架：各段去掉数字，数字位记为 #，如 cdn.yzzy30-play.com → cdn.yzzy#-play.com
func HostSkeleton(host string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(host)), ".")
	for i, p := range parts {
		var b strings.Builder
		for _, tk := range tokenizeSeg(p) {
			if tk.num {
				b.WriteString("#")
			} else {
				b.WriteString(tk.text)
			}
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, ".")
}

// HostFamilyRegex 对同族域名生成精确泛化正则，仅编号位写 \d+。
// 例：cdn.yzzy30-play.com + cdn.yzzy31-play.com → cdn\.yzzy\d+\-play\.com
// 段数不一致或结构无法对齐时返回空串（调用方应退回到 GenericRegex）。
func HostFamilyRegex(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	first := strings.Split(strings.ToLower(strings.TrimSpace(hosts[0])), ".")
	n := len(first)
	cols := make([][]string, n)
	for _, h := range hosts {
		p := strings.Split(strings.ToLower(strings.TrimSpace(h)), ".")
		if len(p) != n {
			return ""
		}
		for i, s := range p {
			cols[i] = append(cols[i], s)
		}
	}
	parts := make([]string, 0, n)
	for _, col := range cols {
		same := true
		for _, s := range col[1:] {
			if s != col[0] {
				same = false
				break
			}
		}
		if same {
			parts = append(parts, regexp.QuoteMeta(col[0]))
			continue
		}
		base := tokenizeSeg(col[0])
		ok := true
		for _, s := range col[1:] {
			if !segTokensEqual(base, tokenizeSeg(s)) {
				ok = false
				break
			}
		}
		if !ok {
			return ""
		}
		parts = append(parts, segRegex(base))
	}
	return strings.Join(parts, `\.`)
}
