package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/core"
	"dytt_parser_go/internal/store"
)

// ---------- 认证 ----------

// handleLogin 后台登录（admin/admin123，哈希校验）
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cfg := s.cfgMgr.Snapshot()
	if !config.VerifyPassword(body.Password, cfg.AdminHash) {
		s.logger.Write("WARN", "admin", "登录失败(密码错误) ["+clientIP(r)+"]")
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "密码错误"})
		return
	}
	tok, _ := s.sessions.Issue()
	http.SetCookie(w, &http.Cookie{
		Name:     "dytt_admin",
		Value:    tok,
		Path:     s.basePath + "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
	s.logger.Write("INFO", "admin", "登录成功 ["+clientIP(r)+"]")
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "登录成功"})
}

// handleLogout 登出
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie("dytt_admin"); err == nil {
		s.sessions.Revoke(ck.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "dytt_admin", Value: "", Path: s.basePath + "/", HttpOnly: true, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已退出"})
}

// handleMe 登录态探测
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie("dytt_admin")
	logged := err == nil && s.sessions.Valid(ck.Value)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "logged": logged, "version": core.Version,
	})
}

// handlePassword 修改后台密码
func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cfg := s.cfgMgr.Snapshot()
	if !config.VerifyPassword(body.Old, cfg.AdminHash) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "原密码错误"})
		return
	}
	if len(body.New) < 4 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "新密码长度至少 4 位"})
		return
	}
	if err := s.cfgMgr.Update(func(f *config.File) error {
		f.AdminHash = config.HashPassword(body.New)
		return nil
	}); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
		return
	}
	s.logger.Write("INFO", "admin", "后台密码已修改 ["+clientIP(r)+"]")
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "密码已修改"})
}

// ---------- 站点管理 ----------

// normalizeMatchers 归一化 URL 匹配前缀/正则：新旧字段互相同步，保证单值与多值一致
func normalizeMatchers(f *core.Site) {
	pre := f.PrefixList()
	rg := f.RegexList()
	f.URLPrefixes = pre
	f.URLRegexes = rg
	f.URLPrefix = ""
	f.URLRegex = ""
	if len(pre) > 0 {
		f.URLPrefix = pre[0]
	}
	if len(rg) > 0 {
		f.URLRegex = rg[0]
	}
}

// handleSitesList GET 列表 / POST 新增
func (s *Server) handleSitesList(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfgMgr.Snapshot()
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "sites": cfg.Sites})
	case http.MethodPost:
		var f core.Site
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		if strings.TrimSpace(f.Name) == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "站点名不能为空"})
			return
		}
		if f.ID == "" {
			f.ID = genID()
		}
		if f.CacheSubdir == "" {
			f.CacheSubdir = f.ID
		}
		f.ProxyURL = strings.TrimSpace(f.ProxyURL)
		if err := checkProxyURL(f.ProxyURL); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": err.Error()})
			return
		}
		normalizeMatchers(&f)
		if err := s.cfgMgr.Update(func(c *config.File) error {
			// 新增站点排序：未指定时取现有最大 sort+1（避免新站排到最前）
			if f.Sort <= 0 {
				maxSort := 0
				for _, st := range c.Sites {
					if st.Sort > maxSort {
						maxSort = st.Sort
					}
				}
				f.Sort = maxSort + 1
			}
			c.Sites = append(c.Sites, f)
			return nil
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败"})
			return
		}
		s.reload()
		s.ensureCacheSubdir(f.CacheSubdir) // 首次添加：新建缓存子目录
		s.logger.Write("INFO", "admin", "新增站点 ["+f.Name+"] "+f.ID)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已新增站点", "id": f.ID})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
	}
}

// handleSitesItem PUT 更新 / DELETE 删除
func (s *Server) handleSitesItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, s.adminPath()+"/api/sites/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缺少站点 ID"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		// 站点表单不管理「保护指纹 / 内容哈希」（由测试页规则学习写入），
		// 而本接口是整体替换，缺省时继承旧值，避免学习成果被保存站点时清掉
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		rawBytes, _ := json.Marshal(raw)
		var f core.Site
		if err := json.Unmarshal(rawBytes, &f); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		if old, ok := s.findSite(id); ok {
			if _, has := raw["keep_fingerprint"]; !has {
				f.KeepFingerprint = old.KeepFingerprint
			}
			if _, has := raw["keep_fingerprints"]; !has {
				f.KeepFingerprints = old.KeepFingerprints
			}
			if _, has := raw["ads_hashes"]; !has {
				f.AdsHashes = old.AdsHashes
			}
		}
		f.ID = id
		if f.CacheSubdir == "" {
			f.CacheSubdir = id
		}
		f.ProxyURL = strings.TrimSpace(f.ProxyURL)
		if err := checkProxyURL(f.ProxyURL); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": err.Error()})
			return
		}
		normalizeMatchers(&f)
		oldSub := ""
		if old, ok := s.findSite(id); ok {
			oldSub = old.CacheSubdir
		}
		err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.Sites {
				if c.Sites[i].ID == id {
					c.Sites[i] = f
					return nil
				}
			}
			return errNotFound
		})
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "站点不存在"})
			return
		}
		s.reload()
		s.renameCacheSubdir(oldSub, f.CacheSubdir) // 缓存子目录改名：跟随重命名
		s.logger.Write("INFO", "admin", "更新站点 ["+f.Name+"] "+id)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已保存"})
	case http.MethodDelete:
		oldSub := ""
		if old, ok := s.findSite(id); ok {
			oldSub = old.CacheSubdir
		}
		err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.Sites {
				if c.Sites[i].ID == id {
					c.Sites = append(c.Sites[:i], c.Sites[i+1:]...)
					return nil
				}
			}
			return errNotFound
		})
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "站点不存在"})
			return
		}
		s.reload()
		s.removeCacheSubdir(oldSub) // 删除规则：一并删除该站点缓存目录
		s.logger.Write("WARN", "admin", "删除站点 "+id)
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已删除"})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
	}
}

// findSite 按 ID 查找站点规则
func (s *Server) findSite(id string) (core.Site, bool) {
	cfg := s.cfgMgr.Snapshot()
	for _, st := range cfg.Sites {
		if st.ID == id {
			return st, true
		}
	}
	return core.Site{}, false
}

// ---------- 站点规则 导出 / 导入 ----------

// handleSitesExport GET /api/sites/export?ids=a,b（缺省导出全部规则）
// 返回带格式版本号的 JSON 结构，前端下载保存为 *.json 文件
func (s *Server) handleSitesExport(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfgMgr.Snapshot()
	sites := cfg.Sites
	if ids := strings.TrimSpace(r.URL.Query().Get("ids")); ids != "" {
		want := map[string]bool{}
		for _, id := range strings.Split(ids, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				want[id] = true
			}
		}
		out := make([]core.Site, 0, len(want))
		for _, st := range sites {
			if want[st.ID] {
				out = append(out, st)
			}
		}
		sites = out
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code":        200,
		"format":      "dytt-parser-sites",
		"version":     1,
		"exported_at": time.Now().Format(time.RFC3339),
		"count":       len(sites),
		"sites":       sites,
	})
}

// handleSitesImport POST /api/sites/import
// body: { sites: [核心 Site 结构...], mode: "append" | "replace" }
//   - append（默认）：导入的规则作为新规则追加；ID / 缓存子目录冲突时自动重生成，同名规则加 -N 后缀，不覆盖本地已有规则
//   - replace：整体替换全部规则（写前自动备份 rules.json，保留导入规则的 ID 与缓存子目录）
func (s *Server) handleSitesImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Sites []core.Site `json:"sites"`
		Mode  string      `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
		return
	}
	if len(body.Sites) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "没有可导入的站点规则"})
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode != "append" && mode != "replace" {
		mode = "append"
	}

	// 预校验：站点名、正则合法性、代理地址格式（任一不合法即整体中止，避免写入一半）
	for _, st := range body.Sites {
		if strings.TrimSpace(st.Name) == "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "存在站点名为空的规则，已中止导入"})
			return
		}
		if err := checkProxyURL(st.ProxyURL); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "站点「" + st.Name + "」代理地址无效: " + err.Error()})
			return
		}
		for _, rg := range st.RegexList() {
			if _, err := regexp.Compile(rg); err != nil {
				writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "站点「" + st.Name + "」正则无效: " + rg + "（" + err.Error() + "），已中止导入"})
				return
			}
		}
	}

	bak := ""
	if mode == "replace" {
		if b, err := os.ReadFile(filepath.Join(s.dataDir, "rules.json")); err == nil {
			bak = filepath.Join(s.dataDir, "rules.json.bak_import_"+time.Now().Format("20060102_150405"))
			_ = os.WriteFile(bak, b, 0644)
		}
	}

	added := 0
	skipped := 0
	usedID := map[string]bool{}
	usedSub := map[string]bool{}
	usedName := map[string]int{}
	if mode == "append" {
		cfg := s.cfgMgr.Snapshot()
		for _, st := range cfg.Sites {
			usedID[st.ID] = true
			if st.CacheSubdir != "" {
				usedSub[st.CacheSubdir] = true
			}
			usedName[st.Name]++
		}
	}

	imported := []core.Site{}
	err := s.cfgMgr.Update(func(c *config.File) error {
		if mode == "replace" {
			c.Sites = []core.Site{}
			usedID = map[string]bool{}
			usedSub = map[string]bool{}
			usedName = map[string]int{}
		}
		for _, src := range body.Sites {
			f := src
			normalizeMatchers(&f)
			if strings.TrimSpace(f.Name) == "" {
				skipped++
				continue
			}
			// 同名规则加 -N 后缀（append 不覆盖本地已有规则）
			if usedName[f.Name] > 0 {
				base := f.Name
				for i := 2; ; i++ {
					f.Name = fmt.Sprintf("%s-%d", base, i)
					if usedName[f.Name] == 0 {
						break
					}
				}
			}
			usedName[f.Name]++
			// ID 冲突重生成
			if f.ID == "" || usedID[f.ID] {
				f.ID = genID()
				for usedID[f.ID] {
					f.ID = genID()
				}
			}
			usedID[f.ID] = true
			// 缓存子目录：replace 保留导入值；append 冲突时跟随新 ID
			if f.CacheSubdir == "" {
				f.CacheSubdir = f.ID
			}
			if usedSub[f.CacheSubdir] {
				f.CacheSubdir = f.ID
			}
			usedSub[f.CacheSubdir] = true
			c.Sites = append(c.Sites, f)
			imported = append(imported, f)
			added++
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
		return
	}
	s.reload()
	for _, st := range imported {
		s.ensureCacheSubdir(st.CacheSubdir)
	}
	s.logger.Write("INFO", "admin", fmt.Sprintf("导入站点规则 %d 条（模式=%s 跳过%d）", added, mode, skipped))
	msg := fmt.Sprintf("已导入 %d 条站点规则", added)
	if mode == "replace" {
		msg += "（整体替换，原规则已备份）"
	}
	if skipped > 0 {
		msg += fmt.Sprintf("，跳过 %d 条（站点名为空）", skipped)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "msg": msg,
		"data": map[string]interface{}{"added": added, "skipped": skipped, "mode": mode, "backup": bak},
	})
}

// ---------- 新增站点向导：m3u8 自动探测 ----------

// handleProbe POST {url, index_mode, stream_pick, fixed_sub, hash_check, sample_size, concurrency}
// 对示例地址做结构与广告探测，返回建站建议（供新增站点表单一键填充）
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	var body struct {
		URL         string `json:"url"`
		IndexMode   string `json:"index_mode"`
		StreamPick  string `json:"stream_pick"`
		FixedSub    string `json:"fixed_sub"`
		HashCheck   bool   `json:"hash_check"`
		SampleSize  int    `json:"sample_size"`
		Concurrency int    `json:"concurrency"`
		ProxyURL    string `json:"proxy_url"` // 临时指定出网代理（空=按站点/全局规则）
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
		return
	}
	if strings.TrimSpace(body.URL) == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "请输入 m3u8 示例地址"})
		return
	}
	if body.SampleSize <= 0 {
		body.SampleSize = 4096
	}
	if body.Concurrency <= 0 {
		body.Concurrency = 12
	}
	p := s.parser_()
	if c := s.tempProxyClient(body.ProxyURL, p.Timeout); c != nil {
		p = p.CloneWithClient(c)
	}
	res := core.Probe(p.Client, body.URL, p.UA, p.Timeout, body.IndexMode, body.StreamPick,
		body.FixedSub, body.HashCheck, body.SampleSize, body.Concurrency)
	if !res.Ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": res.Msg})
		return
	}
	s.logger.Write("INFO", "admin", fmt.Sprintf("探测 %s：%d段/%.1fs/候选%d处", res.Host, res.TotalSegs, res.TotalDur, len(res.Candidates)))
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": res})
}

// ---------- M3U8 测试 ----------

// handleTest POST {url, site_id} 解析对比测试（不写缓存）
func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL      string `json:"url"`
		SiteID   string `json:"site_id"`
		ProxyURL string `json:"proxy_url"` // 临时指定出网代理（空=按站点/全局规则）
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.URL) == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "请输入 m3u8 地址"})
		return
	}
	p := s.parser_()
	if c := s.tempProxyClient(body.ProxyURL, p.Timeout); c != nil {
		p = p.CloneWithClient(c)
	}
	res := p.Preview(body.URL, body.SiteID)
	if !res.Ok {
		msg := res.Msg
		if px := strings.TrimSpace(body.ProxyURL); px != "" {
			msg += "（临时代理 " + px + "：" + s.proxyDiag(px, body.URL) + "）"
			// 临时代理的成败同样回写代理池统计（便于自动剔除不可用节点）
			cfg := s.cfgMgr.Snapshot()
			full := core.NormalizeProxyAddr(px, cfg.Settings.ProxyPoolProtocol)
			core.DefaultPool().ReportFail(full, res.Msg)
		} else {
			reportProxyResult(s.parser_(), body.URL, false, res.Msg)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": msg})
		return
	}
	if px := strings.TrimSpace(body.ProxyURL); px != "" {
		cfg := s.cfgMgr.Snapshot()
		core.DefaultPool().ReportOK(core.NormalizeProxyAddr(px, cfg.Settings.ProxyPoolProtocol))
	} else {
		reportProxyResult(s.parser_(), body.URL, true, "")
	}
	// 缓存本次解析结果，供播放预览 / 片段查看复用（30 分钟有效）
	res.Token = s.putPreview(res)
	s.logger.Write("INFO", "admin", fmt.Sprintf("解析测试 %s：%d段/广告%d段/命中%d处",
		res.SiteName, res.TotalSegs, res.AdsSegs, len(res.Hits)))
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": res})
}

// handleSettings GET 读取缓存设置 / POST 修改（有效期 + 定时清理）
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.cfgMgr.Snapshot()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200,
			"settings": map[string]interface{}{
				"cache_ttl_days":        float64(cfg.Settings.CacheTTL) / 86400,
				"cache_auto_clean":      cfg.Settings.CacheAutoClean,
				"cache_clean_every_h":   cfg.Settings.CacheCleanEveryH,
				"cache_clean_unit":      normCleanUnit(cfg.Settings.CacheCleanUnit),
				"cache_clean_types":     normCleanTypes(cfg.Settings.CacheCleanTypes),
				"cache_clean_sites":     cfg.Settings.CacheCleanSites,
				"cache_clean_days":      cfg.Settings.CacheCleanDays,
				"cache_scheme":          normCacheScheme(cfg.Settings.CacheScheme),
				"cors_allow_origin":     cfg.Settings.CORSAllowOrigin,
				"admin_path":            s.adminPath(),
				"proxy_pool_enable":     cfg.Settings.ProxyPoolEnable,
				"proxy_pool_api":        cfg.Settings.ProxyPoolAPI,
				"proxy_pool_protocol":   cfg.Settings.ProxyPoolProtocol,
				"proxy_pool_count":      cfg.Settings.ProxyPoolCount,
				"proxy_pool_country":    cfg.Settings.ProxyPoolCountry,
				"proxy_pool_refresh_m":  cfg.Settings.ProxyPoolRefreshM,
				"proxy_pool_strategy":   cfg.Settings.ProxyPoolStrategy,
				"proxy_pool_fail_max":   cfg.Settings.ProxyPoolFailMax,
				"proxy_pool_manual":     cfg.Settings.ProxyPoolManual,
				"ip_access_enable":      cfg.Settings.IPAccessEnable,
			},
		})
	case http.MethodPost:
		var body struct {
			CacheTTLDays     *float64 `json:"cache_ttl_days"`
			CacheAutoClean   *bool    `json:"cache_auto_clean"`
			CacheCleanEveryH *int     `json:"cache_clean_every_h"`
			CacheCleanUnit   *string  `json:"cache_clean_unit"`
			CacheCleanTypes  []string `json:"cache_clean_types"`
			CacheCleanSites  []string `json:"cache_clean_sites"`
			CacheCleanDays   *int     `json:"cache_clean_days"`
			CacheScheme      *string  `json:"cache_scheme"`
			CORSAllowOrigin  *string  `json:"cors_allow_origin"`
			AdminPath        *string  `json:"admin_path"`
			// 代理池
			ProxyPoolEnable   *bool   `json:"proxy_pool_enable"`
			ProxyPoolAPI      *string `json:"proxy_pool_api"`
			ProxyPoolProtocol *string `json:"proxy_pool_protocol"`
			ProxyPoolCount    *int    `json:"proxy_pool_count"`
			ProxyPoolCountry  *string `json:"proxy_pool_country"`
			ProxyPoolRefreshM *int    `json:"proxy_pool_refresh_m"`
			ProxyPoolStrategy *string `json:"proxy_pool_strategy"`
			ProxyPoolFailMax  *int    `json:"proxy_pool_fail_max"`
			// 手动代理列表
			ProxyPoolManual *string `json:"proxy_pool_manual"`
			// IP 授权（纯白名单）
			IPAccessEnable *bool `json:"ip_access_enable"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
			return
		}
		if body.CacheTTLDays != nil {
			if *body.CacheTTLDays <= 0 {
				writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缓存有效期必须大于 0 天"})
				return
			}
			if *body.CacheTTLDays > 365 {
				writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缓存有效期不能超过 365 天"})
				return
			}
		}
		if err := s.cfgMgr.Update(func(f *config.File) error {
			if body.CacheTTLDays != nil {
				f.Settings.CacheTTL = int(*body.CacheTTLDays * 86400)
			}
			if body.CacheAutoClean != nil {
				f.Settings.CacheAutoClean = *body.CacheAutoClean
			}
			if body.CacheCleanEveryH != nil {
				n := *body.CacheCleanEveryH
				if n < 1 {
					n = 1
				}
				f.Settings.CacheCleanEveryH = n
			}
			if body.CacheCleanUnit != nil {
				f.Settings.CacheCleanUnit = normCleanUnit(*body.CacheCleanUnit)
			}
			if body.CacheCleanTypes != nil {
				f.Settings.CacheCleanTypes = normCleanTypes(body.CacheCleanTypes)
			}
			if body.CacheCleanSites != nil {
				f.Settings.CacheCleanSites = normCacheSites(body.CacheCleanSites)
			}
			if body.CacheCleanDays != nil {
				d := *body.CacheCleanDays
				if d < 0 {
					d = 0
				}
				f.Settings.CacheCleanDays = d
			}
			if body.CacheScheme != nil {
				f.Settings.CacheScheme = normCacheScheme(*body.CacheScheme)
			}
			if body.CORSAllowOrigin != nil {
				f.Settings.CORSAllowOrigin = strings.TrimSpace(*body.CORSAllowOrigin)
			}
			if body.AdminPath != nil {
				raw := strings.TrimSpace(*body.AdminPath)
				np := normAdminPath(raw)
				if raw != "" && np != "/"+strings.Trim(strings.TrimSpace(raw), "/") {
					return fmt.Errorf("后台地址无效（不能为空、不能是 /api、/cache、/static 或含空格），已保持为 %s", f.Settings.AdminPath)
				}
				f.Settings.AdminPath = np
			}
			// ---- 代理池 ----
			if body.ProxyPoolEnable != nil {
				f.Settings.ProxyPoolEnable = *body.ProxyPoolEnable
			}
			if body.ProxyPoolAPI != nil {
				api := strings.TrimSpace(*body.ProxyPoolAPI)
				if api == "" {
					api = core.DefaultProxyPoolAPI
				}
				if !isHTTPLinkSafe(api) {
					return fmt.Errorf("代理池接口地址必须以 http:// 或 https:// 开头")
				}
				f.Settings.ProxyPoolAPI = api
			}
			if body.ProxyPoolProtocol != nil {
				proto := strings.ToLower(strings.TrimSpace(*body.ProxyPoolProtocol))
				switch proto {
				case "", "http", "https", "socks5", "socks5h":
				default:
					return fmt.Errorf("代理协议仅支持 http / https / socks5")
				}
				if proto == "" {
					proto = core.DefaultProxyPoolProtocol
				}
				f.Settings.ProxyPoolProtocol = proto
			}
			if body.ProxyPoolCount != nil {
				n := *body.ProxyPoolCount
				if n < 1 {
					n = 1
				}
				if n > 50 {
					n = 50
				}
				f.Settings.ProxyPoolCount = n
			}
			if body.ProxyPoolCountry != nil {
				f.Settings.ProxyPoolCountry = strings.ToUpper(strings.TrimSpace(*body.ProxyPoolCountry))
			}
			if body.ProxyPoolRefreshM != nil {
				n := *body.ProxyPoolRefreshM
				if n < 1 {
					n = 1
				}
				if n > 1440 {
					n = 1440
				}
				f.Settings.ProxyPoolRefreshM = n
			}
			if body.ProxyPoolStrategy != nil {
				st := strings.ToLower(strings.TrimSpace(*body.ProxyPoolStrategy))
				if st != "random" {
					st = core.DefaultProxyPoolStrategy
				}
				f.Settings.ProxyPoolStrategy = st
			}
			if body.ProxyPoolFailMax != nil {
				n := *body.ProxyPoolFailMax
				if n < 1 {
					n = 1
				}
				if n > 10 {
					n = 10
				}
				f.Settings.ProxyPoolFailMax = n
			}
			// ---- 手动代理列表 ----
			if body.ProxyPoolManual != nil {
				f.Settings.ProxyPoolManual = *body.ProxyPoolManual
			}
			// ---- IP 授权（纯白名单）----
			if body.IPAccessEnable != nil {
				f.Settings.IPAccessEnable = *body.IPAccessEnable
			}
			return nil
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
			return
		}
		s.reload()      // 让新 TTL 立即作用于缓存命中判断
		s.replanClean() // 重算下次清理时间
		cfg := s.cfgMgr.Snapshot()
		if cfg.Settings.ProxyPoolEnable && len(core.DefaultPool().Status().Items) == 0 {
			go func() { _, _ = core.DefaultPool().Refresh() }() // 刚开启：后台预热一次
		}
		s.logger.Write("INFO", "admin", fmt.Sprintf("更新系统设置：有效期 %.2f 天 / 定时清理 %v / 每 %d %s / 代理池 %v / IP授权 %v",
			float64(cfg.Settings.CacheTTL)/86400, cfg.Settings.CacheAutoClean,
			cfg.Settings.CacheCleanEveryH, cleanUnitLabel(cfg.Settings.CacheCleanUnit),
			cfg.Settings.ProxyPoolEnable, cfg.Settings.IPAccessEnable))
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "msg": "设置已保存",
			"settings": map[string]interface{}{
				"admin_path":        s.adminPath(),
				"cache_ttl_days":    float64(cfg.Settings.CacheTTL) / 86400,
				"cache_clean_types": normCleanTypes(cfg.Settings.CacheCleanTypes),
				"cache_clean_sites": cfg.Settings.CacheCleanSites,
				"proxy_pool_enable": cfg.Settings.ProxyPoolEnable,
				"ip_access_enable":  cfg.Settings.IPAccessEnable,
			},
		})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
	}
}

// tempProxyClient 按请求临时指定的代理串构造客户端；空串 / - / direct 返回 nil（沿用站点或全局规则）。
func (s *Server) tempProxyClient(raw string, timeout time.Duration) *http.Client {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" || strings.EqualFold(raw, "direct") {
		return nil
	}
	if _, err := core.ParseProxyURL(raw, nil); err != nil {
		return nil
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return core.NewClientWithProxy(timeout, raw)
}

// proxyDiag 用临时代理直接访问目标地址，返回一句话诊断，便于区分「代理不通」与「站点/地址问题」
func (s *Server) proxyDiag(raw, target string) string {
	cfg := s.cfgMgr.Snapshot()
	res := core.TestProxy(raw, target, 10*time.Second, cfg.Settings.UserAgent)
	if res.OK {
		return "代理可达目标，HTTP " + itoa(res.Status)
	}
	if res.Err != "" {
		return res.Err
	}
	return "HTTP " + itoa(res.Status)
}

// checkProxyURL 校验代理地址格式（空串=跟随全局/环境变量，- 或 direct=强制直连）
func checkProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "-" || strings.EqualFold(raw, "direct") {
		return nil
	}
	if _, err := core.ParseProxyURL(raw, nil); err != nil {
		return fmt.Errorf("代理地址格式无效: %v", err)
	}
	return nil
}

// ---------- 缓存子目录联动 ----------

// cacheSubdirPath 站点缓存子目录绝对路径（空则返回缓存根）
func (s *Server) cacheSubdirPath(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return s.cacheDir
	}
	return filepath.Join(s.cacheDir, sub)
}

// ensureCacheSubdir 新建站点时创建对应缓存目录
func (s *Server) ensureCacheSubdir(sub string) {
	dir := s.cacheSubdirPath(sub)
	if dir == s.cacheDir {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		s.logger.Write("WARN", "admin", "创建缓存目录失败 "+dir+": "+err.Error())
		return
	}
	s.logger.Write("INFO", "admin", "创建缓存目录 "+filepath.Base(dir))
}

// renameCacheSubdir 站点缓存子目录改名：目标不存在直接重命名，存在则并入
func (s *Server) renameCacheSubdir(oldSub, newSub string) {
	oldSub = strings.TrimSpace(oldSub)
	newSub = strings.TrimSpace(newSub)
	if oldSub == newSub {
		s.ensureCacheSubdir(newSub)
		return
	}
	oldDir := s.cacheSubdirPath(oldSub)
	newDir := s.cacheSubdirPath(newSub)
	if oldDir == s.cacheDir || newDir == s.cacheDir {
		s.logger.Write("WARN", "admin", "缓存子目录为空，跳过目录变更")
		return
	}
	if _, err := os.Stat(oldDir); err != nil {
		s.ensureCacheSubdir(newSub) // 旧目录不存在，只保证新目录存在
		return
	}
	if _, err := os.Stat(newDir); err == nil {
		// 新目录已存在：逐个搬文件后删除旧目录
		moved := 0
		if entries, rerr := os.ReadDir(oldDir); rerr == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if rerr := os.Rename(filepath.Join(oldDir, e.Name()), filepath.Join(newDir, e.Name())); rerr == nil {
					moved++
				}
			}
		}
		_ = os.Remove(oldDir)
		s.logger.Write("INFO", "admin", fmt.Sprintf("缓存目录合并 %s -> %s（移动 %d 个文件）", oldSub, newSub, moved))
		return
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		s.logger.Write("WARN", "admin", "缓存目录重命名失败 "+oldSub+" -> "+newSub+": "+err.Error())
		return
	}
	s.logger.Write("INFO", "admin", "缓存目录重命名 "+oldSub+" -> "+newSub)
}

// removeCacheSubdir 删除站点时一并删除其缓存目录
func (s *Server) removeCacheSubdir(sub string) {
	dir := s.cacheSubdirPath(sub)
	if dir == s.cacheDir {
		s.logger.Write("WARN", "admin", "缓存子目录为空，未删除缓存根目录")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		s.logger.Write("WARN", "admin", "删除缓存目录失败 "+dir+": "+err.Error())
		return
	}
	s.logger.Write("WARN", "admin", "删除缓存目录 "+filepath.Base(dir))
}

// ---------- 记录 / 缓存 / 日志 ----------

// handleRecords GET ?n= 最近解析记录
func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
	// 清空解析记录
	if r.Method == http.MethodDelete {
		s.recs.Clear()
		s.logger.Write("INFO", "admin", "清空全部解析记录")
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "msg": "已清空解析记录"})
		return
	}

	n := 200
	if v := r.URL.Query().Get("n"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
			if n > 2000 {
				n = 2000
			}
		}
	}
	status := r.URL.Query().Get("status") // all | ok | fail
	site := strings.TrimSpace(r.URL.Query().Get("site"))
	kw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kw")))

	all := s.recs.All()
	out := make([]store.Record, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- { // 倒序：最新在前
		rec := all[i]
		if status == "ok" && rec.Code != 200 {
			continue
		}
		if status == "fail" && rec.Code == 200 {
			continue
		}
		if site != "" && rec.Site != site {
			continue
		}
		if kw != "" && !strings.Contains(strings.ToLower(rec.URL), kw) &&
			!strings.Contains(strings.ToLower(rec.Msg), kw) &&
			!strings.Contains(strings.ToLower(rec.Site), kw) {
			continue
		}
		out = append(out, rec)
	}
	matched := len(out)
	if n > 0 && n < len(out) {
		out = out[:n]
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "total": s.recs.Count(), "matched": matched, "records": out,
	})
}

// handleCache POST 清理缓存 {types:[expired|all|site|root|preview|seghash], sites:[缓存子目录], days, mode}
func (s *Server) handleCache(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode  string   `json:"mode"`  // 兼容旧参数：all / expired / days
		Days  int      `json:"days"`  // days>0 时按 N 天前清理
		Types []string `json:"types"` // 清理类型（新参数，缺省按 mode 或已保存配置）
		Sites []string `json:"sites"` // 指定站点缓存子目录（types 含 site 时生效）
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	types := body.Types
	if len(types) == 0 {
		switch body.Mode {
		case "expired", "days":
			types = []string{"expired"}
		case "all":
			types = []string{"all"}
		default:
			types = normCleanTypes(s.cfgMgr.Snapshot().Settings.CacheCleanTypes)
		}
	}
	res := s.runCleanByTypes(types, body.Sites, body.Days)
	s.logger.Write("INFO", "admin", fmt.Sprintf("清理缓存 %s 删除 %d 个文件", strings.Join(res.Labels, "+"), res.Total))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "msg": "已清理", "deleted": res.Total, "detail": res.Detail, "labels": res.Labels,
	})
}

// handleLogs GET 今日日志尾部
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 1000 {
			limit = x
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "lines": s.logger.Tail(limit)})
}

var errNotFound = errStr("not found")

type errStr string

func (e errStr) Error() string { return string(e) }

func genID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
