package core

import (
	"fmt"
	"strings"
	"time"
)

// 版本号
const Version = "1.3.2"

// Settings 全局运行配置（对应 PHP rules.php 中的各全局量）
type Settings struct {
	CacheTTL          int      `json:"cache_ttl"`            // 缓存有效期（秒），默认 604800
	MatchTolerance    float64  `json:"match_tolerance"`      // 时长匹配容差（秒），默认 0.05
	UseHashCheck      bool     `json:"use_hash_check"`       // 是否启用内容 SHA256 校验
	AdsHashSampleSize int      `json:"ads_hash_sample_size"` // 采样字节数，默认 262144
	FetchTimeout      int      `json:"fetch_timeout"`        // 抓取超时（秒），默认 30
	UserAgent         string   `json:"user_agent"`           // 请求 UA
	CacheScheme       string   `json:"cache_scheme"`         // 缓存访问协议：auto=跟随本次访问协议（HTTPS 访问出 HTTPS，HTTP 访问出 HTTP，默认）/ https=强制 https / http=强制 http
	CORSAllowOrigin   string   `json:"cors_allow_origin"`    // 跨域允许来源：* 或逗号分隔域名列表（留空按 *）
	CacheAutoClean    bool     `json:"cache_auto_clean"`     // 是否开启定时自动清理过期缓存
	CacheCleanEveryH  int      `json:"cache_clean_every_h"`  // 定时清理间隔（小时），默认 24
	CacheCleanUnit    string   `json:"cache_clean_unit"`     // 定时清理间隔单位：s=秒 / m=分 / h=小时，默认 h
	CacheCleanTypes   []string `json:"cache_clean_types"`    // 定时清理的缓存类型：expired / all / site / root / preview / seghash（默认 expired）
	CacheCleanSites   []string `json:"cache_clean_sites"`    // 指定站点缓存类型生效的站点目录（cache_clean_types 含 site 时使用）
	CacheCleanDays    int      `json:"cache_clean_days"`     // 定时清理的 N 天前阈值：>0 时对缓存类按 N 天前清理，0 表示按缓存有效期
	AdminPath         string   `json:"admin_path"`           // 后台入口路径（默认 /admin），只有该前缀下才能进后台，改后即时生效

	// ---- 代理池（proxy.scdn.io get_proxy API）----
	ProxyPoolEnable   bool   `json:"proxy_pool_enable"`    // 是否启用代理池（站点级 proxy_url 为空时才生效）
	ProxyPoolAPI      string `json:"proxy_pool_api"`       // 代理池拉取接口，默认 https://proxy.scdn.io/api/get_proxy.php
	ProxyPoolProtocol string `json:"proxy_pool_protocol"`  // 拉取协议参数：http / https / socks5
	ProxyPoolCount    int    `json:"proxy_pool_count"`     // 单次/目标池大小，默认 3
	ProxyPoolCountry  string `json:"proxy_pool_country"`   // 国家代码，如 CN，留空不限
	ProxyPoolRefreshM int    `json:"proxy_pool_refresh_m"` // 自动刷新间隔（分钟），默认 30
	ProxyPoolStrategy string `json:"proxy_pool_strategy"`  // 取用策略：round（轮询，默认）/ random
	ProxyPoolFailMax  int    `json:"proxy_pool_fail_max"`  // 连续失败多少次后剔除，默认 3
	ProxyPoolManual   string `json:"proxy_pool_manual"`    // 手动代理列表（每行一个，支持 ip:port / scheme://user:pass@ip:port），优先于接口拉取

	// ---- IP 授权（纯白名单）----
	IPAccessEnable bool `json:"ip_access_enable"` // 是否启用 IP 白名单（关闭时全部放行，开启后仅白名单 IP 可访问）
}

// CleanPath 对外解析接口的自定义地址（默认 /api/clean，可自定义多个路径并同时生效）
type CleanPath struct {
	ID      string `json:"id"`
	Name    string `json:"name"`           // 备注名，如 影视站接口
	Path    string `json:"path"`           // 接口路径，如 /api/clean 或 /jx（自动补前导斜杠）
	Enabled bool   `json:"enabled"`        // 是否生效
	Note    string `json:"note,omitempty"` // 备注
}

// Site 站点规则（持久化到 data/rules.json）
type Site struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`                   // 站点名（如 电影天堂(vip.dytt*)）
	ParseType    string      `json:"parse_type"`             // duration / hash / both / direct（direct=直链播放：无广告时直接输出源站地址）
	URLPrefix    string      `json:"url_prefix"`             // URL 匹配前缀（host 前缀，兼容旧数据，单值）
	URLRegex     string      `json:"url_regex"`              // URL 匹配正则（可选，兼容旧数据，单值）
	URLPrefixes  []string    `json:"url_prefixes,omitempty"` // URL 匹配前缀（可多个，任一命中即匹配）
	URLRegexes   []string    `json:"url_regexes,omitempty"`  // URL 匹配正则（可多个，任一命中即匹配）
	CacheSubdir  string      `json:"cache_subdir"`           // 缓存标识子目录
	Fingerprint  []float64   `json:"fingerprint"`            // 特征码：广告时长指纹数组
	Fingerprints [][]float64 `json:"fingerprints"`           // 多组特征码：任一命中即剔除（应对同一站点多种广告形态）
	// 保护指纹（正片指纹）：命中该时长序列的片段禁止剔除，用于修正「正片被误判为广告」
	// 由测试页规则学习对「误判（keep）」标记自动生成并写回，任何检测方式下都优先于剔除生效
	KeepFingerprint  []float64   `json:"keep_fingerprint,omitempty"`
	KeepFingerprints [][]float64 `json:"keep_fingerprints,omitempty"`
	AdsHashes        []HashGroup `json:"ads_hashes,omitempty"` // 广告内容哈希组（可选）
	FilterRegex      []string    `json:"filter_regex"`         // 广告过滤正则列表
	IndexMode        string      `json:"index_mode"`           // 索引模式：auto(默认，跟随主索引) / single(单级列表) / fixed(固定子路径)
	DetectMode       string      `json:"detect_mode"`          // 广告检测方式：sequence(默认，TS 序列连贯性) / fingerprint(时长指纹) / hash(内容哈希)
	StreamPick       string      `json:"stream_pick"`          // 码率选择：first(默认) / highest / lowest
	FixedSub         string      `json:"fixed_sub,omitempty"`  // 固定子路径（index_mode=fixed 时生效）
	ProxyURL         string      `json:"proxy_url,omitempty"`  // 站点级出网代理（覆盖全局；填 - 或 direct 表示强制直连）
	Enabled          bool        `json:"enabled"`              // 启用/禁用
	Sort             int         `json:"sort"`                 // 排序码
	Remark           string      `json:"remark"`               // 备注
}

// PrefixList 合并新旧字段，返回全部 URL 匹配前缀（去重、去空）
func (s *Site) PrefixList() []string {
	out := make([]string, 0, len(s.URLPrefixes)+1)
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, v := range s.URLPrefixes {
		add(v)
	}
	add(s.URLPrefix)
	return out
}

// RegexList 合并新旧字段，返回全部 URL 匹配正则（去重、去空）
func (s *Site) RegexList() []string {
	out := make([]string, 0, len(s.URLRegexes)+1)
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, v := range s.URLRegexes {
		add(v)
	}
	add(s.URLRegex)
	return out
}

// AdsFPList 返回站点全部广告时长指纹（单组兼容字段 + 多组字段，去重空组）
func (s *Site) AdsFPList() [][]float64 {
	out := make([][]float64, 0, len(s.Fingerprints)+1)
	if len(s.Fingerprint) > 0 {
		out = append(out, s.Fingerprint)
	}
	for _, fp := range s.Fingerprints {
		if len(fp) > 0 {
			out = append(out, fp)
		}
	}
	return out
}

// KeepFPList 返回站点全部保护（正片）时长指纹
func (s *Site) KeepFPList() [][]float64 {
	out := make([][]float64, 0, len(s.KeepFingerprints)+1)
	if len(s.KeepFingerprint) > 0 {
		out = append(out, s.KeepFingerprint)
	}
	for _, fp := range s.KeepFingerprints {
		if len(fp) > 0 {
			out = append(out, fp)
		}
	}
	return out
}

// IsDirect 是否为「直链播放」站点（parse_type=direct）
//
// 直链语义：照常解析并检测广告，但没有插播广告（剔除 0 段）时直接输出源站 m3u8 地址，
// 不生成/不落本地缓存，播放器直连源站，省去中间转发与缓存开销；检测到广告时仍走净化流程。
func (s *Site) IsDirect() bool {
	return strings.EqualFold(strings.TrimSpace(s.ParseType), "direct")
}

// EffDetectMode 有效检测方式：直链站点未配置时回落 auto（序列主检测 + 指纹/哈希补充），
// 其余站点保持原值（空值沿用既有指纹+哈希主流程，不改变历史行为）
func (s *Site) EffDetectMode() string {
	m := strings.ToLower(strings.TrimSpace(s.DetectMode))
	if m == "" && s.IsDirect() {
		return "auto"
	}
	return m
}

// EffIndexMode 有效索引模式：空值回落 auto（跟随主索引，与 ResolvePlaylistOpt 默认一致）
func (s *Site) EffIndexMode() string {
	m := strings.ToLower(strings.TrimSpace(s.IndexMode))
	if m == "" {
		return "auto"
	}
	return m
}

// HashSeg 单个片段的采样哈希
type HashSeg struct {
	Sample string `json:"sample"`
	Size   int    `json:"size"`
}

// HashGroup 一组广告片段的哈希序列（alias，兼容 []HashSeg）
type HashGroup = []HashSeg

// Fingerprint ads_lib 中的指纹组（learn 收录产物对应 PHP ads_lib.php）
type Fingerprint struct {
	Site       string    `json:"site"`
	SampleSize int       `json:"sample_size"`
	Segs       []HashSeg `json:"segs"`
}

// AdsLib 全局广告内容哈希库
type AdsLib struct {
	SampleSize   int           `json:"sample_size"`
	Fingerprints []Fingerprint `json:"fingerprints"`
}

// Segment 解析出的一个视频片段
type Segment struct {
	Idx      int     // 片段序号（1-based）
	Dur      float64 // 时长（秒）
	Name     string  // 片段地址（相对或绝对）
	Disc     bool    // 该片段前是否有 DISCONTINUITY 标记
	DiscPair bool    // 该片段前是否有连续两个及以上 DISCONTINUITY 标记（DISC-DISC 广告入口特征）
}

// SegmentView 片段明细（测试页定位与播放预览使用）
type SegmentView struct {
	Index int     `json:"index"` // 段号（1-based，与原始 m3u8 中的顺序一致）
	Dur   float64 `json:"dur"`   // 时长（秒）
	URI   string  `json:"uri"`   // 原始写法（可能是相对路径）
	URL   string  `json:"url"`   // 补全后的绝对地址
	Ad    bool    `json:"ad"`    // 是否命中广告区间
	Start float64 `json:"start"` // 在原始列表中的起始时间（秒）
}

// IPRule IP 授权规则：单 IP 或 CIDR 网段
type IPRule struct {
	ID      string `json:"id"`      // 规则 ID
	IP      string `json:"ip"`      // 单 IP（如 1.2.3.4）或 CIDR（如 1.2.3.0/24）
	Mode    string `json:"mode"`    // allow=放行 / deny=拒绝
	Note    string `json:"note"`    // 备注
	Enabled bool   `json:"enabled"` // 是否启用（未启用的规则不参与匹配）
}

// CustomAPI 自定义接口：外部接口地址与参数配置
type CustomAPI struct {
	ID      string `json:"id"`                // 接口 ID
	Name    string `json:"name"`              // 名称
	URL     string `json:"url"`               // 接口地址
	Method  string `json:"method"`            // 请求方法：GET / POST
	Params  string `json:"params"`            // 参数（query 串或 JSON，如 a=1&b=2）
	Headers string `json:"headers,omitempty"` // 自定义请求头（每行一个 Name: Value）
	Note    string `json:"note"`              // 备注
	Enabled bool   `json:"enabled"`           // 是否启用
}

// Res 统一 JSON 响应（与 PHP 版字段完全一致）
// 说明：除 code/msg 外全部 omitempty —— 失败（code=404）响应只会输出 code 与 msg 两个字段
type Res struct {
	Code   int         `json:"code"`
	Msg    string      `json:"msg"`
	Origin string      `json:"origin,omitempty"`
	Link   string      `json:"link,omitempty"`
	URL    string      `json:"url,omitempty"`
	Time   string      `json:"Time,omitempty"`
	Data   interface{} `json:"data,omitempty"`
}

// WeekdayCN 中文星期
var WeekdayCN = [7]string{"日", "一", "二", "三", "四", "五", "六"}

// TimeTok 生成 PHP 风格时间文本后缀：2006-01-02 15:04:05 星期X
func TimeTok(now time.Time) string {
	return fmt.Sprintf("%s 星期%s", now.Format("2006-01-02 15:04:05"), WeekdayCN[int(now.Weekday())])
}
