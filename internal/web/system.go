package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/core"
)

// ---------- 系统设置聚合 ----------

// handleSystemSettings GET 返回系统设置页所需全部数据
// （基础设置 + 缓存清理状态 + IP 规则 + 自定义接口 + 代理池状态）
func (s *Server) handleSystemSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	cfg := s.cfgMgr.Snapshot()
	cleanLast, cleanNext, cleanLastN := s.cleanState()
	files, bytes := s.cacheStat()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200,
		"settings": map[string]interface{}{
			"cache_ttl_days":      float64(cfg.Settings.CacheTTL) / 86400,
			"cache_auto_clean":    cfg.Settings.CacheAutoClean,
			"cache_clean_every_h": cfg.Settings.CacheCleanEveryH,
			"cache_clean_unit":    normCleanUnit(cfg.Settings.CacheCleanUnit),
			"cache_clean_types":   normCleanTypes(cfg.Settings.CacheCleanTypes),
			"cache_clean_sites":   cfg.Settings.CacheCleanSites,
			"cache_clean_days":    cfg.Settings.CacheCleanDays,
			"admin_path":          s.adminPath(),
			// 访问协议 / 跨域来源：必须回传，否则系统设置页永远显示默认值 auto / *
			"cache_scheme":      normCacheScheme(cfg.Settings.CacheScheme),
			"cors_allow_origin": cfg.Settings.CORSAllowOrigin,
			// 代理池
			"proxy_pool_enable":    cfg.Settings.ProxyPoolEnable,
			"proxy_pool_api":       cfg.Settings.ProxyPoolAPI,
			"proxy_pool_protocol":  cfg.Settings.ProxyPoolProtocol,
			"proxy_pool_count":     cfg.Settings.ProxyPoolCount,
			"proxy_pool_country":   cfg.Settings.ProxyPoolCountry,
			"proxy_pool_refresh_m": cfg.Settings.ProxyPoolRefreshM,
			"proxy_pool_strategy":  cfg.Settings.ProxyPoolStrategy,
			"proxy_pool_fail_max":  cfg.Settings.ProxyPoolFailMax,
			// IP 授权（纯白名单）
			"ip_access_enable": cfg.Settings.IPAccessEnable,
		},
		"cache": map[string]interface{}{
			"files":      files,
			"bytes":      bytes,
			"last":       cleanLast,
			"next":       cleanNext,
			"last_count": cleanLastN,
		},
		"ip_rules": cfg.IPRules,
		"ip_mode":  "white",
		// 自定义解析接口地址（默认 /api/clean + 自定义路径）
		"clean_paths":        cfg.CleanPaths,
		"clean_path_default": DefaultCleanPath,
		"clean_path_active":  s.activeCleanPaths(),
		"clean_path_samples": cleanSamples(r, s.activeCleanPaths()),
		"clean_types":        cleanTypeDescs(),
		"ip_enable":          cfg.Settings.IPAccessEnable,
		"custom_apis":        cfg.CustomAPIs,
		"proxy_pool":         core.DefaultPool().Status(),
	})
}

// cleanSamples 拼出各解析接口地址的完整示例（供前端直接展示/复制）
func cleanSamples(r *http.Request, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, cleanPathSample(r, p))
	}
	return out
}

// ---------- IP 授权 ----------

// handleIPRules GET 列表 / POST 新增
func (s *Server) handleIPRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfgMgr.Snapshot()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "ip_rules": cfg.IPRules,
			"enable": cfg.Settings.IPAccessEnable, "mode": "white",
		})
	case http.MethodPost:
		var f core.IPRule
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		f.IP = strings.TrimSpace(f.IP)
		if !core.ValidateIPRule(f.IP) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "IP 格式无效（支持 1.2.3.4 或 1.2.3.0/24）"})
			return
		}
		// IP 授权已改为纯白名单：所有规则一律为放行规则
		f.Mode = "allow"
		if f.ID == "" {
			f.ID = genID()
		}
		if err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.IPRules {
				if c.IPRules[i].IP == f.IP {
					return fmt.Errorf("该 IP 规则已存在")
				}
			}
			c.IPRules = append(c.IPRules, f)
			return nil
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": err.Error()})
			return
		}
		s.logger.Write("INFO", "admin", "新增 IP 规则 "+f.IP+" ("+f.Mode+") ["+clientIP(r)+"]")
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已新增 IP 规则", "id": f.ID})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
	}
}

// handleIPRuleItem PUT 更新 / DELETE 删除
func (s *Server) handleIPRuleItem(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, s.adminPath()+"/api/ip-rules/"), "/")
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缺少规则 ID"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var f core.IPRule
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		f.ID = id
		f.IP = strings.TrimSpace(f.IP)
		f.Mode = "allow" // 纯白名单：只允许放行规则
		if !core.ValidateIPRule(f.IP) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "IP 格式无效"})
			return
		}
		if err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.IPRules {
				if c.IPRules[i].ID == id {
					c.IPRules[i] = f
					return nil
				}
			}
			return errNotFound
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "规则不存在"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已保存"})
	case http.MethodDelete:
		if err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.IPRules {
				if c.IPRules[i].ID == id {
					c.IPRules = append(c.IPRules[:i], c.IPRules[i+1:]...)
					return nil
				}
			}
			return errNotFound
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "规则不存在"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已删除"})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
	}
}

// handleIPAccessSwitch POST 启用开关与模式：{enable, mode}
func (s *Server) handleIPAccessSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	var body struct {
		Enable *bool `json:"enable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
		return
	}
	if err := s.cfgMgr.Update(func(f *config.File) error {
		if body.Enable != nil {
			f.Settings.IPAccessEnable = *body.Enable
		}
		return nil
	}); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
		return
	}
	cfg := s.cfgMgr.Snapshot()
	s.logger.Write("INFO", "admin", fmt.Sprintf("IP 白名单：enable=%v", cfg.Settings.IPAccessEnable))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "msg": "已保存", "enable": cfg.Settings.IPAccessEnable, "mode": "white",
	})
}

// handleIPCheck POST {ip} 试判定某个 IP 是否放行（不写入配置）
func (s *Server) handleIPCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	var body struct {
		IP string `json:"ip"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	testIP := strings.TrimSpace(body.IP)
	if testIP == "" {
		testIP = clientIP(r)
	}
	cfg := s.cfgMgr.Snapshot()
	res := core.CheckIPAccess(cfg.IPRules, cfg.Settings.IPAccessEnable, testIP)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "ip": testIP, "allowed": res.Allowed, "reason": res.Reason, "rule": res.Rule,
	})
}

// ---------- 自定义接口 ----------

// handleCustomAPIs GET 列表 / POST 新增
func (s *Server) handleCustomAPIs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfgMgr.Snapshot()
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "custom_apis": cfg.CustomAPIs})
	case http.MethodPost:
		var f core.CustomAPI
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		f.Name = strings.TrimSpace(f.Name)
		f.URL = strings.TrimSpace(f.URL)
		if f.Name == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "接口名称不能为空"})
			return
		}
		if f.URL == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "接口地址不能为空"})
			return
		}
		if !isHTTPLinkSafe(f.URL) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "接口地址必须以 http:// 或 https:// 开头"})
			return
		}
		if f.Method == "" {
			f.Method = "GET"
		}
		f.Method = strings.ToUpper(f.Method)
		if f.ID == "" {
			f.ID = genID()
		}
		if err := s.cfgMgr.Update(func(c *config.File) error {
			c.CustomAPIs = append(c.CustomAPIs, f)
			return nil
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已新增接口", "id": f.ID})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
	}
}

// handleCustomAPIItem PUT 更新 / DELETE 删除
func (s *Server) handleCustomAPIItem(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, s.adminPath()+"/api/custom-apis/"), "/")
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缺少接口 ID"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var f core.CustomAPI
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		f.ID = id
		f.URL = strings.TrimSpace(f.URL)
		if f.URL != "" && !isHTTPLinkSafe(f.URL) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "接口地址必须以 http:// 或 https:// 开头"})
			return
		}
		if f.Method == "" {
			f.Method = "GET"
		}
		f.Method = strings.ToUpper(f.Method)
		if err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.CustomAPIs {
				if c.CustomAPIs[i].ID == id {
					c.CustomAPIs[i] = f
					return nil
				}
			}
			return errNotFound
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "接口不存在"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已保存"})
	case http.MethodDelete:
		if err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.CustomAPIs {
				if c.CustomAPIs[i].ID == id {
					c.CustomAPIs = append(c.CustomAPIs[:i], c.CustomAPIs[i+1:]...)
					return nil
				}
			}
			return errNotFound
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "接口不存在"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已删除"})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
	}
}

// handleCustomAPITest POST {id} 或 {url, method, params, headers} 实际调用一次接口并回显结果
func (s *Server) handleCustomAPITest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	var body core.CustomAPI
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
		return
	}
	if body.ID != "" {
		cfg := s.cfgMgr.Snapshot()
		found := false
		for _, a := range cfg.CustomAPIs {
			if a.ID == body.ID {
				body = a
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "接口不存在"})
			return
		}
	}
	if !isHTTPLinkSafe(strings.TrimSpace(body.URL)) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "接口地址必须以 http:// 或 https:// 开头"})
		return
	}
	start := time.Now()
	status, out, err := callCustomAPI(body, 15*time.Second)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "ok": false, "error": err.Error(), "elapsed_ms": ms})
		return
	}
	if len(out) > 4000 {
		out = out[:4000] + "\n...(响应已截断)"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "ok": status >= 200 && status < 300, "status": status, "body": out, "elapsed_ms": ms,
	})
}

// callCustomAPI 按配置实际请求一次自定义接口，返回状态码与前 64KB 文本
func callCustomAPI(a core.CustomAPI, timeout time.Duration) (int, string, error) {
	method := strings.ToUpper(strings.TrimSpace(a.Method))
	if method == "" {
		method = http.MethodGet
	}
	target := strings.TrimSpace(a.URL)
	params := strings.TrimSpace(a.Params)
	var reqBody io.Reader

	if method == http.MethodPost {
		if params != "" {
			low := strings.ToLower(params)
			if strings.HasPrefix(strings.TrimSpace(params), "{") || strings.HasPrefix(low, "{\"") {
				reqBody = strings.NewReader(params)
			} else {
				reqBody = strings.NewReader(params) // form 形式：由 Content-Type 决定
			}
		}
	} else {
		if method != http.MethodHead {
			method = http.MethodGet
		}
		if params != "" {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + strings.TrimLeft(params, "&?")
		}
	}

	req, err := http.NewRequest(method, target, reqBody)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) dytt-parser")
	if method == http.MethodPost {
		if strings.HasPrefix(strings.TrimSpace(params), "{") {
			req.Header.Set("Content-Type", "application/json")
		} else {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(a.Headers, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		req.Header.Set(strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]))
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	return resp.StatusCode, string(data), nil
}

// ---------- 代理池 ----------

// handleProxyPool GET 查看当前池状态
func (s *Server) handleProxyPool(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": core.DefaultPool().Status()})
}

// handleProxyPoolRefresh POST 立即刷新代理池
func (s *Server) handleProxyPoolRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	// 以最新配置刷新（可能刚保存完还没 reload）
	cfg := s.cfgMgr.Snapshot()
	core.DefaultPool().ApplySettings(&cfg.Settings)
	n, err := core.DefaultPool().Refresh()
	if err != nil {
		s.logger.Write("WARN", "admin", "代理池刷新失败: "+err.Error())
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "刷新失败: " + err.Error()})
		return
	}
	s.logger.Write("INFO", "admin", "代理池刷新，当前 "+itoa(n)+" 个代理")
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已刷新，当前 " + itoa(n) + " 个代理", "data": core.DefaultPool().Status()})
}

// handleProxyPoolRemove POST 从池中移除指定代理：{proxy}
func (s *Server) handleProxyPoolRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	var body struct {
		Proxy string `json:"proxy"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	p := strings.TrimSpace(body.Proxy)
	if p == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 400, "msg": "请选择要删除的代理"})
		return
	}
	if !core.DefaultPool().Remove(p) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "池中未找到该代理，可先刷新列表"})
		return
	}
	s.logger.Write("INFO", "admin", "从代理池移除 "+p)
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已移除该代理", "data": core.DefaultPool().Status()})
}

// handleProxyPoolTest POST 测试代理池连通性：{proxy} 指定单个，留空则测池中所有代理
func (s *Server) handleProxyPoolTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	var body struct {
		Proxy string `json:"proxy"`
		URL   string `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cfg := s.cfgMgr.Snapshot()
	ua := cfg.Settings.UserAgent
	testURL := strings.TrimSpace(body.URL)
	if testURL == "" {
		// 未指定时用内置探测地址测（https 且回显出口 IP），
		// 避免用明文地址把不支持 HTTPS 隧道的代理误判为可用
		testURL = core.ProxyProbeURL
	}

	targets := []string{}
	if p := strings.TrimSpace(body.Proxy); p != "" {
		targets = append(targets, core.NormalizeProxyAddr(p, cfg.Settings.ProxyPoolProtocol))
	} else {
		for _, it := range core.DefaultPool().Status().Items {
			targets = append(targets, it.Raw)
		}
	}
	if len(targets) == 0 {
		// 池为空时按当前配置取一个（会自动触发一次拉取）
		if p := core.DefaultPool().Take(); p != "" {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "代理池为空，请先刷新"})
		return
	}

	type item struct {
		Proxy     string `json:"proxy"`
		OK        bool   `json:"ok"`
		Status    int    `json:"status"`
		ElapsedMS int64  `json:"elapsed_ms"`
		Error     string `json:"error,omitempty"`
	}
	results := make([]item, 0, len(targets))
	for _, p := range targets {
		res := core.TestProxy(p, testURL, 15*time.Second, ua)
		results = append(results, item{Proxy: p, OK: res.OK, Status: res.Status, ElapsedMS: res.ElapsedMS, Error: res.Err})
		if res.OK {
			core.DefaultPool().ReportOK(p)
		} else {
			core.DefaultPool().ReportFail(p, res.Err)
		}
	}
	s.logger.Write("INFO", "admin", fmt.Sprintf("代理池连通性测试 %d 个", len(results)))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "results": results, "data": core.DefaultPool().Status(),
	})
}
