package core

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Parser m3u8 广告解析器（无内部可变状态竞争：由上层通过替换指针方式整体更新）
type Parser struct {
	Client     *http.Client
	UA         string
	Timeout    time.Duration
	Settings   *Settings
	Sites      []Site
	AdsLib     *AdsLib
	Cache      *Cache
	proxyCache sync.Map // host -> 生效代理地址字符串（空串=直连）
	lastProxy  sync.Map // host -> 最近一次实际使用的代理地址（供失败上报）
}

// NewParser 构造解析器
func NewParser(settings *Settings, sites []Site, adsLib *AdsLib, cacheDir string) *Parser {
	timeout := 30 * time.Second
	if settings != nil && settings.FetchTimeout > 0 {
		timeout = time.Duration(settings.FetchTimeout) * time.Second
	}
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"
	if settings != nil && settings.UserAgent != "" {
		ua = settings.UserAgent
	}
	cacheTTL := 7 * 24 * time.Hour
	if settings != nil && settings.CacheTTL > 0 {
		cacheTTL = time.Duration(settings.CacheTTL) * time.Second
	}
	p := &Parser{
		UA:       ua,
		Timeout:  timeout,
		Settings: settings,
		Sites:    sites,
		AdsLib:   adsLib,
		Cache: &Cache{
			Dir: cacheDir,
			TTL: cacheTTL,
		},
	}
	// 请求级动态选路：按目标 URL 命中的站点决定走哪个代理（站点级 > 全局 > 环境变量）
	p.Client = newHTTPClient(timeout, p.proxyFor)
	return p
}

// proxyFor 作为 Transport.Proxy 回调：按请求目标选择出网代理。
// 返回 nil,nil 表示直连（同时会覆盖 http_proxy 等环境变量）。
func (p *Parser) proxyFor(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, nil
	}
	host := strings.ToLower(req.URL.Host)
	raw := ""
	if cached, ok := p.proxyCache.Load(host); ok {
		raw, _ = cached.(string)
	} else if s := p.FindSite(req.URL.String()); s != nil && strings.TrimSpace(s.ProxyURL) != "" {
		// 站点级代理固定不变，缓存复用，省去每次请求重新匹配站点
		raw = strings.TrimSpace(s.ProxyURL)
		p.proxyCache.Store(host, raw)
	} else if p.Settings != nil && p.Settings.ProxyPoolEnable {
		// 代理池按次取用（轮换/随机由池策略决定），不缓存；
		// 池暂时为空时也不写缓存，池刷新后下一次请求即可自动走代理，无需重建解析器
		raw = DefaultPool().Take()
	} else {
		p.proxyCache.Store(host, "")
	}
	// 直连时同样写入，避免上一次的残值被误判为本次实际使用的代理
	p.lastProxy.Store(host, raw)
	return ParseProxyURL(raw, req)
}

// LastProxy 返回最近一次为该 URL 分配的代理地址（代理池失败上报用），直连时为空串
func (p *Parser) LastProxy(u string) string {
	if p == nil || u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	host := strings.ToLower(u)
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i+1:], "]") {
		host = host[:i]
	}
	if v, ok := p.lastProxy.Load(host); ok {
		s, _ := v.(string)
		return s
	}
	return ""
}

// ParseProxyURL 解析代理地址：空串跟随环境变量（无环境变量则直连）；
// "-"/"direct" 强制直连；缺 scheme 时默认按 http:// 处理。
func ParseProxyURL(raw string, req *http.Request) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if req != nil {
			return http.ProxyFromEnvironment(req)
		}
		return nil, nil
	}
	if raw == "-" || strings.EqualFold(raw, "direct") {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		if u, err = url.Parse("http://" + raw); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// CloneWithClient 复制解析器并替换 HTTP 客户端（用于按请求临时指定代理）。
// 新解析器共享配置 / 广告库 / 缓存，但不带原代理选路缓存。
func (p *Parser) CloneWithClient(c *http.Client) *Parser {
	if c == nil {
		return p
	}
	return &Parser{
		Client:   c,
		UA:       p.UA,
		Timeout:  p.Timeout,
		Settings: p.Settings,
		Sites:    p.Sites,
		AdsLib:   p.AdsLib,
		Cache:    p.Cache,
	}
}

// ResetProxyCache 配置变更后清空 host -> 代理 的缓存
func (p *Parser) ResetProxyCache() {
	p.proxyCache.Range(func(k, _ any) bool {
		p.proxyCache.Delete(k)
		return true
	})
}

// SetCacheTTL 由 settings 同步缓存 TTL
func (p *Parser) SetCacheTTL(ttlSeconds int) {
	if p.Cache != nil {
		p.Cache.TTL = time.Duration(ttlSeconds) * time.Second
	}
}

// SiteOrigin 站点类型（name 去掉括号部分，对应 PHP site_origin）
func SiteOrigin(s *Site) string {
	if s == nil || s.Name == "" {
		return ""
	}
	if i := strings.Index(s.Name, "("); i >= 0 {
		return strings.TrimSpace(s.Name[:i])
	}
	return s.Name
}

// FindSite 按 URL 主机前缀 / 正则匹配启用中的站点（对应 PHP find_site）
//
// 遍历顺序：按站点 sort 升序（sort<=0 视为无穷大排最后，同级保持 rules.json 数组顺序），
// 保证「界面上排得越靠前，越优先命中」，与后台站点列表的排序一致。
//
// 正则作用域：默认只对 host 匹配；仅当正则显式以 ^ 开头或包含 :// 时才对整条 URL 匹配。
// 原因：库里大量规则是 lzcdn\d*、play-cdn\d*、mtime\d* 这类无锚点短串（\d* 表示零个或多个数字，
// 等价于「含该字样即命中」），若对整条 URL 匹配，任意站点 URL 的路径/参数里出现该字样就会被
// 跨站误捕获。限定 host 后，一条 URL 只会落到它自己 host 所属的那条规则上。
func (p *Parser) FindSite(link string) *Site {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return nil
	}
	host := strings.ToLower(u.Host)
	for _, s := range p.sortedSites() {
		if !s.Enabled {
			continue
		}
		// 匹配前缀支持多个：url_prefixes 数组 + 旧单值 url_prefix
		for _, pre := range s.PrefixList() {
			if strings.HasPrefix(host, pre) {
				return s
			}
		}
		// 匹配正则支持多个：url_regexes 数组 + 旧单值 url_regex
		for _, rg := range s.RegexList() {
			if re, rerr := compileSiteRegex(rg); rerr == nil && re.MatchString(siteRegexTarget(rg, host, link)) {
				return s
			}
		}
	}
	return nil
}

// sortedSites 返回按 sort 升序排列的站点指针（sort<=0 排最后；同级保持数组顺序）
func (p *Parser) sortedSites() []*Site {
	idx := make([]int, 0, len(p.Sites))
	for i := range p.Sites {
		if p.Sites[i].Enabled {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		sa, sb := p.Sites[idx[a]].Sort, p.Sites[idx[b]].Sort
		if sa <= 0 {
			sa = math.MaxInt
		}
		if sb <= 0 {
			sb = math.MaxInt
		}
		return sa < sb
	})
	out := make([]*Site, 0, len(idx))
	for _, i := range idx {
		out = append(out, &p.Sites[i])
	}
	return out
}

// siteRegexTarget 决定正则的作用域：默认只匹配 host；显式 ^ 锚点或含 :// 时才匹配整条 URL
func siteRegexTarget(rg, host, link string) string {
	if strings.HasPrefix(rg, "^") || strings.Contains(rg, "://") {
		return link
	}
	return host
}

// siteRegexCache 站点正则编译缓存：FindSite 在每个出站请求（含每个 TS 片段）都会被调用，
// 逐条 regexp.Compile 代价明显，这里做带上限的进程级缓存。
var siteRegexCache sync.Map // string -> *regexp.Regexp

func compileSiteRegex(rg string) (*regexp.Regexp, error) {
	if v, ok := siteRegexCache.Load(rg); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re, nil
		}
	}
	re, err := regexp.Compile(rg)
	if err != nil {
		return nil, err
	}
	n := 0
	siteRegexCache.Range(func(_, _ any) bool { n++; return false })
	if n > 512 {
		siteRegexCache.Range(func(k, _ any) bool { siteRegexCache.Delete(k); return true })
	}
	siteRegexCache.Store(rg, re)
	return re, nil
}

// SiteByID 按 ID 查找站点（含禁用，供测试页使用）；siteID 为空则回退 FindSite
func (p *Parser) SiteByID(siteID, link string) *Site {
	if siteID != "" {
		for i := range p.Sites {
			if p.Sites[i].ID == siteID {
				return &p.Sites[i]
			}
		}
	}
	return p.FindSite(link)
}

type parseOutcome struct {
	pl         *Playlist
	segs       []Segment
	lines      []string
	hits       [][2]int
	cleanText  string
	adsRemoved int
}

// parse 执行核心解析（不写缓存）
func (p *Parser) parse(link string, site *Site) (*parseOutcome, error) {
	pl, err := ResolvePlaylistOpt(p.Client, link, p.UA, p.Timeout, site.EffIndexMode(), site.StreamPick, site.FixedSub)
	if err != nil {
		return nil, fmt.Errorf("解析失败")
	}
	content, err := Fetch(p.Client, pl.URL, p.UA, p.Timeout)
	if err != nil {
		return nil, fmt.Errorf("解析失败")
	}
	segs, lines := ParseSegments(content)

	// 广告检测：结构型模式（sequence/blockcut/shortblock/slicebatch）跑主检测后，
	// 时长指纹与内容哈希作为补充判据参与合并；纯指纹/哈希模式沿用原有主流程。
	hits := p.detectAds(segs, site, pl.Base)

	cleanText, adsRemoved := BuildClean(lines, segs, hits, pl.Base)
	return &parseOutcome{pl: pl, segs: segs, lines: lines, hits: hits, cleanText: cleanText, adsRemoved: adsRemoved}, nil
}

// 结构型检测方式：这些模式原先只跑结构判据，站点配置的时长指纹被完全忽略，
// 导致「测试页标记学习写入的指纹不生效」。现统一在结构检测后补跑指纹/哈希。
func isStructuralMode(mode string) bool {
	switch mode {
	case "auto", "sequence", "blockcut", "shortblock", "slicebatch", "discpair":
		return true
	}
	return false
}

// tolerance 返回时长匹配容差（未配置时取 0.05）
func (p *Parser) tolerance() float64 {
	if p.Settings != nil && p.Settings.MatchTolerance > 0 {
		return p.Settings.MatchTolerance
	}
	return 0.05
}

// detectAds 广告检测总入口：按站点 detect_mode 选择主判据，再补齐指纹/哈希与保护指纹
func (p *Parser) detectAds(segs []Segment, site *Site, base string) [][2]int {
	mode := strings.ToLower(strings.TrimSpace(site.EffDetectMode()))
	var hits [][2]int
	switch mode {
	case "auto", "sequence":
		// auto：自动（直链站点未配置检测方式时的默认）—— 以 sequence 为主判据，
		// 结构检测后由 finishDetect 补跑时长指纹/内容哈希，避免漏判
		// 判据：广告插播的 TS 与正片不连贯（路径/域名不同 或 序号断裂）
		hits = FindAdsBySequence(segs)
	case "blockcut":
		// 判据：广告块尾片被截断（短于众数时长），块整体被 DISCONTINUITY 包裹
		hits = FindAdsByBlockCut(segs)
	case "shortblock":
		// 判据：整片 DISCONTINUITY 很少、正片块极长，广告是孤立的短块（≤30片/60s）
		hits = FindAdsByShortBlock(segs)
	case "slicebatch":
		// 判据：广告与正片属于两次切片任务，文件 mtime（ETag 前缀/Last-Modified）批次不同
		// 信号不可用时回退 blockcut（尾片截断），避免开了新模式反而漏判既有样本
		hits = FindAdsBySliceBatch(p.Client, segs, base, p.UA, p.Timeout)
		if len(hits) == 0 {
			hits = FindAdsByBlockCut(segs)
		}
	case "discpair":
		// 判据：连续双 DISCONTINUITY 入口 + 块内存在非众数时长（见 discpair.go）
		// 适用：正片均匀切片且分卷频繁插单 DISC 的源站，广告块统一双 DISC 包裹、
		// 块内时长偏离众数；正片分卷虽也双 DISC 但全众数时长，不会被误删
		hits = FindAdsByDiscPair(segs)
	default:
		// 未显式配置 / fingerprint / hash / both：指纹 + 哈希主流程（保持既有行为）
		return p.finishDetect(segs, site, p.detectByFingerprintAndHash(segs, site, base, mode), base, mode)
	}
	return p.finishDetect(segs, site, hits, base, mode)
}

// finishDetect 收尾：先用保护指纹撤销误判区间，再（结构模式下）并入指纹/哈希的补充命中
func (p *Parser) finishDetect(segs []Segment, site *Site, hits [][2]int, base, mode string) [][2]int {
	tol := p.tolerance()

	// 1) 保护指纹优先：命中正片指纹的区间一律撤销，修正「正片被误判为广告」
	hits = ApplyKeepProtect(segs, site, hits, tol)

	// 2) 结构模式下补跑时长指纹：站点规则里配了指纹就应当参与剔除（修复指纹长期失效）
	if isStructuralMode(mode) {
		fpHits := p.detectByFingerprintOnly(segs, site, base)
		if len(fpHits) > 0 {
			fpHits = ApplyKeepProtect(segs, site, fpHits, tol)
			hits = unionHits(hits, fpHits, len(segs))
		}
	}
	return hits
}

// detectByFingerprintOnly 仅按时长指纹检测（结构模式的补充判据），可选哈希确认。
// 使用带广告边界确认的匹配：正片时长与指纹同构（如正片大量 4.0s 段）时，
// 普通滑动匹配会把成片正片误删，边界确认只保留确实处于插播边界（片头/片尾/DISCONTINUITY/时长突变）的命中。
func (p *Parser) detectByFingerprintOnly(segs []Segment, site *Site, base string) [][2]int {
	fps := site.AdsFPList()
	if len(fps) == 0 {
		return nil
	}
	hits := FindAdsMultiBoundary(segs, fps, p.tolerance())
	if len(hits) == 0 {
		return nil
	}
	if p.Settings != nil && p.Settings.UseHashCheck {
		if confirmed, ok := p.confirmByHash(segs, site, base, hits); ok {
			hits = confirmed
		}
	}
	return hits
}

// detectByFingerprintAndHash 纯指纹 / 哈希模式的主流程（原 parse 内 else 分支，行为不变）
func (p *Parser) detectByFingerprintAndHash(segs []Segment, site *Site, base, mode string) [][2]int {
	hits := [][2]int{}

	// 时长指纹匹配
	useDur := mode == "fingerprint" || mode == "both" ||
		site.ParseType == "" || site.ParseType == "duration" || site.ParseType == "both"
	if useDur {
		if fps := site.AdsFPList(); len(fps) > 0 {
			hits = FindAdsMulti(segs, fps, p.tolerance())
		}
	}

	// 内容 SHA256 校验：确认时长命中 + 抓改时长的漏网之鱼（对应 PHP api.php 主流程，带生态兼容兜底）
	useHash := p.Settings != nil && p.Settings.UseHashCheck &&
		(mode == "hash" || mode == "both" || site.ParseType == "" || site.ParseType == "hash" || site.ParseType == "both")
	if useHash {
		if confirmed, ok := p.confirmByHash(segs, site, base, hits); ok {
			hits = confirmed
		}
	}
	return hits
}

// confirmByHash 用内容哈希库校验候选区间；返回 (确认后的区间, 是否采纳)
//
// 采纳规则（按候选来源区分，修复「人工标记学习写入的指纹被哈希否决」）：
//   - 时长指纹命中（hits）：来自站点规则，含测试页标记学习写入的广告素材。
//     「哈希库未收录该素材」不等于「不是广告」，若让哈希否决，新学习的广告将永远删不掉，
//     因此指纹命中一律保留，哈希只做补充确认、不做否决依据。
//   - 结构候选（CandidateByStructure）：纯靠时长结构猜测、无指纹背书，必须经内容哈希确认才采纳，
//     避免仅凭时长巧合误删正片。
//   - 两类都为空时沿用原命中（兜底）。
func (p *Parser) confirmByHash(segs []Segment, site *Site, base string, hits [][2]int) ([][2]int, bool) {
	sampleSize, groups := HashGroupsForSite(site, p.AdsLib)
	if len(groups) == 0 {
		return hits, false
	}
	// 1) 时长指纹命中：直接保留（哈希库未收录不该成为否决理由）
	seen := make(map[[2]int]bool, len(hits)+15)
	confirmed := make([][2]int, 0, len(hits)+8)
	for _, h := range hits {
		if seen[h] {
			continue
		}
		seen[h] = true
		confirmed = append(confirmed, h)
	}
	// 2) 结构候选：无指纹背书，并发采样做内容哈希确认
	// maxCand=8：候选每个至少产生一次采样请求，源站单请求延迟约 1s，候选越多冷启动越慢
	structCands := make([][2]int, 0, 8)
	for _, c := range CandidateByStructure(segs, groups, 8) {
		if !seen[c] {
			seen[c] = true
			structCands = append(structCands, c)
		}
	}
	if len(structCands) > 0 {
		okFlags := verifyCandidates(p.Client, segs, structCands, base, p.UA, sampleSize, groups, p.Timeout)
		for i, ok := range okFlags {
			if ok {
				confirmed = append(confirmed, structCands[i])
			}
		}
	}
	sort.Slice(confirmed, func(i, j int) bool { return confirmed[i][0] < confirmed[j][0] })
	confirmed = mergeSpans(confirmed)
	if len(confirmed) == 0 {
		// 兼容兜底：全部命中被哈希否决时，说明哈希库不含当前站点广告（如新增自定义站点），
		// 回退到纯指纹命中继续剔除，避免"新站永远删不掉广告"
		return hits, false
	}
	return confirmed, true
}

// unionHits 把补充命中并入主命中：合并后广告总段数超过总段数 30% 时放弃补充（宁可不删，不可错删）
func unionHits(main, extra [][2]int, totalSegs int) [][2]int {
	if len(extra) == 0 {
		return main
	}
	if len(main) == 0 {
		if hitSegCount(extra)*10 > totalSegs*3 {
			return main
		}
		return extra
	}
	merged := MergeHits(append(append([][2]int{}, main...), extra...))
	if hitSegCount(merged)*10 > totalSegs*3 {
		return main // 补充后剔除量过大，判为不可信，保持原样
	}
	return merged
}

// hitSegCount 统计命中区间覆盖的片段总数
func hitSegCount(hits [][2]int) int {
	n := 0
	for _, h := range hits {
		if h[1] >= h[0] {
			n += h[1] - h[0] + 1
		}
	}
	return n
}

// mergeSpans 排序后合并重叠 / 相邻的片段区间（1-based，闭区间）
func mergeSpans(spans [][2]int) [][2]int {
	if len(spans) == 0 {
		return spans
	}
	out := [][2]int{spans[0]}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s[0] <= last[1]+1 {
			if s[1] > last[1] {
				last[1] = s[1]
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// newHTTPClient 构造高并发友好的 HTTP 客户端：
// 默认 Transport 的 MaxIdleConnsPerHost 仅为 2，并发采样时会反复重建 TLS 连接，
// 是接口慢的隐藏因素；这里放宽连接池并缩短握手超时。
func newHTTPClient(timeout time.Duration, proxyFn func(*http.Request) (*url.URL, error)) *http.Client {
	if proxyFn == nil {
		proxyFn = http.ProxyFromEnvironment
	}
	tr := &http.Transport{
		Proxy: proxyFn,
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		MaxConnsPerHost:       64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// resTime 生成响应的 Time 字段：{秒,两位小数} + 日期时间 + 星期
func resTime(start time.Time) string {
	return fmt.Sprintf("%.2fs %s", time.Since(start).Seconds(), TimeTok(start))
}

// ParseDetail 解析过程详情（供统计/记录使用）
type ParseDetail struct {
	Site      *Site
	Hits      [][2]int
	CleanText string
	TotalSegs int
	KeptSegs  int
	AdsSegs   int
	FromCache bool
	SubURL    string
	BaseURL   string
}

// Clean 对外主接口：url -> 净化后缓存地址（对应 PHP api.php 主流程）
func (p *Parser) Clean(link, cacheBase string) (*Res, *ParseDetail) {
	start := time.Now()

	site := p.FindSite(link)
	if site == nil {
		return &Res{Code: 404, Msg: "非本站资源"}, nil
	}

	key := CacheKey(link)
	subdir := site.CacheSubdir
	cacheURL := p.Cache.URL(subdir, key, cacheBase)

	// 直链播放（parse_type=direct）：解析并检测广告，但命中 0 段广告时直接回源站地址，
	// 不读不写本地缓存——没有插播广告的片源无需再造一份净化副本
	if site.IsDirect() {
		oc, err := p.parse(link, site)
		if err != nil {
			return &Res{Code: 404, Msg: err.Error()}, nil
		}
		if oc.adsRemoved == 0 {
			src := strings.TrimSpace(oc.pl.URL)
			if src == "" {
				src = link
			}
			return &Res{Code: 200, Msg: "解析成功（直链源址）", Origin: SiteOrigin(site), Link: link, URL: src, Time: resTime(start)},
				&ParseDetail{Site: site, Hits: nil, CleanText: oc.cleanText,
					TotalSegs: len(oc.segs), KeptSegs: len(oc.segs), AdsSegs: 0,
					SubURL: oc.pl.URL, BaseURL: oc.pl.Base}
		}
		// 有广告：与常规流程一致，落净化缓存后返回本地地址
		_ = p.Cache.Write(subdir, key, oc.cleanText)
		return &Res{Code: 200, Msg: "解析成功", Origin: SiteOrigin(site), Link: link, URL: cacheURL, Time: resTime(start)},
			&ParseDetail{Site: site, Hits: oc.hits, CleanText: oc.cleanText,
				TotalSegs: len(oc.segs), KeptSegs: len(oc.segs) - oc.adsRemoved, AdsSegs: oc.adsRemoved,
				SubURL: oc.pl.URL, BaseURL: oc.pl.Base}
	}

	// 命中有效缓存
	if ok, mt := p.Cache.Hit(subdir, key); ok && time.Since(mt) < p.Cache.TTL {
		return &Res{Code: 200, Msg: "解析成功", Origin: SiteOrigin(site), Link: link, URL: cacheURL, Time: resTime(start)},
			&ParseDetail{Site: site, FromCache: true}
	}

	oc, err := p.parse(link, site)
	if err != nil {
		return &Res{Code: 404, Msg: err.Error()}, nil
	}

	// 原子落盘缓存
	_ = p.Cache.Write(subdir, key, oc.cleanText)

	return &Res{Code: 200, Msg: "解析成功", Origin: SiteOrigin(site), Link: link, URL: cacheURL, Time: resTime(start)},
		&ParseDetail{Site: site, Hits: oc.hits, CleanText: oc.cleanText,
			TotalSegs: len(oc.segs), KeptSegs: len(oc.segs) - oc.adsRemoved, AdsSegs: oc.adsRemoved,
			SubURL: oc.pl.URL, BaseURL: oc.pl.Base}
}

// PreviewResult M3U8 测试页返回结构
type PreviewResult struct {
	SiteName   string        `json:"site_name"`
	SiteID     string        `json:"site_id"`
	OriginText string        `json:"origin_text"`
	CleanText  string        `json:"clean_text"`
	TotalSegs  int           `json:"total_segs"`
	AdsSegs    int           `json:"ads_segs"`
	KeptSegs   int           `json:"kept_segs"`
	Hits       [][2]int      `json:"hits"`     // 广告区间，1-based 段号闭区间
	Segments   []SegmentView `json:"segments"` // 片段明细（含补全后的绝对地址）
	SubURL     string        `json:"sub_url"`
	BaseURL    string        `json:"base_url"`
	ElapsedMs  int64         `json:"elapsed_ms"`
	CleanedAt  string        `json:"cleaned_at"`
	Proxy      string        `json:"proxy"`      // 本次解析出网实际使用的代理（站点级/代理池），直连为空
	Direct     bool          `json:"direct"`     // 站点是否为直链播放（parse_type=direct）
	DirectURL  string        `json:"direct_url"` // 直链站点且未检测到广告时对外输出的源站地址（否则为空）
	Token      string        `json:"token"`      // 播放预览凭证（web 层生成）
	Ok         bool          `json:"ok"`
	Msg        string        `json:"msg"`
}

// Preview 供后台 M3U8 测试页使用：不查询/不写缓存，直接解析对比
func (p *Parser) Preview(link, siteID string) *PreviewResult {
	start := time.Now()
	res := &PreviewResult{Ok: false}

	if !isHTTPLink(strings.TrimSpace(link)) {
		res.Msg = "地址必须为 http(s) 链接"
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}
	site := p.SiteByID(siteID, link)
	if site == nil {
		res.Msg = "非本站资源或未匹配到站点规则"
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}

	oc, err := p.parse(link, site)
	if err != nil {
		res.Msg = err.Error()
		res.ElapsedMs = time.Since(start).Milliseconds()
		return res
	}

	res.SiteName = site.Name
	res.SiteID = site.ID
	res.OriginText = oc.pl.Content
	res.CleanText = oc.cleanText
	res.TotalSegs = len(oc.segs)
	res.AdsSegs = oc.adsRemoved
	res.KeptSegs = len(oc.segs) - oc.adsRemoved
	res.Hits = oc.hits
	res.Segments = buildSegmentViews(oc.segs, oc.hits, oc.pl.Base)
	res.SubURL = oc.pl.URL
	res.BaseURL = oc.pl.Base
	res.ElapsedMs = time.Since(start).Milliseconds()
	res.CleanedAt = TimeTok(time.Now())
	res.Proxy = p.LastProxy(link)
	res.Direct = site.IsDirect()
	if res.Direct && res.AdsSegs == 0 {
		res.DirectURL = oc.pl.URL
	}
	res.Ok = true
	return res
}

// buildSegmentViews 生成片段明细：相对地址补全为可直链访问的绝对地址，并标注广告命中与起始时间
func buildSegmentViews(segs []Segment, hits [][2]int, base string) []SegmentView {
	adMap := make(map[int]bool, len(hits)*2)
	for _, h := range hits {
		for i := h[0]; i <= h[1]; i++ {
			adMap[i] = true
		}
	}
	out := make([]SegmentView, 0, len(segs))
	start := 0.0
	for _, sg := range segs {
		u := sg.Name
		if !httpRe.MatchString(strings.TrimSpace(u)) {
			u = AbsFromDir(base, strings.TrimSpace(u))
		}
		out = append(out, SegmentView{
			Index: sg.Idx,
			Dur:   sg.Dur,
			URI:   sg.Name,
			URL:   u,
			Ad:    adMap[sg.Idx],
			Start: start,
		})
		start += sg.Dur
	}
	return out
}
