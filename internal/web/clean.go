package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/core"
)

// DefaultCleanPath 默认对外解析接口地址（始终生效，可在系统设置里追加自定义路径）
const DefaultCleanPath = "/api/clean"

// CleanResult 一次清理的结果（按类型统计）
type CleanResult struct {
	Total  int            `json:"total"`
	Detail map[string]int `json:"detail"`
	Labels []string       `json:"labels"`
}

// cleanTypeMeta 清理类型定义：value / 展示名 / 说明
type cleanTypeMeta struct {
	Value string
	Label string
	Desc  string
}

// cleanTypes 支持的缓存清理类型（顺序即展示顺序）
var cleanTypes = []cleanTypeMeta{
	{Value: "expired", Label: "过期缓存", Desc: "按缓存有效期清理已过期的 m3u8 缓存"},
	{Value: "all", Label: "全部缓存", Desc: "清空 cache 目录下所有站点子目录与根级缓存"},
	{Value: "site", Label: "指定站点缓存", Desc: "只清空所选站点缓存子目录内的缓存"},
	{Value: "root", Label: "根级散落缓存", Desc: "只清理不属于任何站点子目录的根级缓存"},
	{Value: "preview", Label: "预览/临时缓存", Desc: "清理缓存目录内的 .tmp 临时残留文件"},
	{Value: "seghash", Label: "片段采样哈希缓存", Desc: "清空 data/seghash.json 广告片段采样哈希缓存"},
}

// normCleanTypes 清洗并校验清理类型列表，非法值丢弃；空列表回落 expired
func normCleanTypes(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		if !validCleanType(v) {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return []string{"expired"}
	}
	return out
}

func validCleanType(v string) bool {
	for _, t := range cleanTypes {
		if t.Value == v {
			return true
		}
	}
	return false
}

// cleanTypeLabels 返回类型的中文名列表
func cleanTypeLabels(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range normCleanTypes(in) {
		for _, t := range cleanTypes {
			if t.Value == v {
				out = append(out, t.Label)
				break
			}
		}
	}
	if len(out) == 0 {
		return []string{"过期缓存"}
	}
	return out
}

// cleanTypeDescs 返回全部可选类型（前端下拉/多选使用）
func cleanTypeDescs() []cleanTypeMeta {
	out := make([]cleanTypeMeta, 0, len(cleanTypes))
	out = append(out, cleanTypes...)
	return out
}

// runCleanByTypes 按类型执行清理，返回各类型删除数量。days>0 时对缓存类按 N 天前清理
func (s *Server) runCleanByTypes(types []string, sites []string, days int) CleanResult {
	types = normCleanTypes(types)
	res := CleanResult{Detail: map[string]int{}, Labels: cleanTypeLabels(types)}
	c := s.parser_().Cache
	if c == nil {
		return res
	}
	siteList := normCacheSites(sites)
	for _, t := range types {
		n := 0
		switch t {
		case "expired":
			if days > 0 {
				n = c.CleanByDays(days)
			} else {
				n = c.CleanExpired()
			}
		case "all":
			if days > 0 {
				n = c.CleanByDays(days)
			} else {
				n = c.CleanAll()
			}
		case "site":
			if len(siteList) == 0 {
				siteList = s.allCacheSubdirs()
			}
			n = c.CleanSites(siteList)
		case "root":
			n = c.CleanRoot()
		case "preview":
			n = c.CleanPreview()
		case "seghash":
			n = core.ClearSegHashCache()
		}
		res.Detail[t] = n
		res.Total += n
	}
	return res
}

// normCacheSites 清洗站点缓存目录名列表
func normCacheSites(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(strings.Trim(v, "/\\"))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// allCacheSubdirs 返回全部站点的缓存子目录（未指定站点时的兜底）
func (s *Server) allCacheSubdirs() []string {
	cfg := s.cfgMgr.Snapshot()
	out := make([]string, 0, len(cfg.Sites))
	for _, st := range cfg.Sites {
		sub := strings.TrimSpace(strings.Trim(st.CacheSubdir, "/\\"))
		if sub != "" {
			out = append(out, sub)
		}
	}
	return out
}

// readJSONBody 解析 JSON 请求体，失败时写回错误响应并返回 false
func readJSONBody(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": "请求体解析失败：" + err.Error()})
		return false
	}
	return true
}

/* ============================================================
   对外解析接口地址自定义（默认 /api/clean + 自定义路径）
   ============================================================ */

// activeCleanPaths 返回全部生效的解析接口路径（列表内 enabled 的条目，均已规范化）。
// 列表为空或全部被禁用时回落默认地址，保证对外解析接口不会整体失效。
func (s *Server) activeCleanPaths() []string {
	cfg := s.cfgMgr.Snapshot()
	out := make([]string, 0, len(cfg.CleanPaths)+1)
	seen := map[string]bool{}
	for _, p := range cfg.CleanPaths {
		if !p.Enabled {
			continue
		}
		np, err := normCleanPath(p.Path)
		if err != nil {
			continue
		}
		key := strings.ToLower(np)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, np)
	}
	if len(out) == 0 {
		return []string{DefaultCleanPath}
	}
	return out
}

// isCleanPath 判断请求路径是否为生效中的解析接口地址
func (s *Server) isCleanPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	path = "/" + strings.Trim(path, "/")
	low := strings.ToLower(path)
	for _, p := range s.activeCleanPaths() {
		if strings.ToLower(p) == low {
			return true
		}
	}
	return false
}

// normCleanPath 规范化接口路径：补前导斜杠、去尾斜杠、避开保留段与后台入口
func normCleanPath(raw string, adminPath ...string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("接口地址不能为空")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = "/" + strings.Trim(p, "/")
	if p == "/" || len(p) < 2 {
		return "", fmt.Errorf("接口地址无效")
	}
	if strings.ContainsAny(p, " \t?#\\") {
		return "", fmt.Errorf("接口地址不能包含空格、?、# 或反斜杠")
	}
	low := strings.ToLower(p)
	for _, r := range []string{"cache", "static"} {
		if low == "/"+r || strings.HasPrefix(low, "/"+r+"/") {
			return "", fmt.Errorf("接口地址不能占用保留段 /%s", r)
		}
	}
	for _, a := range adminPath {
		a = "/" + strings.Trim(a, "/")
		if a != "/" && (low == a || strings.HasPrefix(low, a+"/")) {
			return "", fmt.Errorf("接口地址不能与后台入口 %s 冲突", a)
		}
	}
	return p, nil
}

// requestScheme 依据请求推断对外协议（自适应：直连 TLS / 反代透传头 / CDN 头 / Referer 兜底）
func requestScheme(r *http.Request) string {
	// 反代透传头优先（多层代理时以最外层访客协议为准）
	if v := firstScheme(r,
		"X-Forwarded-Proto",
		"X-Forwarded-Scheme",
		"X-Url-Scheme",
		"X-Client-Scheme",
		"Front-End-Https",
	); v != "" {
		return v
	}
	// Cloudflare 把访客协议放在 CF-Visitor 的 JSON 里
	if v := strings.ToLower(r.Header.Get("CF-Visitor")); strings.Contains(v, "\"scheme\":\"https\"") {
		return "https"
	}
	// RFC 7239 Forwarded: proto=https
	if v := forwardedProto(r); v != "" {
		return v
	}
	if r.TLS != nil {
		return "https"
	}
	// 兜底：https 页面发起的跨域请求一定带 https 的 Origin/Referer
	for _, h := range []string{"Origin", "Referer"} {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get(h))), "https://") {
			return "https"
		}
	}
	// 兜底：跟随本次连接的实际协议。没有反代头、也没有 TLS 时即为明文 http 访问，
	// 此时必须回 http 链接；若站点实际是全站 HTTPS（CDN/反代强制跳转），
	// 一般由反代透传 X-Forwarded-Proto，或直接在后台把「访问协议」显式设为 https。
	return "http"
}

// cleanPathSample 拼出可对外访问的完整接口示例地址
func cleanPathSample(r *http.Request, path string) string {
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	return requestScheme(r) + "://" + host + path + "?url=视频m3u8地址"
}

// handleCleanPaths 自定义解析接口地址列表（GET 列表 / POST 新增或更新）
func (s *Server) handleCleanPaths(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfgMgr.Snapshot()
		paths := make([]core.CleanPath, 0, len(cfg.CleanPaths))
		for _, p := range cfg.CleanPaths {
			if np, err := normCleanPath(p.Path); err == nil {
				p.Path = np
			}
			paths = append(paths, p)
		}
		samples := make([]string, 0, len(cfg.CleanPaths)+1)
		for _, p := range s.activeCleanPaths() {
			samples = append(samples, cleanPathSample(r, p))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200,
			"data": map[string]interface{}{
				"paths":     paths,
				"default":   DefaultCleanPath,
				"active":    s.activeCleanPaths(),
				"samples":   samples,
				"sample":    cleanPathSample(r, DefaultCleanPath),
				"types":     cleanTypeDescs(),
			},
		})
	case http.MethodPost:
		var body core.CleanPath
		if !readJSONBody(w, r, &body) {
			return
		}
		if !s.saveCleanPath(w, r, &body) {
			return
		}
		s.logger.Write("INFO", "admin", "保存自定义解析接口地址 "+body.Path+"（"+body.Name+"）")
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已保存", "data": body})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 405, "msg": "方法不允许"})
	}
}

// handleCleanPathItem 修改 / 删除单条自定义接口地址
func (s *Server) handleCleanPathItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, s.adminPath()+"/api/clean-paths/")
	id = strings.Trim(id, "/")
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": "缺少 ID"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var body core.CleanPath
		if !readJSONBody(w, r, &body) {
			return
		}
		body.ID = id
		if !s.saveCleanPath(w, r, &body) {
			return
		}
		s.logger.Write("INFO", "admin", "保存解析接口地址 "+body.Path+"（"+body.Name+"）")
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已保存", "data": body})
	case http.MethodDelete:
		if id == config.DefaultCleanPathID {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": "默认接口地址不可删除"})
			return
		}
		found := false
		err := s.cfgMgr.Update(func(f *config.File) error {
			// 至少保留一条启用中的接口地址：删掉后若无启用项则拒绝
			enabled := 0
			for _, p := range f.CleanPaths {
				if p.ID == id {
					continue
				}
				if p.Enabled {
					enabled++
				}
			}
			if enabled == 0 {
				return fmt.Errorf("至少需要保留一条启用中的接口地址")
			}
			kept := make([]core.CleanPath, 0, len(f.CleanPaths))
			for _, p := range f.CleanPaths {
				if p.ID == id {
					found = true
					continue
				}
				kept = append(kept, p)
			}
			f.CleanPaths = kept
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "接口地址不存在"})
			return
		}
		s.logger.Write("INFO", "admin", "删除自定义解析接口地址 id="+id)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已删除"})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 405, "msg": "方法不允许"})
	}
}

// saveCleanPath 校验并持久化一条自定义接口地址，失败时写回错误响应
func (s *Server) saveCleanPath(w http.ResponseWriter, r *http.Request, body *core.CleanPath) bool {
	_ = r
	body.Name = strings.TrimSpace(body.Name)
	body.Note = strings.TrimSpace(body.Note)
	np, err := normCleanPath(body.Path, s.adminPath())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": err.Error()})
		return false
	}
	body.Path = np
	if body.ID == config.DefaultCleanPathID {
		// 内置默认接口：地址固定为 /api/clean，只允许改备注名与说明
		body.Path = config.DefaultCleanPath
	}
	if body.Name == "" {
		body.Name = "自定义接口"
	}
	if body.ID == "" {
		body.ID = genID()
		body.Enabled = true
	}
	err = s.cfgMgr.Update(func(f *config.File) error {
		// 同一路径只允许一条（避免重复注册）
		for i := range f.CleanPaths {
			if f.CleanPaths[i].ID == body.ID {
				continue
			}
			if old, e := normCleanPath(f.CleanPaths[i].Path); e == nil && strings.EqualFold(old, body.Path) {
				return fmt.Errorf("接口地址 %s 已存在", body.Path)
			}
		}
		// 至少保留一条启用中的接口地址：禁用最后一条启用项时拒绝
		if !body.Enabled {
			enabled := 0
			for _, p := range f.CleanPaths {
				if p.ID != body.ID && p.Enabled {
					enabled++
				}
			}
			if enabled == 0 {
				return fmt.Errorf("至少需要保留一条启用中的接口地址")
			}
		}
		for i := range f.CleanPaths {
			if f.CleanPaths[i].ID == body.ID {
				f.CleanPaths[i] = *body
				return nil
			}
		}
		f.CleanPaths = append(f.CleanPaths, *body)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": "保存失败：" + err.Error()})
		return false
	}
	return true
}

// handleCleanPathTest 校验自定义接口地址是否已实际生效（本地回环请求一次）
func (s *Server) handleCleanPathTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 405, "msg": "方法不允许"})
		return
	}
	var body struct {
		Path string `json:"path"`
		URL  string `json:"url"`
		Raw  string `json:"raw"`
	}
	if !readJSONBody(w, r, &body) {
		return
	}
	raw := strings.TrimSpace(body.Raw)
	if raw == "" {
		raw = strings.TrimSpace(body.Path)
	}
	np, err := normCleanPath(raw, s.adminPath())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": err.Error()})
		return
	}
	registered := s.isCleanPath(np)
	out := map[string]interface{}{
		"code":       200,
		"path":       np,
		"registered": registered,
		"sample":     cleanPathSample(r, np),
		"msg":        "接口地址已生效",
	}
	if !registered {
		out["msg"] = "该地址尚未生效（未保存或被停用）"
	}
	// 传入 url 时本地回环请求一次，确认请求进来能正常被解析接口处理
	if u := strings.TrimSpace(body.URL); u != "" {
		target := "http://127.0.0.1" + listenPort(s.listen) + np + "?url=" + url.QueryEscape(u)
		start := time.Now()
		client := &http.Client{Timeout: 20 * time.Second}
		resp, err := client.Get(target)
		elapsed := time.Since(start).Milliseconds()
		out["elapsed_ms"] = elapsed
		out["target"] = target
		if err != nil {
			out["ok"] = false
			out["msg"] = "本地请求失败：" + err.Error()
			writeJSON(w, http.StatusOK, out)
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		out["status"] = resp.StatusCode
		out["ok"] = resp.StatusCode == 200
		out["body"] = string(buf[:n])
		if resp.StatusCode != 200 {
			out["msg"] = "请求未返回 200（HTTP " + strconv.Itoa(resp.StatusCode) + "）"
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// listenPort 由监听地址提取带冒号的端口段，形如 :8080
func listenPort(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return ""
	}
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		p := listen[i+1:]
		if _, err := strconv.Atoi(p); err == nil {
			return ":" + p
		}
	}
	return ""
}
