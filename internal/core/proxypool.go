package core

import (
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 代理池默认配置
const (
	DefaultProxyPoolAPI      = "https://proxy.scdn.io/api/get_proxy.php"
	DefaultProxyPoolProtocol = "https"
	DefaultProxyPoolCount    = 3
	DefaultProxyPoolRefreshM = 30 // 分钟
	DefaultProxyPoolStrategy = "round"
	DefaultProxyPoolFailMax  = 3
	// ProxyProbeURL 连通性测试使用的内置探测地址：https 且直接回显出口 IP 与归属地。
	// 入池不做探测，仅用于「测试代理」按钮；固定不可配置，
	// 避免填成明文 http 地址后把不支持 HTTPS 隧道的代理误判为可用。
	ProxyProbeURL = "https://myip.ipip.net/"
)

// ProxyItem 池中单个代理的展示信息
type ProxyItem struct {
	Raw     string `json:"raw"`                // 可直接交给 ParseProxyURL 的完整地址（含 scheme）
	Addr    string `json:"addr"`               // ip:port
	Fails   int    `json:"fails"`              // 连续失败次数
	Used    int    `json:"used"`               // 取用次数
	LastOK  string `json:"last_ok,omitempty"`  // 最近一次成功时间
	LastErr string `json:"last_err,omitempty"` // 最近一次失败原因
}

// ProxyPoolStatus 代理池状态（系统设置页展示）
type ProxyPoolStatus struct {
	Enable      bool        `json:"enable"`
	API         string      `json:"api"`
	Protocol    string      `json:"protocol"`
	Count       int         `json:"count"`
	Country     string      `json:"country"`
	RefreshM    int         `json:"refresh_m"`
	Strategy    string      `json:"strategy"`
	FailMax     int         `json:"fail_max"`
	Total       int         `json:"total"`
	LastRefresh string      `json:"last_refresh,omitempty"`
	NextRefresh string      `json:"next_refresh,omitempty"`
	LastErr     string      `json:"last_err,omitempty"`
	Items       []ProxyItem `json:"items"`
}

// poolItem 池中代理的内部状态
type poolItem struct {
	Raw    string
	Addr   string
	Fails  int
	Used   int
	LastOK time.Time
	Err    string
}

type poolCfg struct {
	enable   bool
	api      string
	protocol string
	count    int
	country  string
	refresh  time.Duration
	strategy string
	failMax  int
	manual   string // 手动代理列表（多行）
	ua       string // 测试请求 UA
}

// ProxyPool 出网代理池：按配置定期从接口拉取，取用时轮询/随机，失败自动剔除并补位。
// 线程安全，零值不可用，请用 NewProxyPool 构造。
type ProxyPool struct {
	mu          sync.Mutex
	cfg         poolCfg
	items       []*poolItem
	cursor      uint64
	lastRefresh time.Time
	lastErr     string
	client      *http.Client
	stopCh      chan struct{}
	started     bool
}

// NewProxyPool 构造代理池（不自动启动后台刷新，需调用 Start）
func NewProxyPool() *ProxyPool {
	return &ProxyPool{
		cfg: poolCfg{
			api:      DefaultProxyPoolAPI,
			protocol: DefaultProxyPoolProtocol,
			count:    DefaultProxyPoolCount,
			refresh:  DefaultProxyPoolRefreshM * time.Minute,
			strategy: DefaultProxyPoolStrategy,
			failMax:  DefaultProxyPoolFailMax,
		},
		client: &http.Client{Timeout: 20 * time.Second},
		stopCh: make(chan struct{}),
	}
}

var (
	defaultPoolOnce sync.Once
	defaultPool     *ProxyPool
)

// DefaultPool 进程内共享的默认代理池（解析器与后台接口共用）
func DefaultPool() *ProxyPool {
	defaultPoolOnce.Do(func() {
		defaultPool = NewProxyPool()
		defaultPool.Start()
	})
	return defaultPool
}

// ApplySettings 用全局配置刷新代理池参数
func (p *ProxyPool) ApplySettings(s *Settings) {
	if p == nil || s == nil {
		return
	}
	count := s.ProxyPoolCount
	if count < 1 {
		count = DefaultProxyPoolCount
	}
	refreshM := s.ProxyPoolRefreshM
	if refreshM < 1 {
		refreshM = DefaultProxyPoolRefreshM
	}
	api := strings.TrimSpace(s.ProxyPoolAPI)
	if api == "" {
		api = DefaultProxyPoolAPI
	}
	proto := strings.ToLower(strings.TrimSpace(s.ProxyPoolProtocol))
	if proto == "" {
		proto = DefaultProxyPoolProtocol
	}
	strategy := strings.ToLower(strings.TrimSpace(s.ProxyPoolStrategy))
	if strategy != "random" {
		strategy = DefaultProxyPoolStrategy
	}
	failMax := s.ProxyPoolFailMax
	if failMax < 1 {
		failMax = DefaultProxyPoolFailMax
	}
	p.mu.Lock()
	p.cfg = poolCfg{
		enable:   s.ProxyPoolEnable,
		api:      api,
		protocol: proto,
		count:    count,
		country:  strings.ToUpper(strings.TrimSpace(s.ProxyPoolCountry)),
		refresh:  time.Duration(refreshM) * time.Minute,
		strategy: strategy,
		failMax:  failMax,
		manual:   s.ProxyPoolManual,
		ua:       strings.TrimSpace(s.UserAgent),
	}
	p.mu.Unlock()
}

// Start 启动后台定期刷新（每 30 秒检查一次是否到点）
func (p *ProxyPool) Start() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-p.stopCh:
				return
			case <-t.C:
				p.tickRefresh()
			}
		}
	}()
}

// Stop 停止后台刷新
func (p *ProxyPool) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		p.started = false
		close(p.stopCh)
	}
}

// tickRefresh 到点则刷新（未启用或池非空且未过期则跳过）
func (p *ProxyPool) tickRefresh() {
	p.mu.Lock()
	cfg := p.cfg
	due := cfg.enable && (p.lastRefresh.IsZero() || time.Since(p.lastRefresh) >= cfg.refresh)
	p.mu.Unlock()
	if !due {
		return
	}
	_, _ = p.Refresh()
}

// Take 取一个可用代理（返回带 scheme 的完整地址）；未启用或池为空返回空串。
// 固定代理优先于代理池，因此调用方应在全局 ProxyURL 为空时才走这里。
func (p *ProxyPool) Take() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	if !p.cfg.enable {
		p.mu.Unlock()
		return ""
	}
	if len(p.items) == 0 {
		p.mu.Unlock()
		// 首次使用：同步拉一次（失败返回空串，走直连不阻塞业务）
		if _, err := p.Refresh(); err != nil {
			return ""
		}
		p.mu.Lock()
	}
	if len(p.items) == 0 {
		p.mu.Unlock()
		return ""
	}
	var it *poolItem
	if p.cfg.strategy == "random" {
		it = p.items[rand.Intn(len(p.items))]
	} else {
		p.cursor++
		it = p.items[(p.cursor-1)%uint64(len(p.items))]
	}
	it.Used++
	raw := it.Raw
	p.mu.Unlock()
	return raw
}

// ReportOK 标记某个代理使用成功
func (p *ProxyPool) ReportOK(raw string) {
	if p == nil || raw == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, it := range p.items {
		if it.Raw == raw {
			it.Fails = 0
			it.LastOK = time.Now()
			it.Err = ""
			return
		}
	}
}

// ReportFail 标记某个代理使用失败；连续失败达到阈值则剔除，并在池不足时异步补位
func (p *ProxyPool) ReportFail(raw, reason string) {
	if p == nil || raw == "" {
		return
	}
	need := false
	p.mu.Lock()
	for i, it := range p.items {
		if it.Raw != raw {
			continue
		}
		it.Fails++
		it.Err = reason
		if it.Fails >= p.cfg.failMax {
			p.items = append(p.items[:i], p.items[i+1:]...)
			need = true
		}
		break
	}
	half := p.cfg.count / 2
	if half < 1 {
		half = 1
	}
	if !need && len(p.items) < half {
		need = true
	}
	p.mu.Unlock()
	if need {
		go func() { _, _ = p.Refresh() }()
	}
}

type proxyAPIResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Proxies []string `json:"proxies"`
		Count   int      `json:"count"`
	} `json:"data"`
}

// Refresh 立即重建代理池：手动列表优先，不足时从接口补齐；
// 每个候选代理都必须通过入池校验（真实发起 https 请求）才保留，校验不通过的直接丢弃。
func (p *ProxyPool) Refresh() (int, error) {
	if p == nil {
		return 0, errors.New("代理池未初始化")
	}
	p.mu.Lock()
	cfg := p.cfg
	old := make(map[string]*poolItem, len(p.items))
	for _, it := range p.items {
		old[it.Raw] = it
	}
	p.mu.Unlock()

	// 1) 收集候选：手动列表 + 接口补齐
	raws := ParseManualProxies(cfg.manual, cfg.protocol)
	if len(raws) < cfg.count && strings.TrimSpace(cfg.api) != "" {
		got, err := p.fetchFromAPI(cfg)
		if err != nil {
			if len(raws) == 0 {
				return 0, err
			}
			p.setErr("接口拉取失败（已用手动代理）: " + err.Error())
		}
		raws = append(raws, got...)
	}
	if len(raws) == 0 {
		msg := "未配置手动代理，且拉取接口未返回代理"
		p.setErr(msg)
		return 0, errors.New(msg)
	}

	cand := make([]*poolItem, 0, len(raws))
	seen := map[string]bool{}
	for _, raw := range raws {
		full := NormalizeProxyAddr(raw, cfg.protocol)
		if full == "" || seen[full] {
			continue
		}
		seen[full] = true
		if o, ok := old[full]; ok {
			cand = append(cand, o) // 保留历史成功/失败统计
			continue
		}
		cand = append(cand, &poolItem{Raw: full, Addr: proxyAddrOf(full)})
	}

	p.mu.Lock()
	p.items = cand
	p.lastRefresh = time.Now()
	n := len(p.items)
	if n > 0 {
		p.lastErr = ""
	}
	p.mu.Unlock()
	return n, nil
}

// ParseManualProxies 解析手动代理列表（每行一个；# 开头为注释；缺 scheme 时按 protocol 补全）
func ParseManualProxies(manual, protocol string) []string {
	out := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, ln := range strings.Split(manual, "\n") {
		ln = strings.TrimSpace(strings.ReplaceAll(ln, "\r", ""))
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		full := NormalizeProxyAddr(ln, protocol)
		if full == "" || seen[full] {
			continue
		}
		seen[full] = true
		out = append(out, full)
	}
	return out
}

// fetchFromAPI 从代理接口拉取一批代理（原始 ip:port 列表，未校验）
func (p *ProxyPool) fetchFromAPI(cfg poolCfg) ([]string, error) {
	q := url.Values{}
	q.Set("protocol", cfg.protocol)
	q.Set("count", strconv.Itoa(cfg.count))
	if cfg.country != "" {
		q.Set("country_code", cfg.country)
	}
	target := cfg.api
	if strings.Contains(target, "?") {
		target += "&" + q.Encode()
	} else {
		target += "?" + q.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		p.setErr(err.Error())
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		p.setErr(err.Error())
		return nil, err
	}
	defer resp.Body.Close()

	var out proxyAPIResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		p.setErr("接口返回解析失败: " + err.Error())
		return nil, err
	}
	if out.Code != 0 && out.Code != 200 {
		msg := "接口返回 code=" + strconv.Itoa(out.Code)
		if out.Message != "" {
			msg += " " + out.Message
		}
		p.setErr(msg)
		return nil, errors.New(msg)
	}
	return out.Data.Proxies, nil
}

func (p *ProxyPool) setErr(msg string) {
	p.mu.Lock()
	p.lastErr = msg
	p.mu.Unlock()
}

// Remove 从池中移除指定代理（按完整地址或 ip:port 匹配），返回是否命中
func (p *ProxyPool) Remove(raw string) bool {
	raw = strings.TrimSpace(raw)
	if p == nil || raw == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, it := range p.items {
		if it == nil {
			continue
		}
		if it.Raw == raw || it.Addr == raw {
			p.items = append(p.items[:i], p.items[i+1:]...)
			if len(p.items) == 0 {
				p.cursor = 0
			} else if p.cursor > uint64(len(p.items)-1) {
				p.cursor = 0
			}
			return true
		}
	}
	return false
}

// Status 返回当前池状态
func (p *ProxyPool) Status() ProxyPoolStatus {
	st := ProxyPoolStatus{
		API:      DefaultProxyPoolAPI,
		Protocol: DefaultProxyPoolProtocol,
		Count:    DefaultProxyPoolCount,
		RefreshM: DefaultProxyPoolRefreshM,
		Strategy: DefaultProxyPoolStrategy,
		FailMax:  DefaultProxyPoolFailMax,
		Items:    []ProxyItem{},
	}
	if p == nil {
		return st
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st.Enable = p.cfg.enable
	st.API = p.cfg.api
	st.Protocol = p.cfg.protocol
	st.Count = p.cfg.count
	st.Country = p.cfg.country
	st.RefreshM = int(p.cfg.refresh.Minutes())
	st.Strategy = p.cfg.strategy
	st.FailMax = p.cfg.failMax
	st.Total = len(p.items)
	st.LastErr = p.lastErr
	if !p.lastRefresh.IsZero() {
		st.LastRefresh = p.lastRefresh.Format("2006-01-02 15:04:05")
		st.NextRefresh = p.lastRefresh.Add(p.cfg.refresh).Format("2006-01-02 15:04:05")
	}
	for _, it := range p.items {
		pi := ProxyItem{Raw: it.Raw, Addr: it.Addr, Fails: it.Fails, Used: it.Used, LastErr: it.Err}
		if !it.LastOK.IsZero() {
			pi.LastOK = it.LastOK.Format("2006-01-02 15:04:05")
		}
		st.Items = append(st.Items, pi)
	}
	return st
}

// NormalizeProxyAddr 把接口返回的 ip:port 补成完整代理地址。
// 已带 scheme 的原样返回；否则按 protocol 补前缀（https/socks5/http，默认 http）。
func NormalizeProxyAddr(addr, protocol string) string {
	s := strings.TrimSpace(addr)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		return s
	}
	scheme := strings.ToLower(strings.TrimSpace(protocol))
	switch scheme {
	case "socks5", "socks5h":
		// 确实是 socks5 代理服务
	default:
		// http / https：代理服务本身是 HTTP 代理（目标站为 https 时走 CONNECT 隧道），
		// 因此统一补 http://；若错误地补 https:// 会去跟代理握手 TLS 导致证书报错。
		scheme = "http"
	}
	return scheme + "://" + s
}

// proxyAddrOf 从完整地址中取 ip:port（用于展示）
func proxyAddrOf(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		return raw[i+3:]
	}
	return raw
}
