package web

import (
	"net/http"
	"strings"
	"time"

	"dytt_parser_go/internal/core"
	"dytt_parser_go/internal/store"
)

// handleClean 对外接口 /api/clean?url=xxx（对应 PHP api.php）
func (s *Server) handleClean(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	link := strings.TrimSpace(r.URL.Query().Get("url"))
	origin, cacheURL := "", ""

	if link == "" {
		writeJSON(w, http.StatusOK, &core.Res{Code: 404, Msg: "无url参数"})
		return
	}
	if !isHTTPLinkSafe(link) {
		writeJSON(w, http.StatusOK, &core.Res{Code: 404, Msg: "解析失败"})
		return
	}

	resp, detail := s.parser_().Clean(link, s.cacheBase(r))
	origin = resp.Origin
	cacheURL = resp.URL

	// 落解析记录
	rec := store.Record{
		Time:     time.Now().Format("2006-01-02 15:04:05"),
		URL:      link,
		Origin:   origin,
		Code:     resp.Code,
		Msg:      resp.Msg,
		Elapsed:  time.Since(start).Milliseconds(),
		Cached:   detail != nil && detail.FromCache,
		CacheURL: cacheURL,
	}
	if detail != nil && detail.Site != nil {
		rec.Site = detail.Site.Name
	}
	// 命中缓存时不出网，代理字段留空（避免显示上一次的残值）
	usedProxy := ""
	if detail == nil || !detail.FromCache {
		usedProxy = s.parser_().LastProxy(link)
	}
	rec.Proxy = usedProxy
	s.recs.Add(rec)
	reportProxyResult(s.parser_(), link, resp.Code == 200, resp.Msg)

	s.logger.Write("INFO", "api",
		"clean "+link+" -> "+origin+" code="+itoa(resp.Code)+" proxy="+proxyLabel(usedProxy)+
			" ("+time.Since(start).Round(time.Millisecond).String()+")")

	writeJSON(w, http.StatusOK, resp)
}

// reportProxyResult 把本次解析成败反馈给代理池（仅对池中代理生效，用于失败剔除/成功计数）
func reportProxyResult(p *core.Parser, url string, ok bool, reason string) {
	if p == nil {
		return
	}
	raw := p.LastProxy(url)
	if raw == "" {
		return
	}
	if ok {
		core.DefaultPool().ReportOK(raw)
	} else {
		core.DefaultPool().ReportFail(raw, reason)
	}
}

// proxyLabel 把代理地址转成日志可读标签：直连显示 direct
func proxyLabel(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "direct"
	}
	return raw
}

// isHTTPLinkSafe 本地判断 http(s) 前缀（core 内部同功能，避免导出）
func isHTTPLinkSafe(u string) bool {
	low := strings.ToLower(u)
	return strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")
}
