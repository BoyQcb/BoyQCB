package web

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dytt_parser_go/internal/core"
)

// previewEntry 一次解析测试的结果快照，供播放预览与片段查看复用（避免重复解析）
type previewEntry struct {
	Token  string
	Origin string
	Clean  string
	Base   string
	Segs   []core.SegmentView
	Expire time.Time
}

const previewTTL = 30 * time.Minute

var keyURIRe = regexp.MustCompile(`(?i)URI="([^"]+)"`)

// putPreview 缓存解析结果并返回凭证 token
func (s *Server) putPreview(res *core.PreviewResult) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)

	now := time.Now()
	s.prevMu.Lock()
	for k, v := range s.previews {
		if v.Expire.Before(now) {
			delete(s.previews, k)
		}
	}
	s.previews[token] = &previewEntry{
		Token:  token,
		Origin: res.OriginText,
		Clean:  res.CleanText,
		Base:   res.BaseURL,
		Segs:   res.Segments,
		Expire: now.Add(previewTTL),
	}
	s.prevMu.Unlock()
	return token
}

func (s *Server) getPreview(token string) *previewEntry {
	if token == "" {
		return nil
	}
	s.prevMu.Lock()
	defer s.prevMu.Unlock()
	e, ok := s.previews[token]
	if !ok {
		return nil
	}
	if e.Expire.Before(time.Now()) {
		delete(s.previews, token)
		return nil
	}
	return e
}

// handleTestPlaylist 生成可播放的 m3u8：type=clean（过滤后）/ origin（原始）
// 片段地址统一指向本地代理 /api/test/ts，规避跨域与源站防盗链
func (s *Server) handleTestPlaylist(w http.ResponseWriter, r *http.Request) {
	e := s.getPreview(r.URL.Query().Get("token"))
	if e == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "预览已过期，请重新解析"})
		return
	}
	kind := r.URL.Query().Get("type")
	if kind != "origin" {
		kind = "clean"
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, buildPlaylist(e, kind, s.adminPath()))
}

// handleTestSegment 生成仅含指定片段的 m3u8，用于单片段播放
func (s *Server) handleTestSegment(w http.ResponseWriter, r *http.Request) {
	e := s.getPreview(r.URL.Query().Get("token"))
	if e == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "预览已过期，请重新解析"})
		return
	}
	idx, _ := strconv.Atoi(r.URL.Query().Get("i"))
	var seg *core.SegmentView
	for i := range e.Segs {
		if e.Segs[i].Index == idx {
			seg = &e.Segs[i]
			break
		}
	}
	if seg == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "片段不存在"})
		return
	}
	target := int(seg.Dur)
	if target < 1 {
		target = 10
	}
	body := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:" + strconv.Itoa(target) +
		"\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:" +
		strconv.FormatFloat(seg.Dur, 'f', 3, 64) + ",\n" + s.adminPath() +
		"/api/test/ts?token=" + e.Token + "&i=" + strconv.Itoa(seg.Index) + "\n#EXT-X-ENDLIST\n"
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, body)
}

// handleTestTS 片段代理：按段号取绝对地址，后端代抓后转发（带 UA / Referer）
func (s *Server) handleTestTS(w http.ResponseWriter, r *http.Request) {
	e := s.getPreview(r.URL.Query().Get("token"))
	if e == nil {
		http.Error(w, "preview expired", http.StatusNotFound)
		return
	}
	idx, _ := strconv.Atoi(r.URL.Query().Get("i"))
	var abs string
	for _, sg := range e.Segs {
		if sg.Index == idx {
			abs = sg.URL
			break
		}
	}
	if abs == "" {
		http.Error(w, "segment not found", http.StatusNotFound)
		return
	}

	p := s.parser_()
	req, err := http.NewRequest(http.MethodGet, abs, nil)
	if err != nil {
		http.Error(w, "bad url", http.StatusBadRequest)
		return
	}
	if p.UA != "" {
		req.Header.Set("User-Agent", p.UA)
	}
	req.Header.Set("Referer", originOf(abs))
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}

	resp, err := p.Client.Do(req)
	if err != nil {
		http.Error(w, "fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "video/mp2t")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// buildPlaylist 将 m3u8 文本中的片段行替换为本地代理地址，并绝对化 KEY 的 URI
func buildPlaylist(e *previewEntry, kind, adminPath string) string {
	raw := e.Clean
	if kind == "origin" {
		raw = e.Origin
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")

	kept := make([]core.SegmentView, 0, len(e.Segs))
	for _, sg := range e.Segs {
		if kind == "origin" || !sg.Ad {
			kept = append(kept, sg)
		}
	}

	out := make([]string, 0, len(lines))
	k := 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t != "" && !strings.HasPrefix(t, "#") {
			if k < len(kept) {
				out = append(out, adminPath+"/api/test/ts?token="+e.Token+"&i="+strconv.Itoa(kept[k].Index))
				k++
			} else {
				out = append(out, l)
			}
			continue
		}
		if strings.HasPrefix(strings.ToUpper(t), "#EXT-X-KEY") {
			out = append(out, absolutizeKeyURI(l, e.Base))
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// absolutizeKeyURI 把 #EXT-X-KEY 中的相对 URI 补全为绝对地址
func absolutizeKeyURI(line, base string) string {
	if base == "" {
		return line
	}
	return keyURIRe.ReplaceAllStringFunc(line, func(m string) string {
		sub := keyURIRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		u := sub[1]
		if strings.HasPrefix(strings.ToLower(u), "http://") || strings.HasPrefix(strings.ToLower(u), "https://") {
			return m
		}
		return `URI="` + core.AbsFromDir(base, u) + `"`
	})
}

// originOf 取 scheme://host 作为 Referer
func originOf(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	rest := u[i+3:]
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return u[:i] + "://" + rest + "/"
}
