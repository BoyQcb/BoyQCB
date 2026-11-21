package core

import (
	"net/url"
	"strings"
)

// DirOf 返回 URL 的目录部分：去掉 query/fragment 与最后一段文件名。
func DirOf(u string) string {
	s := u
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[:i]
	}
	return s
}

// AbsURL 将 ref 按 base 绝对化，覆盖 m3u8 中各种地址写法：
//   - http(s):// 开头      → 原样返回
//   - //host/path（协议相对）→ 补 base 的 scheme
//   - /path（根绝对路径）   → scheme://host/path
//   - 其它（相对路径）      → base 目录 + "/" + ref
//
// 旧实现仅做 base+"/"+ref，遇到根绝对路径会拼出双斜杠错误地址。
func AbsURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ref
	}
	if httpRe.MatchString(ref) {
		return ref
	}
	u, err := url.Parse(base)
	if err != nil {
		if strings.HasPrefix(ref, "/") {
			return strings.TrimSuffix(base, "/") + ref
		}
		return DirOf(base) + "/" + ref
	}
	if strings.HasPrefix(ref, "//") {
		return u.Scheme + ":" + ref
	}
	if strings.HasPrefix(ref, "/") {
		return u.Scheme + "://" + u.Host + ref
	}
	return DirOf(base) + "/" + ref
}

// AbsFromDir resolves ref against a base that is ALREADY a directory
// (e.g. https://host/a/b/2000k/hls). Callers that hold Playlist.Base must use
// this instead of AbsURL: AbsURL treats the base as a FILE url and strips one
// more path level via DirOf, which breaks every relative segment url.
func AbsFromDir(dir, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || httpRe.MatchString(ref) {
		return ref
	}
	d := dir
	if !strings.HasSuffix(d, "/") {
		d += "/"
	}
	u, err := url.Parse(d)
	if err != nil {
		return strings.TrimSuffix(dir, "/") + "/" + strings.TrimPrefix(ref, "/")
	}
	r, err := u.Parse(ref)
	if err != nil {
		return strings.TrimSuffix(dir, "/") + "/" + strings.TrimPrefix(ref, "/")
	}
	return r.String()
}

// SameHost 判断两个 URL 是否同主机（用于判断是否跨域跳转）
func SameHost(a, b string) bool {
	ua, ea := url.Parse(a)
	ub, eb := url.Parse(b)
	if ea != nil || eb != nil {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host)
}
