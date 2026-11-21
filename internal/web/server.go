package web

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/core"
	"dytt_parser_go/internal/store"
)

// buildTag index.html 中用于绕开浏览器磁盘缓存的版本占位符
var buildTag = []byte("__BUILD__")

// registerMimeTypes 补齐 m3u8 / ts 等播放器依赖的 MIME（不同系统 mime 库差异大，显式注册最稳）
func registerMimeTypes() {
	for ext, typ := range map[string]string{
		".m3u8": "application/vnd.apple.mpegurl",
		".m3u":  "audio/x-mpegurl",
		".ts":   "video/mp2t",
		".key":  "application/octet-stream",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

// Server 后台服务（路由 / 会话 / 静态资源 / 解析器持有）
type Server struct {
	cfgMgr   *config.Manager
	recs     *store.Records
	disc     *store.Discovered
	marks    *store.Marks
	sessions *SessionStore
	logger   *Logger

	rootDir   string
	dataDir   string
	cacheDir  string
	logDir    string
	basePath  string
	listen    string
	startedAt time.Time
	staticFS  fs.FS

	// parser 通过 RWMutex 整体替换（站点/配置变更后 reload）
	pMu    sync.RWMutex
	parser *core.Parser

	// 解析测试快照（播放预览 / 片段查看），token -> 结果
	prevMu   sync.Mutex
	previews map[string]*previewEntry

	// 定时缓存清理状态
	cleanMu    sync.Mutex
	cleanLast  time.Time
	cleanNext  time.Time
	cleanLastN int
}

// NewServer 构造后台服务
func NewServer(cfgMgr *config.Manager, recs *store.Records, rootDir, cacheDir, logDir, listen, basePath string, staticFS fs.FS) *Server {
	registerMimeTypes()
	s := &Server{
		cfgMgr:    cfgMgr,
		recs:      recs,
		sessions:  NewSessionStore(),
		logger:    NewLogger(logDir),
		rootDir:   rootDir,
		dataDir:   filepathSafe(rootDir, "data"),
		cacheDir:  cacheDir,
		logDir:    logDir,
		basePath:  strings.TrimRight(basePath, "/"),
		listen:    listen,
		startedAt: time.Now(),
		staticFS:  staticFS,
		previews:  map[string]*previewEntry{},
	}
	s.disc = store.NewDiscovered(filepath.Join(s.dataDir, "discovered.json"))
	s.marks = store.NewMarks(filepath.Join(s.dataDir, "marks.json"))
	s.reload()
	s.replanClean()
	go s.cleanLoop()
	return s
}

// ---------- 定时缓存清理 ----------

// normCleanUnit 归一化间隔单位，非法值按小时处理
func normCleanUnit(u string) string {
	switch strings.ToLower(strings.TrimSpace(u)) {
	case "s", "sec", "second":
		return "s"
	case "m", "min", "minute":
		return "m"
	default:
		return "h"
	}
}

// cleanUnitLabel 单位中文名
func cleanUnitLabel(u string) string {
	switch normCleanUnit(u) {
	case "s":
		return "秒"
	case "m":
		return "分钟"
	default:
		return "小时"
	}
}

// cleanInterval 由配置换算清理间隔：支持秒/分/时，最小 10 秒
func cleanInterval(cfg *config.File) time.Duration {
	n := cfg.Settings.CacheCleanEveryH
	if n < 1 {
		n = 1
	}
	var d time.Duration
	switch normCleanUnit(cfg.Settings.CacheCleanUnit) {
	case "s":
		d = time.Duration(n) * time.Second
	case "m":
		d = time.Duration(n) * time.Minute
	default:
		d = time.Duration(n) * time.Hour
	}
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	return d
}

// replanClean 按当前配置重算下次清理时间（关闭时为 zero）
func (s *Server) replanClean() {
	cfg := s.cfgMgr.Snapshot()
	s.cleanMu.Lock()
	defer s.cleanMu.Unlock()
	if !cfg.Settings.CacheAutoClean {
		s.cleanNext = time.Time{}
		return
	}
	iv := cleanInterval(&cfg)
	now := time.Now()
	if s.cleanLast.IsZero() {
		s.cleanLast = now
	}
	next := s.cleanLast.Add(iv)
	if !next.After(now) {
		next = now.Add(iv)
	}
	s.cleanNext = next
}

// cleanLoop 每秒检查一次，到点清理过期缓存（支持秒级间隔）
func (s *Server) cleanLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		s.tickClean()
	}
}

// tickClean 单次检查并执行到期清理
func (s *Server) tickClean() {
	cfg := s.cfgMgr.Snapshot()
	if !cfg.Settings.CacheAutoClean {
		return
	}
	s.cleanMu.Lock()
	due := !s.cleanNext.IsZero() && !time.Now().Before(s.cleanNext)
	s.cleanMu.Unlock()
	if !due {
		return
	}
	types := cfg.Settings.CacheCleanTypes
	if len(types) == 0 {
		types = []string{"expired"}
	}
	detail := s.runCleanByTypes(types, cfg.Settings.CacheCleanSites, cfg.Settings.CacheCleanDays)
	n := detail.Total
	s.cleanMu.Lock()
	s.cleanLast = time.Now()
	s.cleanLastN = n
	s.cleanMu.Unlock()
	s.replanClean()
	if n > 0 {
		s.logger.Write("INFO", "admin", "定时清理缓存（"+strings.Join(cleanTypeLabels(types), "+")+"），删除 "+itoa(n)+" 个文件")
	}
}

// cleanState 返回定时清理状态（供仪表盘展示）
func (s *Server) cleanState() (last, next string, lastN int) {
	s.cleanMu.Lock()
	defer s.cleanMu.Unlock()
	lastN = s.cleanLastN
	if !s.cleanLast.IsZero() {
		last = s.cleanLast.Format("2006-01-02 15:04:05")
	}
	if !s.cleanNext.IsZero() {
		next = s.cleanNext.Format("2006-01-02 15:04:05")
	}
	return
}

// reload 依据最新配置重建解析器
func (s *Server) reload() {
	cfg := s.cfgMgr.Snapshot()
	// 代理池参数随配置同步（后台刷新间隔/数量/协议等即时生效）
	core.DefaultPool().ApplySettings(&cfg.Settings)
	p := core.NewParser(&cfg.Settings, cfg.Sites, &cfg.AdsLib, s.cacheDir)
	s.pMu.Lock()
	s.parser = p
	s.pMu.Unlock()
}

// parser_ 当前解析器
func (s *Server) parser_() *core.Parser {
	s.pMu.RLock()
	defer s.pMu.RUnlock()
	return s.parser
}

// Handler 构建 HTTP 路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 公开接口：缓存文件（播放器跨域调用，不随后台入口变化）
	// 禁止列目录：目录请求一律 403，单个缓存文件仍可直接取
	mux.Handle("/cache/", s.serveCacheFiles())

	// 其余路径一律走动态分发：解析接口 / 后台页面 / 后台接口
	mux.HandleFunc("/", s.dispatch)

	// 跨域统一在外层：解析接口 / 后台接口 / 缓存文件全部带上 CORS 头并处理预检
	return s.accessLog(s.withCORS(s.ipGuard(mux)))
}

// serveCacheFiles 缓存文件静态服务：允许直接取文件，禁止列目录（避免被枚举浏览）
func (s *Server) serveCacheFiles() http.Handler {
	inner := http.StripPrefix("/cache/", http.FileServer(http.Dir(s.cacheDir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.Trim(strings.TrimPrefix(r.URL.Path, "/cache/"), "/")
		// 目录根、以 / 结尾、解析后指向目录的请求一律拒绝
		if rel == "" || strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/") {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"code": 403, "msg": "禁止列目录"})
			return
		}
		full := filepath.Join(s.cacheDir, filepath.FromSlash(rel))
		if fi, err := os.Stat(full); err == nil && fi.IsDir() {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"code": 403, "msg": "禁止列目录"})
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// ipGuard IP 授权访问控制（系统设置页配置，默认关闭）
// 白名单生效范围：对外解析接口（默认 /api/clean 与所有启用的自定义接口）+ /cache/ 缓存文件；
// 后台页面、后台 /admin/api/* 接口与登录接口不受限制，避免把自己锁在门外。
func (s *Server) ipGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.cfgMgr.Snapshot()
		if cfg.Settings.IPAccessEnable && s.needIPGuard(r.URL.Path) {
			ip := clientIP(r)
			res := core.CheckIPAccess(cfg.IPRules, true, ip)
			if !res.Allowed {
				s.logger.Write("WARN", "admin", "IP 拦截 "+ip+" ("+res.Reason+") "+r.Method+" "+r.URL.Path)
				writeJSON(w, http.StatusForbidden, map[string]interface{}{"code": 403, "msg": "IP 未授权，禁止访问"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// needIPGuard 判断路径是否受 IP 白名单保护
func (s *Server) needIPGuard(path string) bool {
	if path == "/cache" || strings.HasPrefix(path, "/cache/") {
		return true
	}
	// 对外解析接口：默认 /api/clean + 系统设置里启用的自定义接口地址
	return s.isCleanPath(path)
}

// normAdminPath 规范化后台入口：以 / 开头、去掉尾斜杠、避开保留段，非法值回落 /admin
func normAdminPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/admin"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = "/" + strings.Trim(p, "/")
	if p == "/" {
		return "/admin"
	}
	if strings.ContainsAny(p, " \t?#\\") {
		return "/admin"
	}
	low := strings.ToLower(p)
	for _, r := range []string{"api", "cache", "static"} {
		if low == "/"+r || strings.HasPrefix(low, "/"+r+"/") {
			return "/admin"
		}
	}
	return p
}

// adminPath 当前生效的后台入口（每次实时读配置，改完立即生效无需重启）
func (s *Server) adminPath() string {
	return normAdminPath(s.cfgMgr.Snapshot().Settings.AdminPath)
}

// dispatch 根分发：命中后台入口 → 后台页面/接口，其余一律 404
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// 数据目录伪装：/data 与 /data/... 一律按「路径不存在」处理，返回与任意未知路径完全一致的 404。
	// 不返回 403、不列目录、响应里不出现 data 字样，避免配置目录的存在被探测；服务端留一条 WARN 便于自查。
	if low := strings.ToLower(path); low == "/data" || strings.HasPrefix(low, "/data/") {
		s.logger.Write("WARN", "admin", "探测 data 目录（已伪装 404） "+clientIP(r)+" "+r.Method+" "+path)
		http.NotFound(w, r)
		return
	}
	// 对外解析接口：默认 /api/clean + 系统设置里启用的自定义接口地址，请求进来按相同逻辑解析
	if s.isCleanPath(path) {
		s.withCORS(http.HandlerFunc(s.handleClean)).ServeHTTP(w, r)
		return
	}
	admin := s.adminPath()
	if path == admin {
		http.Redirect(w, r, admin+"/", http.StatusFound)
		return
	}
	if !strings.HasPrefix(path, admin+"/") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, admin) // 形如 / 或 /api/xxx 或 /css/app.css
	if rest == "/api" || strings.HasPrefix(rest, "/api/") {
		s.adminAPI(w, r)
		return
	}
	s.serveAdminSPA(w, r)
}

// adminAPI 后台接口分发（去掉后台入口与 /api 前缀后按原路径匹配）
func (s *Server) adminAPI(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, s.adminPath()+"/api") // "" / "/xxx" / "/sites/xxx"
	switch {
	case sub == "/login":
		s.handleLogin(w, r)
	case sub == "/logout":
		s.handleLogout(w, r)
	case sub == "/me":
		s.handleMe(w, r)
	case sub == "/dashboard":
		s.requireAuth(s.handleDashboard)(w, r)
	case sub == "/sites":
		s.requireAuth(s.handleSitesList)(w, r)
	case sub == "/sites/export":
		s.requireAuth(s.handleSitesExport)(w, r)
	case sub == "/sites/import":
		s.requireAuth(s.handleSitesImport)(w, r)
	case strings.HasPrefix(sub, "/sites/"):
		s.requireAuth(s.handleSitesItem)(w, r)
	case sub == "/test":
		s.requireAuth(s.handleTest)(w, r)
	case sub == "/test/playlist":
		s.requireAuth(s.handleTestPlaylist)(w, r)
	case sub == "/test/segment":
		s.requireAuth(s.handleTestSegment)(w, r)
	case sub == "/test/ts":
		s.requireAuth(s.handleTestTS)(w, r)
	case sub == "/test/marks":
		s.requireAuth(s.handleTestMarks)(w, r)
	case sub == "/marks":
		s.requireAuth(s.handleMarksList)(w, r)
	case sub == "/learn/suggest":
		s.requireAuth(s.handleLearnSuggest)(w, r)
	case sub == "/learn/hash":
		s.requireAuth(s.handleLearnHash)(w, r)
	case sub == "/learn/apply":
		s.requireAuth(s.handleLearnApply)(w, r)
	case sub == "/probe":
		s.requireAuth(s.handleProbe)(w, r)
	case sub == "/discover":
		s.requireAuth(s.handleDiscover)(w, r)
	case sub == "/discover/resolve":
		s.requireAuth(s.handleDiscoverResolve)(w, r)
	case sub == "/discover/batch":
		s.requireAuth(s.handleDiscoverBatch)(w, r)
	case sub == "/discover/history":
		s.requireAuth(s.handleDiscoverHistory)(w, r)
	case sub == "/sources":
		s.requireAuth(s.handleSources)(w, r)
	case sub == "/records":
		s.requireAuth(s.handleRecords)(w, r)
	case sub == "/password":
		s.requireAuth(s.handlePassword)(w, r)
	case sub == "/cache":
		s.requireAuth(s.handleCache)(w, r)
	case sub == "/logs":
		s.requireAuth(s.handleLogs)(w, r)
	case sub == "/settings":
		s.requireAuth(s.handleSettings)(w, r)
	case sub == "/clean-paths/test":
		s.requireAuth(s.handleCleanPathTest)(w, r)
	case sub == "/clean-paths":
		s.requireAuth(s.handleCleanPaths)(w, r)
	case strings.HasPrefix(sub, "/clean-paths/"):
		s.requireAuth(s.handleCleanPathItem)(w, r)

	// ---- 系统设置 ----
	case sub == "/system/settings":
		s.requireAuth(s.handleSystemSettings)(w, r)
	case sub == "/ip-rules":
		s.requireAuth(s.handleIPRules)(w, r)
	case sub == "/ip-rules/check":
		s.requireAuth(s.handleIPCheck)(w, r)
	case strings.HasPrefix(sub, "/ip-rules/"):
		s.requireAuth(s.handleIPRuleItem)(w, r)
	case sub == "/ip-access":
		s.requireAuth(s.handleIPAccessSwitch)(w, r)
	case sub == "/custom-apis/test":
		s.requireAuth(s.handleCustomAPITest)(w, r)
	case sub == "/custom-apis":
		s.requireAuth(s.handleCustomAPIs)(w, r)
	case strings.HasPrefix(sub, "/custom-apis/"):
		s.requireAuth(s.handleCustomAPIItem)(w, r)
	case sub == "/proxy-pool":
		s.requireAuth(s.handleProxyPool)(w, r)
	case sub == "/proxy-pool/refresh":
		s.requireAuth(s.handleProxyPoolRefresh)(w, r)
	case sub == "/proxy-pool/test":
		s.requireAuth(s.handleProxyPoolTest)(w, r)
	case sub == "/proxy-pool/remove":
		s.requireAuth(s.handleProxyPoolRemove)(w, r)
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "接口不存在"})
	}
}

// serveAdminSPA 后台静态资源与页面（仅后台入口下可访问，根路径不再暴露后台）
func (s *Server) serveAdminSPA(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, s.adminPath()), "/")
	if p == "" || p == "index.html" {
		p = "index.html"
	}
	if f, err := s.staticFS.Open(p); err == nil {
		defer f.Close()
		if st, serr := f.Stat(); serr == nil && !st.IsDir() {
			if data, rerr := io.ReadAll(f); rerr == nil {
				s.writeStatic(w, p, data)
				return
			}
		}
	}
	s.writeIndex(w, r)
}

// writeStatic 输出静态文件（index.html 需注入后台入口供前端拼接口地址）
func (s *Server) writeStatic(w http.ResponseWriter, name string, data []byte) {
	if name == "index.html" {
		data = bytes.ReplaceAll(data, []byte("__ADMIN_PATH__"), []byte(s.adminPath()))
	}
	if bytes.Contains(data, buildTag) {
		data = bytes.ReplaceAll(data, buildTag, []byte(strconv.FormatInt(s.startedAt.Unix(), 10)))
	}
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	// 前端改版后浏览器会继续用旧 js/css，这里一律禁止缓存静态资源
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_, _ = w.Write(data)
}

// writeIndex 输出后台页面
func (s *Server) writeIndex(w http.ResponseWriter, r *http.Request) {
	if data, err := fs.ReadFile(s.staticFS, "index.html"); err == nil {
		s.writeStatic(w, "index.html", data)
		return
	}
	http.NotFound(w, r)
}

// corsHeaders 写入跨域响应头（播放器跨域拉取缓存 m3u8 / 调 /api/clean 均需）
func (s *Server) corsHeaders(w http.ResponseWriter, r *http.Request) {
	allow := "*"
	if cfg := s.cfgMgr.Snapshot(); strings.TrimSpace(cfg.Settings.CORSAllowOrigin) != "" {
		allow = strings.TrimSpace(cfg.Settings.CORSAllowOrigin)
	}
	if allow != "*" {
		// 指定域名列表：命中则回显该来源，未命中保持 *（避免整站被任一来源拖垮）
		origin := r.Header.Get("Origin")
		hit := false
		for _, it := range strings.Split(allow, ",") {
			it = strings.TrimSpace(it)
			if it == "" {
				continue
			}
			if it == "*" || it == origin {
				hit = true
				allow = it
				break
			}
		}
		if !hit {
			allow = "*"
		}
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Origin", allow)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "version, access-token, user-token, Accept, apiAuth, User-Agent, Keep-Alive, Origin, No-Cache, X-Requested-With, If-Modified-Since, Pragma, Last-Modified, Cache-Control, Expires, Content-Type, Authorization, Range, X-E4M-With")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range")
	w.Header().Set("Access-Control-Max-Age", "86400")
}

// withCORS 统一跨域处理：预检直接 204，其余请求带上跨域头后继续
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.corsHeaders(w, r)
		// 解析类接口结果禁止缓存：响应内容随请求协议/来源变化，被浏览器或 CDN
		// 缓存旧结果会让 HTTPS 页面拿到 http 缓存地址，触发 Mixed Content
		if strings.Contains(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			w.Header().Add("Vary", "Origin, Referer, X-Forwarded-Proto")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cacheBase 计算缓存访问前缀
func (s *Server) cacheBase(r *http.Request) string {
	cfg := s.cfgMgr.Snapshot()
	// 强制协议：CDN 回源为 http、反代未透传协议头时用它兜底
	if sc := normCacheScheme(cfg.Settings.CacheScheme); sc != "auto" {
		return sc + "://" + r.Host + s.basePath + "/cache"
	}
	return requestScheme(r) + "://" + r.Host + s.basePath + "/cache"
}

// normCacheScheme 归一化缓存访问协议配置：auto / https / http，非法或空值按 auto
func normCacheScheme(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "https", "http":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return "auto"
	}
}

// firstScheme 取指定头中第一个合法协议值
func firstScheme(r *http.Request, names ...string) string {
	for _, n := range names {
		for _, raw := range r.Header.Values(n) {
			for _, part := range strings.Split(raw, ",") {
				v := strings.ToLower(strings.TrimSpace(part))
				if v == "https" {
					return "https"
				}
				if v == "http" {
					return "http"
				}
			}
		}
	}
	return ""
}

// forwardedProto 解析 RFC 7239 Forwarded: proto=https;host=xxx
func forwardedProto(r *http.Request) string {
	for _, raw := range r.Header.Values("Forwarded") {
		for _, part := range strings.Split(raw, ",") {
			for _, kv := range strings.Split(part, ";") {
				kv = strings.TrimSpace(kv)
				if strings.EqualFold(kv, "proto=https") {
					return "https"
				}
				if strings.EqualFold(kv, "proto=http") {
					return "http"
				}
			}
		}
	}
	return ""
}

// requireAuth 未登录直接返回 401 JSON
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie("dytt_admin")
		if err != nil || !s.sessions.Valid(ck.Value) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 401, "msg": "未登录或登录已过期"})
			return
		}
		next(w, r)
	}
}

// accessLog 日志中间件
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		admin := s.adminPath()
		isAdmin := r.URL.Path == admin || strings.HasPrefix(r.URL.Path, admin+"/")
		if isAdmin {
			// 后台访问记录（clean 由业务逻辑记录，避免刷屏）
			s.logger.Write("INFO", "admin",
				r.Method+" "+r.URL.Path+" -> "+itoa(sw.code)+" ("+time.Since(start).Round(time.Millisecond).String()+") ["+clientIP(r)+"]")
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	w.code = c
	w.ResponseWriter.WriteHeader(c)
}

func itoa(i int) string {
	return jsonNumber(i)
}

func jsonNumber(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		if i := strings.Index(ip, ","); i > 0 {
			return strings.TrimSpace(ip[:i])
		}
		return strings.TrimSpace(ip)
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		return host[:i]
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// sysInfo 系统概况（仪表盘使用）
func sysInfo(s *Server, cfg *config.File) map[string]interface{} {
	cacheFiles, cacheBytes := s.cacheStat()
	enableCount := 0
	for _, st := range cfg.Sites {
		if st.Enabled {
			enableCount++
		}
	}
	cleanLast, cleanNext, cleanLastN := s.cleanState()
	return map[string]interface{}{
		"version":             core.Version,
		"go_version":          runtime.Version(),
		"pid":                 jsonNumber(osPID()),
		"started_at":          s.startedAt.Format("2006-01-02 15:04:05"),
		"uptime_sec":          int64(time.Since(s.startedAt).Seconds()),
		"listen":              s.listen,
		"site_total":          len(cfg.Sites),
		"site_enabled":        enableCount,
		"cache_files":         cacheFiles,
		"cache_bytes":         cacheBytes,
		"cache_ttl_days":      float64(cfg.Settings.CacheTTL) / 86400,
		"use_hash_check":      cfg.Settings.UseHashCheck,
		"record_count":        s.recs.Count(),
		"tolerance":           cfg.Settings.MatchTolerance,
		"fetch_timeout":       cfg.Settings.FetchTimeout,
		"sample_size_kb":      cfg.Settings.AdsHashSampleSize / 1024,
		"cache_auto_clean":    cfg.Settings.CacheAutoClean,
		"cache_clean_every_h": cfg.Settings.CacheCleanEveryH,
		"cache_clean_unit":    normCleanUnit(cfg.Settings.CacheCleanUnit),
		"cache_clean_last":    cleanLast,
		"cache_clean_next":    cleanNext,
		"cache_clean_last_n":  cleanLastN,
		"cache_clean_types":   cleanTypeLabels(cfg.Settings.CacheCleanTypes),
		"proxy_site_count":    proxySiteCount(cfg.Sites),
		"proxy_pool":          core.DefaultPool().Status(),
		"clean_paths":         s.activeCleanPaths(),
		"admin_path":          s.adminPath(),
	}
}

// proxySiteCount 统计单独配置了出网代理的站点数量
func proxySiteCount(sites []core.Site) int {
	n := 0
	for i := range sites {
		if sites[i].ProxyURL != "" {
			n++
		}
	}
	return n
}
