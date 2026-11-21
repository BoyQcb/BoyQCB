package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Record 一条解析记录
type Record struct {
	Time     string `json:"time"`
	URL      string `json:"url"`
	Site     string `json:"site"` // 站点名
	Origin   string `json:"origin"`
	Status   int    `json:"status"`
	Code     int    `json:"code"`
	Msg      string `json:"msg"`
	Elapsed  int64  `json:"elapsed_ms"`
	Cached   bool   `json:"from_cache"`
	CacheURL string `json:"cache_url"`
	Proxy    string `json:"proxy"` // 本次出网实际使用的代理（站点级/代理池），直连或命中缓存时为空
}

// Records 解析记录簿（内存 + data/records.json 持久化，保留最近 N 条）
type Records struct {
	mu    sync.Mutex
	path  string
	max   int
	items []Record
}

// NewRecords 构造记录簿
func NewRecords(path string, max int) *Records {
	r := &Records{path: path, max: max}
	r.load()
	return r
}

func (r *Records) load() {
	data, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &r.items)
}

// Add 追加一条记录并持久化（超出上限裁尾）
func (r *Records) Add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, rec)
	if len(r.items) > r.max {
		r.items = r.items[len(r.items)-r.max:]
	}
	r.save()
}

func (r *Records) save() {
	if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
		return
	}
	if data, err := json.MarshalIndent(r.items, "", "  "); err == nil {
		tmp := r.path + ".tmp"
		_ = os.WriteFile(tmp, data, 0644)
		_ = os.Rename(tmp, r.path)
	}
}

// All 返回全部记录（拷贝）
func (r *Records) All() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, len(r.items))
	copy(out, r.items)
	return out
}

// Recent 返回最近 N 条（倒序）
func (r *Records) Recent(n int) []Record {
	all := r.All()
	if n <= 0 || n > len(all) {
		n = len(all)
	}
	out := make([]Record, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, all[i])
	}
	return out
}

// Count 记录总数
func (r *Records) Count() int {
	return len(r.All())
}

// Clear 清空全部记录并持久化
func (r *Records) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = nil
	r.save()
}
