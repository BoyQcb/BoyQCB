package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dytt_parser_go/internal/core"
)

// Source 资源站条目（域名发现页按「资源站」维度管理，一条 = 一个采集接口）
type Source struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	APIURL string `json:"api_url"`
	Kind   string `json:"kind,omitempty"`    // xml / json，留空自动识别
	SiteID string `json:"site_id,omitempty"` // 关联站点：批量添加域名时默认追加到该站点
	Pages  int    `json:"pages,omitempty"`   // 默认扫描页数，0 表示取全局默认 3
	Preset bool   `json:"preset,omitempty"`  // 内置预置源
	Sort   int    `json:"sort"`
}

// PresetSources 内置预置采集源
var PresetSources = []Source{
	{ID: "dytt", Name: "电影天堂 dytt", APIURL: "https://caiji.dyttzyapi.com/api.php/provide/vod/at/xml/", Kind: "xml", SiteID: "dytt", Preset: true, Sort: 1},
	{ID: "xg", Name: "西瓜 xg", APIURL: "https://caiji.xgzyapi.com/api.php/provide/vod/at/xml/", Kind: "xml", SiteID: "xg", Preset: true, Sort: 2},
	{ID: "lz", Name: "量子 lz", APIURL: "https://cj.lziapi.com/api.php/provide/vod/from/lzm3u8/at/xml/", Kind: "xml", SiteID: "lz", Preset: true, Sort: 3},
	{ID: "bf", Name: "暴风 bf", APIURL: "https://bfzyapi.com/api.php/provide/vod/", Kind: "json", SiteID: "fengbao", Preset: true, Sort: 4},
	{ID: "mdzy", Name: "魔都 mdzy", APIURL: "https://www.mdzyapi.com/api.php/provide/vod/?ac=videolist", Kind: "xml", SiteID: "modujx", Pages: 3, Preset: true, Sort: 5},
	{ID: "wsyzy", Name: "无水印 wsyzy", APIURL: "https://api.wsyzy.net/api.php/provide/vod/from/wsym3u8/at/xml", Kind: "xml", SiteID: "wsyzym", Pages: 3, Preset: true, Sort: 6},
	{ID: "sdzy", Name: "闪电 sdzy", APIURL: "https://xsd.sdzyapi.com/api.php/provide/vod/from/sdm3u8/at/xml/", Kind: "xml", SiteID: "sdzy", Pages: 3, Preset: true, Sort: 7},
	{ID: "wujin", Name: "无尽 wujin", APIURL: "http://api.wujinwszy.net/api.php/provide/vod/?ac=videolist", Kind: "xml", SiteID: "wujin", Pages: 3, Preset: true, Sort: 8},
	{ID: "yyzy", Name: "优质 yyzy", APIURL: "https://api.yyzy-tv.vip/inc/api_mac10.php", Kind: "json", SiteID: "yyzy", Pages: 3, Preset: true, Sort: 9},
}

// PresetSourcesCopy 返回预置源的副本（避免调用方修改包级变量）
func PresetSourcesCopy() []Source {
	out := make([]Source, 0, len(PresetSources))
	for _, s := range PresetSources {
		out = append(out, s)
	}
	return out
}

// ensurePresetSources 保证配置中包含全部预置资源站：缺失则追加，已存在则刷新为预置值。返回是否发生变更
func ensurePresetSources(f *File) bool {
	idx := make(map[string]int, len(f.Sources))
	for i, s := range f.Sources {
		idx[s.ID] = i
	}
	changed := false
	for _, p := range PresetSourcesCopy() {
		if i, ok := idx[p.ID]; ok {
			if f.Sources[i] != p {
				f.Sources[i] = p
				changed = true
			}
			continue
		}
		f.Sources = append(f.Sources, p)
		idx[p.ID] = len(f.Sources) - 1
		changed = true
	}
	return changed
}

// upgradeSettings 补齐后加的定时清理字段：interval 为 0 视为老配置，一次性启用默认值
func upgradeSettings(f *File) bool {
	if f.Settings.CacheCleanEveryH > 0 {
		return false
	}
	f.Settings.CacheCleanEveryH = 24
	if f.Settings.CacheCleanUnit == "" {
		f.Settings.CacheCleanUnit = "h"
	}
	f.Settings.CacheAutoClean = true
	return true
}

// upgradeProxyPool 补齐后加的代理池字段：仅填充零值，不动用户已保存的设置
func upgradeProxyPool(f *File) bool {
	changed := false
	if strings.TrimSpace(f.Settings.ProxyPoolAPI) == "" {
		f.Settings.ProxyPoolAPI = core.DefaultProxyPoolAPI
		changed = true
	}
	if strings.TrimSpace(f.Settings.ProxyPoolProtocol) == "" {
		f.Settings.ProxyPoolProtocol = core.DefaultProxyPoolProtocol
		changed = true
	}
	if f.Settings.ProxyPoolCount <= 0 {
		f.Settings.ProxyPoolCount = core.DefaultProxyPoolCount
		changed = true
	}
	if f.Settings.ProxyPoolRefreshM <= 0 {
		f.Settings.ProxyPoolRefreshM = core.DefaultProxyPoolRefreshM
		changed = true
	}
	if strings.TrimSpace(f.Settings.ProxyPoolStrategy) == "" {
		f.Settings.ProxyPoolStrategy = core.DefaultProxyPoolStrategy
		changed = true
	}
	if f.Settings.ProxyPoolFailMax <= 0 {
		f.Settings.ProxyPoolFailMax = core.DefaultProxyPoolFailMax
		changed = true
	}
	return changed
}

// upgradeIPWhitelist IP 授权改为纯白名单：删除历史的黑名单规则（mode != allow），避免语义反转
func upgradeIPWhitelist(f *File) bool {
	if len(f.IPRules) == 0 {
		return false
	}
	kept := make([]core.IPRule, 0, len(f.IPRules))
	for _, r := range f.IPRules {
		if strings.EqualFold(strings.TrimSpace(r.Mode), "allow") {
			r.Mode = "allow"
			kept = append(kept, r)
		}
	}
	if len(kept) == len(f.IPRules) {
		return false
	}
	f.IPRules = kept
	return true
}

// upgradeCleanTypes 定时清理支持清理类型：老配置补默认类型（仅过期缓存）
func upgradeCleanTypes(f *File) bool {
	if len(f.Settings.CacheCleanTypes) > 0 {
		return false
	}
	f.Settings.CacheCleanTypes = []string{"expired"}
	return true
}

// DefaultCleanPathID 默认解析接口地址在列表中的固定 ID
const DefaultCleanPathID = "default"

// DefaultCleanPath 默认解析接口地址
const DefaultCleanPath = "/api/clean"

// DefaultCleanPathNote 默认解析接口的说明内容（老配置补写，用户可在后台自行修改）
const DefaultCleanPathNote = "系统内置解析接口：GET 传入 ?url= m3u8 地址即可解析；可改备注名与说明，可临时停用，但不可删除。"

// upgradeCleanPaths 把默认解析接口地址作为列表第一条纳入统一管理（老配置升级时补入，已存在则补齐说明）
func upgradeCleanPaths(f *File) bool {
	for i := range f.CleanPaths {
		if f.CleanPaths[i].ID == DefaultCleanPathID || strings.EqualFold(strings.TrimSpace(f.CleanPaths[i].Path), DefaultCleanPath) {
			changed := false
			if f.CleanPaths[i].ID == "" {
				f.CleanPaths[i].ID = DefaultCleanPathID
				changed = true
			}
			if strings.TrimSpace(f.CleanPaths[i].Name) == "" {
				f.CleanPaths[i].Name = "默认接口"
				changed = true
			}
			if strings.TrimSpace(f.CleanPaths[i].Note) == "" {
				f.CleanPaths[i].Note = DefaultCleanPathNote
				changed = true
			}
			return changed
		}
	}
	def := core.CleanPath{
		ID:      DefaultCleanPathID,
		Name:    "默认接口",
		Path:    DefaultCleanPath,
		Note:    DefaultCleanPathNote,
		Enabled: true,
	}
	f.CleanPaths = append([]core.CleanPath{def}, f.CleanPaths...)
	return true
}

// File 配置文件整体结构（持久化于 data/rules.json）
type File struct {
	Settings core.Settings `json:"settings"`
	Sites    []core.Site   `json:"sites"`
	Sources  []Source      `json:"sources,omitempty"`
	AdsLib   core.AdsLib   `json:"ads_lib"`
	// IP 授权规则（纯白名单：命中即放行）
	IPRules []core.IPRule `json:"ip_rules,omitempty"`
	// 自定义接口地址（对外解析接口路径，如 /api/clean，可自定义多个并同时生效）
	CleanPaths []core.CleanPath `json:"clean_paths,omitempty"`
	// 自定义接口配置（外部接口保存/调用测试，兼容保留）
	CustomAPIs []core.CustomAPI `json:"custom_apis,omitempty"`
	// 管理员密码哈希（sha256("dytt::"+pwd)）
	AdminHash string `json:"admin_password_hash"`
}

// HashPassword 计算密码哈希
func HashPassword(pwd string) string {
	sum := sha256.Sum256([]byte("dytt::" + pwd))
	return hex.EncodeToString(sum[:])
}

// VerifyPassword 校验密码
func VerifyPassword(pwd, hash string) bool {
	if hash == "" {
		return false
	}
	return HashPassword(pwd) == hash
}

// Default 生成默认配置（内置两站规则 + PHP ads_lib 指纹 + admin/admin123）
func Default() *File {
	return &File{
		Settings: core.Settings{
			CacheTTL:          604800,
			MatchTolerance:    0.05,
			UseHashCheck:      true,
			AdsHashSampleSize: 262144,
			FetchTimeout:      30,
			UserAgent:         "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
			CacheScheme:       "auto",
			CORSAllowOrigin:   "*",
			CacheAutoClean:    true,
			CacheCleanEveryH:  24,
			CacheCleanUnit:    "h",
			CacheCleanTypes:   []string{"expired"},
			AdminPath:         "/admin",

			// 代理池（默认关闭，可在系统设置页开启）
			ProxyPoolEnable:   false,
			ProxyPoolAPI:      core.DefaultProxyPoolAPI,
			ProxyPoolProtocol: core.DefaultProxyPoolProtocol,
			ProxyPoolCount:    core.DefaultProxyPoolCount,
			ProxyPoolCountry:  "CN",
			ProxyPoolRefreshM: core.DefaultProxyPoolRefreshM,
			ProxyPoolStrategy: core.DefaultProxyPoolStrategy,
			ProxyPoolFailMax:  core.DefaultProxyPoolFailMax,
			ProxyPoolManual:   "# 手动代理列表：每行一个，支持 ip:port / socks5://user:pass@ip:port\n",

			// IP 授权（纯白名单，默认关闭 = 全部放行）
			IPAccessEnable: false,
		},
		Sites: []core.Site{
			{
				ID:          "dytt",
				Name:        "电影天堂(vip.dytt*)",
				ParseType:   "both",
				URLPrefix:   "vip.dytt",
				CacheSubdir: "dytt",
				Fingerprint: []float64{5.567, 3.2, 5.367, 3.333, 1.6},
				Enabled:     true,
				Sort:        1,
				Remark:      "vip.dytt 系列站点",
			},
			{
				ID:          "xg",
				Name:        "西瓜(svip.xgplay*)",
				ParseType:   "both",
				URLPrefix:   "svip.xgplay",
				CacheSubdir: "xg",
				Fingerprint: []float64{4.6, 5.533, 0.233},
				Enabled:     true,
				Sort:        2,
				Remark:      "svip.xgplay 系列站点",
			},
		},
		Sources: PresetSourcesCopy(),
		AdsLib: core.AdsLib{
			SampleSize: 262144,
			Fingerprints: []core.Fingerprint{
				{
					Site:       "xgplay(西瓜)",
					SampleSize: 262144,
					Segs: []core.HashSeg{
						{Sample: "50e9bad9827b96eb", Size: 262144},
						{Sample: "8cf9e507cbe97d1e", Size: 262144},
						{Sample: "9ff503296875ed14", Size: 262144},
					},
				},
				{
					Site:       "dytt(电影天堂)",
					SampleSize: 262144,
					Segs: []core.HashSeg{
						{Sample: "a9b74edc95cd0a35", Size: 262144},
						{Sample: "ba4f4e515321a98b", Size: 262144},
						{Sample: "97d035fc128c3ffb", Size: 262144},
						{Sample: "66bcc5d61771c98d", Size: 262144},
						{Sample: "850b5bdea0503d47", Size: 262144},
					},
				},
			},
		},
		// 解析接口地址列表：默认地址 /api/clean 作为第一条纳入统一管理
		CleanPaths: []core.CleanPath{
			{ID: DefaultCleanPathID, Name: "默认接口", Path: DefaultCleanPath, Note: DefaultCleanPathNote, Enabled: true},
		},
		AdminHash: HashPassword("admin123"),
	}
}

// Manager 配置读写（内存 + data/rules.json 原子持久化）
type Manager struct {
	mu   sync.RWMutex
	C    *File
	path string
}

// New 创建 Manager，不存在配置时写入默认值
func New(path string) (*Manager, error) {
	m := &Manager{path: path}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		var f File
		if jerr := json.Unmarshal(data, &f); jerr == nil {
			m.C = &f
			// 老配置升级：补齐/刷新预置资源站列表（按 ID 幂等）
			changed := ensurePresetSources(m.C)
			// 老配置升级：补定时清理默认值（此前无此字段，一律视为首次启用）
			if upgradeSettings(m.C) {
				changed = true
			}
			// 升级：补齐代理池 / IP 授权 / 自定义接口等新字段（仅填空，覆盖不了已有值）
			if upgradeProxyPool(m.C) {
				changed = true
			}
			if upgradeIPWhitelist(m.C) {
				changed = true
			}
			if upgradeCleanTypes(m.C) {
				changed = true
			}
			if upgradeCleanPaths(m.C) {
				changed = true
			}
			if changed {
				_ = m.save()
			}
			return m, nil
		}
	}
	m.C = Default()
	if err := m.save(); err != nil {
		return nil, err
	}
	return m, nil
}

// Snapshot 返回当前配置拷贝（读锁）
func (m *Manager) Snapshot() File {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 深拷贝
	b, _ := json.Marshal(m.C)
	var f File
	_ = json.Unmarshal(b, &f)
	return f
}

// Update 在写锁下修改并持久化
func (m *Manager) Update(fn func(*File) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := fn(m.C); err != nil {
		return err
	}
	return m.save()
}

func (m *Manager) save() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.C, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp%d", m.path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
