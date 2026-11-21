// 片段标记库：data/marks.json
//
// 测试页里用户手动标记的片段（漏判广告 / 误判正片 / 疑似），
// 是规则学习的原始素材：标记 → 归并成区间 → 生成时长指纹或内容哈希 → 写回站点规则。
// 按「站点 + m3u8 地址」聚合，同一条影片反复解析只保留一份最新标记。

package store

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// 标记类型
const (
	MarkAd      = "ad"      // 用户判为广告（系统未判出 = 漏判）
	MarkKeep    = "keep"    // 用户判为正片（系统判成广告 = 误判）
	MarkSuspect = "suspect" // 疑似，待确认
)

// MarkSeg 被标记的一个片段
type MarkSeg struct {
	Index int     `json:"index"`
	Dur   float64 `json:"dur"`
	URL   string  `json:"url"`
	Kind  string  `json:"kind"`
	WasAd bool    `json:"was_ad"` // 系统原本的判定
	Note  string  `json:"note,omitempty"`
}

// MarkRecord 一条影片的标记记录
type MarkRecord struct {
	Key       string    `json:"key"`
	SiteID    string    `json:"site_id"`
	SiteName  string    `json:"site_name"`
	URL       string    `json:"url"`
	SubURL    string    `json:"sub_url,omitempty"`
	TotalSegs int       `json:"total_segs"`
	Hits      [][2]int  `json:"hits,omitempty"` // 系统判定的广告区间
	Segs      []MarkSeg `json:"segs"`
	Note      string    `json:"note,omitempty"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

type marksFile struct {
	Records map[string]*MarkRecord `json:"records"`
}

// Marks 标记库
type Marks struct {
	mu   sync.Mutex
	path string
	data *marksFile
}

// NewMarks 载入标记库
func NewMarks(path string) *Marks {
	m := &Marks{
		path: path,
		data: &marksFile{Records: map[string]*MarkRecord{}},
	}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		var f marksFile
		if json.Unmarshal(b, &f) == nil && f.Records != nil {
			m.data = &f
		}
	}
	return m
}

// MarkKey 记录主键：站点 + 地址
func MarkKey(siteID, url string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(siteID+"|"+url)))
}

// Save 写入 / 覆盖一条标记记录（按 Key upsert）
func (m *Marks) Save(rec *MarkRecord) {
	if rec == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().Format("2006-01-02 15:04:05")
	rec.UpdatedAt = now
	rec.Key = MarkKey(rec.SiteID, rec.URL)
	if old, ok := m.data.Records[rec.Key]; ok && old != nil {
		rec.CreatedAt = old.CreatedAt
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = now
	}
	sort.Slice(rec.Segs, func(i, j int) bool { return rec.Segs[i].Index < rec.Segs[j].Index })
	m.data.Records[rec.Key] = rec
	m.saveLocked()
}

// Get 取一条记录
func (m *Marks) Get(key string) *MarkRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.data.Records[key]
	if !ok || r == nil {
		return nil
	}
	cp := *r
	cp.Segs = append([]MarkSeg(nil), r.Segs...)
	return &cp
}

// List 列出记录，siteID 为空表示全部；按更新时间倒序
func (m *Marks) List(siteID string) []*MarkRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*MarkRecord{}
	for _, r := range m.data.Records {
		if r == nil {
			continue
		}
		if siteID != "" && r.SiteID != siteID {
			continue
		}
		cp := *r
		cp.Segs = append([]MarkSeg(nil), r.Segs...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

// Delete 删除一条记录
func (m *Marks) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data.Records, key)
	m.saveLocked()
}

func (m *Marks) saveLocked() {
	b, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.path), 0755)
	_ = os.WriteFile(m.path, b, 0644)
}
