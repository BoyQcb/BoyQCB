package store

// 域名发现的历史累积库：data/discovered.json
//
// 采集接口每页条数被站方锁死（常见 20 条/页），一次扫描只能覆盖全库极小一部分，
// 单次结果天然不全。这里把每次扫到的域名按采集接口累积下来，
// 多次扫描 = 一次全量，扫过的域名不会因为下次没扫到就消失。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DiscHost 单个域名的历史累积信息
type DiscHost struct {
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	Hits      int    `json:"hits"`
	Sample    string `json:"sample"`
	Kind      string `json:"kind"`
	Vod       string `json:"vod"`
}

// DiscSource 一个采集接口（资源站）的历史
type DiscSource struct {
	LastScan string               `json:"last_scan"`
	Scans    int                  `json:"scans"`
	Hosts    map[string]*DiscHost `json:"hosts"`
}

type discoveredFile struct {
	Sources map[string]*DiscSource `json:"sources"`
}

// HostStat 一次扫描中某域名的统计（供 Merge 入参）
type HostStat struct {
	Host   string
	Sample string
	Kind   string
	Vod    string
	Count  int
}

// Discovered 域名历史库
type Discovered struct {
	mu   sync.Mutex
	path string
	data *discoveredFile
}

// NewDiscovered 载入历史库，文件不存在或损坏时以空库启动
func NewDiscovered(path string) *Discovered {
	d := &Discovered{
		path: path,
		data: &discoveredFile{Sources: map[string]*DiscSource{}},
	}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		var f discoveredFile
		if json.Unmarshal(b, &f) == nil && f.Sources != nil {
			d.data = &f
		}
	}
	return d
}

// Merge 合并一次扫描结果并落盘，返回（该源最新历史快照 map，本次新增域名数）
func (d *Discovered) Merge(apiURL string, hosts []HostStat) (map[string]DiscHost, int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	src, ok := d.data.Sources[apiURL]
	if !ok || src.Hosts == nil {
		src = &DiscSource{Hosts: map[string]*DiscHost{}}
		d.data.Sources[apiURL] = src
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	added := 0
	for _, h := range hosts {
		if h.Host == "" {
			continue
		}
		old, exist := src.Hosts[h.Host]
		if !exist {
			added++
			src.Hosts[h.Host] = &DiscHost{
				FirstSeen: now,
				LastSeen:  now,
				Hits:      h.Count,
				Sample:    h.Sample,
				Kind:      h.Kind,
				Vod:       h.Vod,
			}
			continue
		}
		old.LastSeen = now
		old.Hits += h.Count
		if old.Sample == "" {
			old.Sample = h.Sample
		}
		if old.Kind == "" || (h.Kind == "m3u8" && old.Kind != "m3u8") {
			old.Kind = h.Kind
		}
		if h.Kind == "m3u8" || old.Vod == "" {
			old.Vod = h.Vod
		}
	}
	src.LastScan = now
	src.Scans++
	_ = d.save()

	out := make(map[string]DiscHost, len(src.Hosts))
	for k, v := range src.Hosts {
		out[k] = *v
	}
	return out, added
}

// Snapshot 返回某采集接口的历史快照（值拷贝）
func (d *Discovered) Snapshot(apiURL string) map[string]DiscHost {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]DiscHost{}
	src, ok := d.data.Sources[apiURL]
	if !ok {
		return out
	}
	for k, v := range src.Hosts {
		out[k] = *v
	}
	return out
}

// SourceStat 单个采集接口的历史概况
type SourceStat struct {
	APIURL   string `json:"api_url"`
	Hosts    int    `json:"hosts"`
	Scans    int    `json:"scans"`
	LastScan string `json:"last_scan"`
}

// Stats 返回全部采集接口的历史概况（按域名数降序）
func (d *Discovered) Stats() []SourceStat {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]SourceStat, 0, len(d.data.Sources))
	for api, s := range d.data.Sources {
		out = append(out, SourceStat{APIURL: api, Hosts: len(s.Hosts), Scans: s.Scans, LastScan: s.LastScan})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hosts > out[j].Hosts })
	return out
}

// Clear 清空某采集接口的历史；apiURL 为空表示清空全部。返回被清除的域名数
func (d *Discovered) Clear(apiURL string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	if apiURL == "" {
		for _, s := range d.data.Sources {
			n += len(s.Hosts)
		}
		d.data.Sources = map[string]*DiscSource{}
	} else if s, ok := d.data.Sources[apiURL]; ok {
		n = len(s.Hosts)
		delete(d.data.Sources, apiURL)
	}
	_ = d.save()
	return n
}

func (d *Discovered) save() error {
	if d.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp%d", d.path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, d.path); err != nil {
		// 重命名失败时退化为直接写，避免丢数据
		return os.WriteFile(d.path, data, 0644)
	}
	return nil
}
