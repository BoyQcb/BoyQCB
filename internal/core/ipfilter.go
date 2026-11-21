package core

import (
	"net"
	"strings"
)

// IPAccessResult IP 访问判定结果
type IPAccessResult struct {
	Allowed bool    `json:"allowed"`
	Reason  string  `json:"reason"` // pass / denied / not_in_whitelist / disabled
	Rule    *IPRule `json:"rule,omitempty"`
}

// NormalizeIP 从可能带端口/空格/方括号的串中提取 IP
func NormalizeIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// 去掉 IPv6 方括号
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i > 0 {
			s = s[1:i]
		}
	}
	// 去掉端口（仅当只有一个冒号时视为 IPv4:port）
	if strings.Count(s, ":") == 1 {
		if i := strings.LastIndex(s, ":"); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// rulePrefixLen 返回规则的网段前缀长度与网络对象；非法规则返回 nil
func rulePrefixLen(rule string) (net.IPNet, int) {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return net.IPNet{}, -1
	}
	if strings.Contains(rule, "/") {
		_, n, err := net.ParseCIDR(rule)
		if err != nil {
			return net.IPNet{}, -1
		}
		ones, _ := n.Mask.Size()
		return *n, ones
	}
	ip := net.ParseIP(rule)
	if ip == nil {
		return net.IPNet{}, -1
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, bits
}

// MatchIPRule 在规则列表中找最具体（前缀最长）的匹配项；无匹配返回 nil
func MatchIPRule(rules []IPRule, ip string) *IPRule {
	target := net.ParseIP(NormalizeIP(ip))
	if target == nil {
		return nil
	}
	var best *IPRule
	bestLen := -1
	for i := range rules {
		r := rules[i]
		if !r.Enabled {
			continue
		}
		netw, ones := rulePrefixLen(r.IP)
		if ones < 0 {
			continue
		}
		if !netw.Contains(target) {
			continue
		}
		if ones > bestLen { // 前缀越长越精确，优先级越高
			bestLen = ones
			rr := r
			best = &rr
		}
	}
	return best
}

// CheckIPAccess 判定某个 IP 是否允许访问（纯白名单）。
// enable=false 时一律放行；启用后只有命中「已启用白名单规则」的 IP 才放行，其余一律拒绝。
func CheckIPAccess(rules []IPRule, enable bool, ip string) IPAccessResult {
	if !enable {
		return IPAccessResult{Allowed: true, Reason: "disabled"}
	}
	if r := MatchIPRule(rules, ip); r != nil {
		return IPAccessResult{Allowed: true, Reason: "pass", Rule: r}
	}
	return IPAccessResult{Allowed: false, Reason: "not_in_whitelist"}
}

// ValidateIPRule 校验 IP/CIDR 写法是否合法
func ValidateIPRule(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if strings.Contains(v, "/") {
		_, _, err := net.ParseCIDR(v)
		return err == nil
	}
	return net.ParseIP(v) != nil
}
