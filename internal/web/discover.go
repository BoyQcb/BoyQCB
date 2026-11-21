package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dytt_parser_go/internal/config"
	"dytt_parser_go/internal/core"
	"dytt_parser_go/internal/store"
)

const (
	defaultDiscoverPages = 20
	// maxDiscoverPages 仅作安全阀：页数完全跟随前端「扫描页数」输入，这里只防误填超大值把任务拖死
	maxDiscoverPages    = 5000
	discoverConcurrency = 12 // 并发下限（页数少时保守，避免压垮目标站）
	discoverMaxWorkers  = 40 // 并发上限
	discoverListLimit   = 200
)

// discoverWorkers 按请求页数自适应并发：页数越多并发越高，页数少时保持保守
func discoverWorkers(pages int) int {
	switch {
	case pages <= 0:
		return discoverConcurrency
	case pages <= 20:
		return discoverConcurrency
	case pages <= 60:
		return 16
	case pages <= 120:
		return 24
	case pages <= 300:
		return 32
	default:
		return discoverMaxWorkers
	}
}

// ---------- 通过苹果 CMS 采集接口批量发现播放域名 ----------
//
// 思路：一个资源站往往有多个播放域名（镜像/轮播），逐个手工加规则很慢。
// 这里直接抓采集接口的若干页数据，把所有播放地址按域名聚合，
// 得到「域名 -> 出现次数/样本地址/是否已在规则中」，再对样本一键探测特征码并落规则。

// PresetSource 预置采集源
type PresetSource struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Kind string `json:"kind"` // xml / json
}

// presetSources 预置采集源：数据源统一取自 config.PresetSources，避免两处维护
var presetSources = presetFromConfig()

func presetFromConfig() []PresetSource {
	out := make([]PresetSource, 0, len(config.PresetSources))
	for _, s := range config.PresetSources {
		out = append(out, PresetSource{Name: s.Name, URL: s.APIURL, Kind: s.Kind})
	}
	return out
}

var (
	reAnyURL   = regexp.MustCompile(`https?://[^\s#$"'<>\\]+`)
	reAssetExt = regexp.MustCompile(`(?i)\.(jpg|jpeg|png|webp|gif|bmp|css|js|woff2?|svg|ico)(\?|$)`)
	reM3u8Abs  = regexp.MustCompile(`https?://[^\s"'<>\\]+?\.m3u8[^\s"'<>\\]*`)
	reM3u8Any  = regexp.MustCompile(`["']([^\s"'<>\\]{2,300}?\.m3u8[^\s"'<>\\]*)["']`)
	reVodName  = regexp.MustCompile(`(?s)<name><!\[CDATA\[(.*?)\]\]></name>`)
)

// discoverHost 域名聚合结果
type discoverHost struct {
	Host        string `json:"host"`
	Count       int    `json:"count"`
	Kind        string `json:"kind"` // m3u8(直链) / page(播放页，需二次解析)
	Sample      string `json:"sample"`
	Vod         string `json:"vod"`
	Configured  bool   `json:"configured"`
	SiteID      string `json:"site_id"`
	SiteName    string `json:"site_name"`
	New         bool   `json:"new"`          // 本次扫描首次发现
	Hits        int    `json:"hits"`         // 历史累计出现次数
	FirstSeen   string `json:"first_seen"`   // 历史首次发现时间
	LastSeen    string `json:"last_seen"`    // 历史最近出现时间
	HistoryOnly bool   `json:"history_only"` // 本次没扫到，仅存在于历史库
}

// hostGroup 同族域名（骨架相同、编号位不同）的泛化建议
type hostGroup struct {
	Pattern  string   `json:"pattern"`  // 精确泛化正则，如 cdn\.yzzy\d+\-play\.com
	Label    string   `json:"label"`    // 人类可读形式，如 cdn.yzzy*-play.com
	Members  []string `json:"members"`  // 该族已发现的域名
	Hits     int      `json:"hits"`     // 该族累计出现次数
	Variants int      `json:"variants"` // 编号取值个数（>1 说明确实存在轮播编号）
	Loose    string   `json:"loose"`    // 宽松兜底正则（core.GenericRegex）
}

// discoverMeta 采集接口的分页元信息
type discoverMeta struct {
	PageSize  int    `json:"pagesize"`
	PageCount int    `json:"pagecount"`
	Total     int    `json:"total"`
	Strategy  string `json:"strategy"`
	Requested int    `json:"pages_requested"`
	Scanned   int    `json:"pages_scanned"`
	PageList  []int  `json:"page_list"`
}

// handleDiscover GET 返回预置采集源；POST 扫描采集接口聚合播放域名
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "sources": presetSources})
		return
	case http.MethodPost:
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}

	var body struct {
		APIURL   string `json:"api_url"`
		Pages    int    `json:"pages"`
		Strategy string `json:"strategy"` // stride(全库均匀跳页) / seq(连续翻页) / random(随机页)
		History  *bool  `json:"history"`  // 是否合并历史库，默认 true
		ProxyURL string `json:"proxy_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
		return
	}
	api := strings.TrimSpace(body.APIURL)
	if api == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "请填写采集接口地址"})
		return
	}
	if body.Pages <= 0 {
		body.Pages = defaultDiscoverPages
	}
	if body.Pages > maxDiscoverPages {
		body.Pages = maxDiscoverPages
	}
	strategy := strings.ToLower(strings.TrimSpace(body.Strategy))
	if strategy == "" {
		strategy = "stride"
	}
	useHistory := true
	if body.History != nil {
		useHistory = *body.History
	}

	p := s.parser_()
	if c := s.tempProxyClient(body.ProxyURL, p.Timeout); c != nil {
		p = p.CloneWithClient(c)
	}

	// 单页超时上限：原解析超时一般 30s，扫描时个别慢页会把整批拖尾，这里压到 25s
	pageTimeout := p.Timeout
	if pageTimeout <= 0 || pageTimeout > 25*time.Second {
		pageTimeout = 25 * time.Second
	}

	// 先抓首页：既计入统计，也用于读分页元信息（pagecount / pagesize / total），
	// 据此在全库范围内取页，而不是傻翻前 N 页（每页条数被站方锁死，前几页覆盖不到全库）
	firstBody, firstErr := core.Fetch(p.Client, withPage(api, 1), p.UA, pageTimeout)
	meta := parsePageMeta(firstBody)
	meta.Strategy = strategy
	meta.Requested = body.Pages

	pageNums := pageList(meta, strategy, body.Pages)
	rest := make([]int, 0, len(pageNums))
	firstDone := false
	for _, pg := range pageNums {
		if pg == 1 {
			if firstDone {
				continue
			}
			firstDone = true
		}
		rest = append(rest, pg)
	}
	if firstDone {
		rest = rest[1:]
	}

	type pageRes struct {
		pg   int
		body string
		err  error
	}
	// 并发随页数自适应：页数多时并发更高，页数少时保守，避免把目标站压垮
	workers := discoverWorkers(len(rest))
	outs := make([]pageRes, len(rest))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, pgNum := range rest {
		wg.Add(1)
		go func(idx, pg int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := core.Fetch(p.Client, withPage(api, pg), p.UA, pageTimeout)
			outs[idx] = pageRes{pg, b, err}
		}(i, pgNum)
	}
	wg.Wait()
	sort.Slice(outs, func(i, j int) bool { return outs[i].pg < outs[j].pg })

	hosts := map[string]*discoverHost{}
	vodCount := 0
	scanned := 0
	var lastErr string
	if firstErr == nil && strings.TrimSpace(firstBody) != "" {
		scanned++
		vodCount += collectHosts(firstBody, hosts)
	} else if firstErr != nil {
		lastErr = firstErr.Error()
	}
	for i := range outs {
		if outs[i].err != nil {
			lastErr = outs[i].err.Error()
			continue
		}
		scanned++
		vodCount += collectHosts(outs[i].body, hosts)
	}
	meta.Scanned = scanned
	meta.PageList = pageNums

	// 合并历史库：本次扫到的累加出现次数；历史见过但本次没扫到的补进结果，
	// 避免「这次没抽到就当不存在」——每页条数被锁死时，多次扫描才等于一次全量
	newHosts, historyTotal := 0, 0
	if s.disc != nil {
		stats := make([]store.HostStat, 0, len(hosts))
		for h, v := range hosts {
			stats = append(stats, store.HostStat{Host: h, Sample: v.Sample, Kind: v.Kind, Vod: v.Vod, Count: v.Count})
		}
		before := s.disc.Snapshot(api)
		hist := before
		if useHistory {
			hist, newHosts = s.disc.Merge(api, stats)
		}
		for h, hv := range hist {
			if cur, ok := hosts[h]; ok {
				cur.Hits = hv.Hits
				cur.FirstSeen = hv.FirstSeen
				cur.LastSeen = hv.LastSeen
				if _, seen := before[h]; !seen {
					cur.New = true
				}
			} else {
				hosts[h] = &discoverHost{
					Host:        h,
					Kind:        hv.Kind,
					Sample:      hv.Sample,
					Vod:         hv.Vod,
					Hits:        hv.Hits,
					FirstSeen:   hv.FirstSeen,
					LastSeen:    hv.LastSeen,
					HistoryOnly: true,
				}
			}
		}
		historyTotal = len(hist)
	}

	list := make([]*discoverHost, 0, len(hosts))
	for _, h := range hosts {
		list = append(list, h)
	}
	for _, h := range list {
		if h.Hits == 0 {
			h.Hits = h.Count
		}
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.HistoryOnly != b.HistoryOnly {
			return !a.HistoryOnly // 本次扫到的排前面
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Hits != b.Hits {
			return a.Hits > b.Hits
		}
		return a.Host < b.Host
	})
	if len(list) > discoverListLimit {
		list = list[:discoverListLimit]
	}

	// 标记哪些域名已有规则覆盖
	cfg := s.cfgMgr.Snapshot()
	for _, h := range list {
		for _, st := range cfg.Sites {
			if siteMatchHost(st, h.Host) {
				h.Configured = true
				h.SiteID = st.ID
				h.SiteName = st.Name
				break
			}
		}
	}
	if scanned == 0 {
		msg := "采集接口无响应"
		if lastErr != "" {
			msg = "采集接口请求失败: " + lastErr
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": msg})
		return
	}
	s.logger.Write("INFO", "admin", "域名发现 "+api+" 扫描"+itoa(scanned)+"页("+strategy+") -> "+itoa(len(list))+"个域名，新增"+itoa(newHosts)+"个")
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": map[string]interface{}{
		"api_url":       api,
		"pages_scanned": scanned,
		"vod_count":     vodCount,
		"new_hosts":     newHosts,
		"history_total": historyTotal,
		"meta":          meta,
		"hosts":         list,
		"groups":        buildGroups(list),
	}})
}

// batchReq 批量添加未配置域名的请求体
type batchReq struct {
	Mode         string   `json:"mode"`          // append=追加到已有规则 / new=新建规则
	SiteID       string   `json:"site_id"`       // append 模式下的目标站点
	SourceID     string   `json:"source_id"`     // 来源资源站：保证「一个资源站 = 一条规则」
	SourceName   string   `json:"source_name"`   // 资源站名称，new 模式下作为规则名
	Hosts        []string `json:"hosts"`         // 勾选的域名列表
	DetectMode   string   `json:"detect_mode"`   // new 模式：sequence / fingerprint / path
	IndexMode    string   `json:"index_mode"`    // new 模式：auto / single
	StreamPick   string   `json:"stream_pick"`   // new 模式：first / highest / lowest
	ParseType    string   `json:"parse_type"`    // new 模式：duration / count / size
	GenericRegex bool     `json:"generic_regex"` // 是否同时写入宽松泛化正则（兜底同系列编号域名）
	ExtraRegexes []string `json:"extra_regexes"` // 额外写入的精确泛化正则（域名族建议，如 cdn\.yzzy\d+\-play\.com）
}

// handleDiscoverBatch POST 把勾选的多个未配置域名一次性写入规则
func (s *Server) handleDiscoverBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	var req batchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
		return
	}
	hosts := make([]string, 0, len(req.Hosts))
	seen := map[string]bool{}
	for _, h := range req.Hosts {
		h = normalizeHost(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	if len(hosts) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "请先勾选域名"})
		return
	}

	cfg := s.cfgMgr.Snapshot()
	// 以当前快照判断覆盖情况，避免新增过程中互相干扰
	pending := make([]string, 0, len(hosts))
	skipped := make([]string, 0, len(hosts))
	for _, h := range hosts {
		hit := false
		for _, st := range cfg.Sites {
			if siteMatchHost(st, h) {
				hit = true
				break
			}
		}
		if hit {
			skipped = append(skipped, h)
		} else {
			pending = append(pending, h)
		}
	}
	if len(pending) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "msg": "所选域名均已被现有规则覆盖", "added": []string{}, "skipped": skipped,
		})
		return
	}

	// 一个资源站永远只对应一条规则：资源站已绑定规则时，强制走追加
	if strings.TrimSpace(req.SiteID) == "" && strings.TrimSpace(req.SourceID) != "" {
		for _, sr := range cfg.Sources {
			if sr.ID == req.SourceID && strings.TrimSpace(sr.SiteID) != "" {
				req.SiteID = sr.SiteID
				break
			}
		}
	}
	for _, st := range cfg.Sites {
		if st.ID == req.SiteID {
			req.Mode = "append"
			break
		}
	}

	var msg string
	if req.Mode == "append" {
		idx := -1
		for i := range cfg.Sites {
			if cfg.Sites[i].ID == req.SiteID {
				idx = i
				break
			}
		}
		if idx < 0 {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "目标站点不存在"})
			return
		}
		st := cfg.Sites[idx]
		preSet, reSet := map[string]bool{}, map[string]bool{}
		for _, p := range st.PrefixList() {
			preSet[p] = true
		}
		for _, p := range st.RegexList() {
			reSet[p] = true
		}
		st.URLPrefixes = append([]string{}, st.PrefixList()...)
		st.URLRegexes = append([]string{}, st.RegexList()...)
		for _, h := range pending {
			if !preSet[h] {
				st.URLPrefixes = append(st.URLPrefixes, h)
				preSet[h] = true
			}
			if req.GenericRegex {
				re := core.GenericRegex(h)
				if !reSet[re] {
					st.URLRegexes = append(st.URLRegexes, re)
					reSet[re] = true
				}
			}
		}
		for _, re := range req.ExtraRegexes {
			re = strings.TrimSpace(re)
			if re == "" || reSet[re] {
				continue
			}
			if _, err := regexp.Compile(re); err != nil {
				continue
			}
			st.URLRegexes = append(st.URLRegexes, re)
			reSet[re] = true
		}
		normalizeMatchers(&st)
		if err := s.cfgMgr.Update(func(c *config.File) error {
			for i := range c.Sites {
				if c.Sites[i].ID == st.ID {
					c.Sites[i] = st
					return nil
				}
			}
			return errors.New("站点不存在: " + st.ID)
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
			return
		}
		msg = fmt.Sprintf("已向 [%s] 追加 %d 个域名前缀", st.Name, len(pending))
	} else {
		// 一个资源站 = 一条规则：勾选的所有域名写入同一条规则的 URL 匹配前缀
		name := strings.TrimSpace(req.SourceName)
		if name == "" {
			name = core.HostMain(pending[0])
		}
		idBase := core.SlugID(name)
		if idBase == "" || idBase == "site" {
			idBase = core.SlugID(core.HostMain(pending[0]))
		}
		if idBase == "" {
			idBase = "site"
		}
		existID := map[string]bool{}
		for _, st := range cfg.Sites {
			existID[st.ID] = true
		}
		id := idBase
		for n := 2; existID[id]; n++ {
			id = fmt.Sprintf("%s-%d", idBase, n)
		}
		st := core.Site{
			ID:          id,
			Name:        name,
			Enabled:     true,
			DetectMode:  orDefault(req.DetectMode, "sequence"),
			IndexMode:   orDefault(req.IndexMode, "auto"),
			StreamPick:  orDefault(req.StreamPick, "first"),
			ParseType:   orDefault(req.ParseType, "duration"),
			CacheSubdir: core.GenericName(idBase),
			URLPrefixes: append([]string{}, pending...),
		}
		if req.GenericRegex {
			reSet := map[string]bool{}
			regs := make([]string, 0, len(pending))
			for _, h := range pending {
				re := core.GenericRegex(h)
				if !reSet[re] {
					reSet[re] = true
					regs = append(regs, re)
				}
			}
			st.URLRegexes = regs
		}
		for _, re := range req.ExtraRegexes {
			re = strings.TrimSpace(re)
			if re == "" {
				continue
			}
			if _, err := regexp.Compile(re); err != nil {
				continue
			}
			dup := false
			for _, x := range st.URLRegexes {
				if x == re {
					dup = true
					break
				}
			}
			if !dup {
				st.URLRegexes = append(st.URLRegexes, re)
			}
		}
		normalizeMatchers(&st)
		if err := s.cfgMgr.Update(func(c *config.File) error {
			c.Sites = append(c.Sites, st)
			// 资源站与规则一对一绑定，后续扫描自动改为追加，不再新建
			if strings.TrimSpace(req.SourceID) != "" {
				for i := range c.Sources {
					if c.Sources[i].ID == req.SourceID {
						c.Sources[i].SiteID = st.ID
					}
				}
			}
			return nil
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"code": 500, "msg": "保存失败: " + err.Error()})
			return
		}
		msg = fmt.Sprintf("已为 [%s] 新建 1 条规则，覆盖 %d 个域名", name, len(pending))
	}

	s.reload()
	s.logger.Write("INFO", "discover", fmt.Sprintf("批量添加域名: %s（跳过 %d 个已覆盖）", msg, len(skipped)))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"code": 200, "msg": msg, "added": pending, "skipped": skipped,
	})
}

// normalizeHost 把用户勾选的域名归一化成纯 host
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	return strings.TrimSpace(strings.TrimSuffix(h, ":"))
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// handleDiscoverResolve POST {page_url} 播放页 -> 真实 m3u8（采集到的是 share/play 页时需要）
func (s *Server) handleDiscoverResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
		return
	}
	var body struct {
		PageURL  string `json:"page_url"`
		ProxyURL string `json:"proxy_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "参数错误: " + err.Error()})
		return
	}
	page := strings.TrimSpace(body.PageURL)
	if page == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "缺少播放页地址"})
		return
	}
	p := s.parser_()
	if c := s.tempProxyClient(body.ProxyURL, p.Timeout); c != nil {
		p = p.CloneWithClient(c)
	}
	html, err := core.Fetch(p.Client, page, p.UA, p.Timeout)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "播放页抓取失败: " + err.Error()})
		return
	}
	base, _ := url.Parse(page)
	var found []string
	seen := map[string]bool{}
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		found = append(found, u)
	}
	for _, m := range reM3u8Abs.FindAllString(html, -1) {
		add(m)
	}
	for _, m := range reM3u8Any.FindAllStringSubmatch(html, -1) {
		if len(m) > 1 {
			add(absolutize(base, m[1]))
		}
	}
	if len(found) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 404, "msg": "该播放页未提取到 m3u8（可能需要浏览器渲染或已失效）"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": map[string]interface{}{
		"page_url": page,
		"m3u8":     found[0],
		"all":      found,
	}})
}

// collectHosts 从一页采集数据中提取播放地址并按域名聚合，返回该页视频数
func collectHosts(body string, hosts map[string]*discoverHost) int {
	trimmed := strings.TrimSpace(body)
	vodCount := 0
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		vodCount += collectJSON(body, hosts)
		return vodCount
	}
	// XML：逐个 <video> 块，优先取 <vod_play_url> 播放地址（苹果CMS标准）。
	// 该字段缺失/无 http 链接时，退回提取块内全部链接——播放特征由 addHost 统一把关，
	// 简介/下载外链若带播放路径特征仍会被统计，但不会再混入纯外链。
	// 按索引顺序切块，避免 strings.Split 一次性切出整页所有分片（大页内存/GC 开销明显）
	for idx := strings.Index(body, "<video>"); idx >= 0; {
		start := idx + len("<video>")
		var b string
		if end := strings.Index(body[start:], "<video>"); end >= 0 {
			b = body[start : start+end]
			idx = start + end
		} else {
			b = body[start:]
			idx = -1
		}
		vodCount++
		name := ""
		if m := reVodName.FindStringSubmatch(b); len(m) > 1 {
			name = m[1]
		}
		playURLs := collectXMLPlayURL(b)
		if len(playURLs) > 0 {
			for _, u := range playURLs {
				addHost(hosts, u, name)
			}
			continue
		}
		for _, u := range reAnyURL.FindAllString(b, -1) {
			addHost(hosts, u, name)
		}
	}
	if vodCount == 0 {
		// 兼容非标准 XML（无 video 标签）：直接提取播放地址，退回整页链接
		playURLs := collectXMLPlayURL(body)
		if len(playURLs) == 0 {
			playURLs = reAnyURL.FindAllString(body, -1)
		}
		for _, u := range playURLs {
			addHost(hosts, u, "")
		}
	}
	return vodCount
}

// reVodPlayXMLBlock 苹果 CMS XML 的 <vod_play_url> 块（内容可能含 CDATA）
var reVodPlayXMLBlock = regexp.MustCompile(`(?is)<vod_play_url>(.*?)</vod_play_url>`)

// collectXMLPlayURL 提取 XML 中 <vod_play_url> 标签内的播放器地址列表：
// 支持 CDATA 包裹与实体转义，按苹果CMS分隔符（$ 播放器分割 / # 集数分割）拆分成 URL。
func collectXMLPlayURL(block string) []string {
	raw := []string{}
	for _, m := range reVodPlayXMLBlock.FindAllStringSubmatch(block, -1) {
		if len(m) < 2 {
			continue
		}
		v := strings.TrimSpace(m[1])
		v = strings.TrimPrefix(v, "<![CDATA[")
		v = strings.TrimSuffix(v, "]]>")
		v = strings.ReplaceAll(v, "&amp;", "&")
		v = strings.ReplaceAll(v, "&lt;", "<")
		v = strings.ReplaceAll(v, "&gt;", ">")
		for _, part := range strings.FieldsFunc(v, func(c rune) bool { return c == '#' || c == '$' }) {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "http") {
				raw = append(raw, part)
			}
		}
	}
	return raw
}

// 苹果 CMS JSON 采集接口的字段正则（包级编译一次，避免每页重复 MustCompile）
var (
	reVodPlayURL = regexp.MustCompile(`"vod_play_url"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	reVodNameJ   = regexp.MustCompile(`"vod_name"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// collectJSON 解析苹果 CMS json 采集接口的 vod_play_url 字段
func collectJSON(body string, hosts map[string]*discoverHost) int {
	// 轻量提取：直接找 "vod_play_url":"...." 的值，避免依赖完整结构定义
	re := reVodPlayURL
	reName := reVodNameJ
	names := reName.FindAllStringSubmatch(body, -1)
	count := 0
	for i, m := range re.FindAllStringSubmatch(body, -1) {
		count++
		name := ""
		if i < len(names) && len(names[i]) > 1 {
			name = unescapeJSON(names[i][1])
		}
		val := unescapeJSON(m[1])
		// 格式：第1集$url#第2集$url 或 播放器$url$$$播放器$url
		for _, part := range strings.FieldsFunc(val, func(c rune) bool { return c == '#' || c == '$' }) {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "http") {
				addHost(hosts, part, name)
			}
		}
	}
	return count
}

// nonPlayHosts 非播放域名：影片简介/外链里常见的站点，不是播放 CDN，不该进域名表
var nonPlayHosts = []string{
	"wikipedia.org", "baike.so.com", "baike.baidu.com", "baidu.com",
	"douban.com", "weibo.com", "toutiao.com", "douyin.com", "kuaishou.com",
	"taobao.com", "tmall.com", "jd.com", "gov.cn", "sogou.com", "360.cn",
	"qq.com", "163.com", "sohu.com", "sina.com", "csdn.net", "zhihu.com",
}

// isNonPlayHost 判断域名是否属于黑名单（含子域）
func isNonPlayHost(host string) bool {
	host = strings.ToLower(host)
	for _, s := range nonPlayHosts {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// rePlayPath 常见播放页路径特征（资源站播放地址：/play/123-1-1.html、/vodplay/123、/detail/123 等）。
// 用于把「播放地址」与「简介外链 / 官网链接 / 下载地址」区分开，
// 避免域名发现页混入非当前站点使用的域名。
// 注意：刻意不收录单字符段（v/p）——/v1/、/p2/ 在简介/静态资源里太常见，误命中率高。
var rePlayPath = regexp.MustCompile(`(?i)(^|/)(play|vodplay|vod/play|detail|show|movie|view|watch|video)([/_.-]\s*\d)`)

// reVodParam 苹果CMS播放参数特征：m=vod-play、m=vod-detail-id-123、m=vod-123 等；
// 单独写 m=vod 泛匹配会命中 m=vod-search / m=vod-list 等非播放操作，故收紧。
var reVodParam = regexp.MustCompile(`(?i)m=vod-(play|detail|show|x|view)([^\s&]*)`)

// isPlayURL 判断 URL 是否具备播放特征：
// m3u8 / mp4 / flv 直链、播放页路径（play/vodplay/detail/show 等 + 数字）、或苹果CMS播放参数（m=vod-play / ac=videolist）。
func isPlayURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	path := strings.ToLower(u.Path)
	if strings.Contains(path, ".m3u8") || strings.Contains(path, ".mp4") || strings.Contains(path, ".flv") {
		return true
	}
	q := strings.ToLower(u.RawQuery)
	if reVodParam.MatchString(q) || strings.Contains(q, "ac=videolist") {
		return true
	}
	return rePlayPath.MatchString(path)
}

// addHost 把一条播放地址计入域名统计（仅接受具备播放特征的地址，过滤简介外链/官网/下载链接等）
func addHost(hosts map[string]*discoverHost, raw, vod string) {
	u := strings.TrimRight(raw, "]}>)")
	if reAssetExt.MatchString(u) {
		return
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" {
		return
	}
	if !isPlayURL(pu) {
		return
	}
	host := pu.Host
	lower := strings.ToLower(host)
	if strings.Contains(lower, "img") || strings.Contains(lower, "pic") || strings.Contains(lower, "static") || strings.Contains(lower, "caiji.") {
		return
	}
	if isNonPlayHost(host) {
		return
	}
	kind := "page"
	if strings.Contains(strings.ToLower(pu.Path), ".m3u8") {
		kind = "m3u8"
	}
	h, ok := hosts[host]
	if !ok {
		h = &discoverHost{Host: host, Kind: kind, Sample: u, Vod: vod}
		hosts[host] = h
	}
	h.Count++
	if kind == "m3u8" && h.Kind != "m3u8" {
		h.Kind = "m3u8"
		h.Sample = u
	}
}

// siteMatchHost 判断某站点规则是否已覆盖该域名
func siteMatchHost(st core.Site, host string) bool {
	for _, pre := range st.PrefixList() {
		if strings.Contains(host, pre) {
			return true
		}
	}
	for _, rg := range st.RegexList() {
		if re, err := regexp.Compile(rg); err == nil && re.MatchString(host) {
			return true
		}
	}
	return false
}

// withPage 给采集接口补充分页参数
func withPage(api string, pg int) string {
	sep := "?"
	if strings.Contains(api, "?") {
		sep = "&"
	}
	if strings.Contains(api, "ac=") {
		return api + sep + "pg=" + strconv.Itoa(pg)
	}
	return api + sep + "ac=videolist&pg=" + strconv.Itoa(pg)
}

// absolutize 把相对地址补全为绝对地址
func absolutize(base *url.URL, p string) string {
	p = strings.TrimSpace(p)
	if p == "" || base == nil {
		return p
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	if strings.HasPrefix(p, "//") {
		return base.Scheme + ":" + p
	}
	if strings.HasPrefix(p, "/") {
		return base.Scheme + "://" + base.Host + p
	}
	dir := base.Path
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i+1]
	}
	return base.Scheme + "://" + base.Host + dir + strings.TrimPrefix(p, "./")
}

// unescapeJSON 还原 json 字符串中的常见转义
func unescapeJSON(s string) string {
	r := strings.NewReplacer(`\/`, "/", `\"`, `"`, `\n`, "", `\r`, "")
	return r.Replace(s)
}

// ---------- 分页元信息 / 取页策略 / 域名族泛化 ----------

var (
	reJSONPageCount = regexp.MustCompile(`"pagecount"\s*:\s*"?(\d+)"?`)
	reJSONTotal     = regexp.MustCompile(`"total"\s*:\s*"?(\d+)"?`)
	reJSONPageSize  = regexp.MustCompile(`"(?:limit|pagesize)"\s*:\s*"?(\d+)"?`)
	reXMLPageCount  = regexp.MustCompile(`(?i)pagecount=["\']?(\d+)`)
	reXMLPageSize   = regexp.MustCompile(`(?i)pagesize=["\']?(\d+)`)
	reXMLRecord     = regexp.MustCompile(`(?i)recordcount=["\']?(\d+)`)
)

// parsePageMeta 从采集接口返回中解析分页元信息（JSON 与 XML 通用）
func parsePageMeta(body string) discoverMeta {
	m := discoverMeta{}
	t := strings.TrimSpace(body)
	if t == "" {
		return m
	}
	atoi := func(s string) int {
		n, _ := strconv.Atoi(s)
		return n
	}
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if g := reJSONPageCount.FindStringSubmatch(t); len(g) > 1 {
			m.PageCount = atoi(g[1])
		}
		if g := reJSONTotal.FindStringSubmatch(t); len(g) > 1 {
			m.Total = atoi(g[1])
		}
		if g := reJSONPageSize.FindStringSubmatch(t); len(g) > 1 {
			m.PageSize = atoi(g[1])
		}
		return m
	}
	if g := reXMLPageCount.FindStringSubmatch(t); len(g) > 1 {
		m.PageCount = atoi(g[1])
	}
	if g := reXMLPageSize.FindStringSubmatch(t); len(g) > 1 {
		m.PageSize = atoi(g[1])
	}
	if g := reXMLRecord.FindStringSubmatch(t); len(g) > 1 {
		m.Total = atoi(g[1])
	}
	return m
}

// pageList 按策略生成要抓取的页码
//
// stride（默认）：在 1..pagecount 之间均匀取点，跨全库时间线采样。
// 采集接口每页条数是站方锁死的（常见 20 条），连续翻前几页只能覆盖最新那批影片，
// 而播放域名是按影片分布的，均匀跳页才能覆盖到不同时期、不同域名。
func pageList(m discoverMeta, strategy string, want int) []int {
	if want <= 0 {
		want = defaultDiscoverPages
	}
	if m.PageCount <= 0 {
		// 拿不到总页数（接口不返回 pagecount）：退化为连续翻页
		out := make([]int, 0, want)
		for i := 1; i <= want; i++ {
			out = append(out, i)
		}
		return out
	}
	maxPg := m.PageCount
	if want > maxPg {
		want = maxPg
	}
	out := make([]int, 0, want)
	seen := map[int]bool{}
	switch strategy {
	case "seq":
		for i := 1; i <= want; i++ {
			out = append(out, i)
		}
	case "random":
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		for len(out) < want {
			pg := 1 + r.Intn(maxPg)
			if !seen[pg] {
				seen[pg] = true
				out = append(out, pg)
			}
		}
		sort.Ints(out)
	default: // stride
		if want == 1 {
			return []int{1}
		}
		step := float64(maxPg-1) / float64(want-1)
		for i := 0; i < want; i++ {
			pg := 1 + int(math.Round(float64(i)*step))
			if pg < 1 {
				pg = 1
			}
			if pg > maxPg {
				pg = maxPg
			}
			if !seen[pg] {
				seen[pg] = true
				out = append(out, pg)
			}
		}
		// 均匀点去重后可能不足（pagecount 较小时），按序补齐
		for pg := 1; len(out) < want && pg <= maxPg; pg++ {
			if !seen[pg] {
				seen[pg] = true
				out = append(out, pg)
			}
		}
		sort.Ints(out)
	}
	return out
}

// buildGroups 把域名按骨架归并成族，给出精确泛化正则建议。
// 只有编号位不同的一族（如 cdn.yzzy30-play.com / cdn.yzzy31-play.com）最值得泛化：
// 靠翻页永远抽不全编号，靠模式匹配能整族覆盖。
func buildGroups(list []*discoverHost) []*hostGroup {
	fam := map[string][]*discoverHost{}
	for _, h := range list {
		if h.Host == "" {
			continue
		}
		k := core.HostSkeleton(h.Host)
		fam[k] = append(fam[k], h)
	}
	out := make([]*hostGroup, 0, len(fam))
	for _, hs := range fam {
		sort.Slice(hs, func(i, j int) bool { return hs[i].Count > hs[j].Count })
		members := make([]string, 0, len(hs))
		hits := 0
		for _, h := range hs {
			members = append(members, h.Host)
			hits += h.Count + h.Hits
		}
		pat := core.HostFamilyRegex(members)
		if pat == "" {
			continue
		}
		out = append(out, &hostGroup{
			Pattern:  pat,
			Label:    strings.ReplaceAll(pat, `\d+`, "*"),
			Members:  members,
			Hits:     hits,
			Variants: len(members),
			Loose:    core.GenericRegex(members[0]),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ai, aj := out[i].Variants > 1, out[j].Variants > 1
		if ai != aj {
			return ai // 有多编号取值的族优先
		}
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// handleDiscoverHistory 域名历史库：GET 查看概况，DELETE 清空（可按 api_url 指定某个采集接口）
func (s *Server) handleDiscoverHistory(w http.ResponseWriter, r *http.Request) {
	if s.disc == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "历史库未启用"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 200, "data": s.disc.Stats()})
	case http.MethodDelete:
		api := strings.TrimSpace(r.URL.Query().Get("api_url"))
		n := s.disc.Clear(api)
		if api == "" {
			api = "全部采集接口"
		}
		s.logger.Write("INFO", "discover", "清空域名历史 "+api+" -> "+itoa(n)+" 个")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"code": 200, "msg": "已清空 " + itoa(n) + " 个历史域名", "removed": n,
		})
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"code": 403, "msg": "不支持的方法"})
	}
}
