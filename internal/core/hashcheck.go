package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// seg-hash cache: url+size -> sampled hash, persisted to disk
var segHashCache = &hashCache{m: make(map[string]string, 1024)}

const segHashCacheMax = 50000

type hashCache struct {
	mu   sync.RWMutex
	m    map[string]string
	path string
	put  int
}

func (c *hashCache) get(k string) (string, bool) {
	c.mu.RLock()
	v, ok := c.m[k]
	c.mu.RUnlock()
	return v, ok
}

func (c *hashCache) set(k, v string) {
	c.mu.Lock()
	if len(c.m) >= segHashCacheMax {
		c.m = make(map[string]string, 1024)
	}
	c.m[k] = v
	c.put++
	needSave := c.path != "" && c.put >= 5
	c.mu.Unlock()
	if needSave {
		c.save()
	}
}

func (c *hashCache) save() {
	c.mu.RLock()
	if c.path == "" || len(c.m) == 0 {
		c.mu.RUnlock()
		return
	}
	snapshot := make(map[string]string, len(c.m))
	for k, v := range c.m {
		snapshot[k] = v
	}
	c.mu.RUnlock()

	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err = os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, c.path)
	c.mu.Lock()
	c.put = 0
	c.mu.Unlock()
}

func InitSegHashCache(dataDir string) {
	if dataDir == "" {
		return
	}
	p := filepath.Join(dataDir, "seghash.json")
	segHashCache.mu.Lock()
	segHashCache.path = p
	segHashCache.mu.Unlock()

	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		return
	}
	var loaded map[string]string
	if err = json.Unmarshal(b, &loaded); err != nil {
		return
	}
	segHashCache.mu.Lock()
	for k, v := range loaded {
		if len(segHashCache.m) < segHashCacheMax {
			segHashCache.m[k] = v
		}
	}
	segHashCache.mu.Unlock()

	// background periodic flush so a hard kill does not lose recent entries
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for range t.C {
			segHashCache.save()
		}
	}()
}

// ClearSegHashCache 清空片段采样哈希缓存（删除 data/seghash.json 并重置内存表），返回清理条目数
func ClearSegHashCache() int {
	segHashCache.mu.Lock()
	n := len(segHashCache.m)
	path := segHashCache.path
	segHashCache.m = make(map[string]string, 1024)
	segHashCache.put = 0
	segHashCache.mu.Unlock()
	if path != "" {
		_ = os.Remove(path)
		_ = os.Remove(path + ".tmp")
	}
	return n
}

func FlushSegHashCache() {
	segHashCache.save()
}

func FetchSample(client *http.Client, url, ua string, size int, timeout time.Duration) (string, error) {
	body, err := FetchRange(client, url, ua, size, timeout)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])[:16], nil
}

func fetchSampleCached(client *http.Client, url, ua string, size int, timeout time.Duration) (string, error) {
	key := strconv.Itoa(size) + "|" + url
	if v, ok := segHashCache.get(key); ok {
		return v, nil
	}
	h, err := FetchSample(client, url, ua, size, timeout)
	if err == nil && h != "" {
		segHashCache.set(key, h)
	}
	return h, err
}

func HashGroupsForSite(site *Site, lib *AdsLib) (int, [][]HashSeg) {
	sampleSize := 262144
	if lib != nil && lib.SampleSize > 0 {
		sampleSize = lib.SampleSize
	}
	groups := [][]HashSeg{}
	if site != nil && len(site.AdsHashes) > 0 {
		for _, hg := range site.AdsHashes {
			groups = append(groups, []HashSeg(hg))
		}
	}
	if lib != nil {
		for _, g := range lib.Fingerprints {
			if len(g.Segs) > 0 {
				groups = append(groups, g.Segs)
				if g.SampleSize > 0 {
					sampleSize = g.SampleSize
				}
			}
		}
	}
	return sampleSize, groups
}

func VerifyByHash(client *http.Client, segs []Segment, startIdx, endIdx int, baseURL, ua string, sampleSize int, groups [][]HashSeg, timeout time.Duration) (bool, int) {
	span := endIdx - startIdx + 1
	if span <= 0 || startIdx < 1 || endIdx > len(segs) {
		return false, -1
	}

	matched := []int{}
	for gi, g := range groups {
		if len(g) == span {
			matched = append(matched, gi)
		}
	}
	if len(matched) == 0 {
		return false, -1
	}

	urls := make([]string, span)
	for i := startIdx; i <= endIdx; i++ {
		full := segs[i-1].Name
		if !httpRe.MatchString(full) {
			// baseURL 是目录（Playlist.Base），按目录语义拼接
			full = AbsFromDir(baseURL, full)
		}
		urls[i-startIdx] = full
	}
	hashes := make([]string, span)

	// stage 1: first segment only - cheap early rejection
	first, err := fetchSampleCached(client, urls[0], ua, sampleSize, timeout)
	if err != nil {
		return false, -1
	}
	hashes[0] = first
	anyFirst := false
	for _, gi := range matched {
		if groups[gi][0].Sample != "" && first == groups[gi][0].Sample {
			anyFirst = true
			break
		}
	}
	if !anyFirst {
		return false, -1
	}

	// stage 2: remaining segments in parallel
	if span > 1 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for k := 1; k < span; k++ {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if hs, e := fetchSampleCached(client, urls[k], ua, sampleSize, timeout); e == nil {
					hashes[k] = hs
				}
			}(k)
		}
		wg.Wait()
	}

	for _, gi := range matched {
		group := groups[gi]
		ok := true
		for k := 0; k < span; k++ {
			if hashes[k] == "" || group[k].Sample == "" || hashes[k] != group[k].Sample {
				ok = false
				break
			}
		}
		if ok {
			return true, gi
		}
	}
	return false, -1
}

func verifyCandidates(client *http.Client, segs []Segment, cands [][2]int, baseURL, ua string,
	sampleSize int, groups [][]HashSeg, timeout time.Duration) []bool {
	out := make([]bool, len(cands))
	if len(cands) == 0 {
		return out
	}
	workers := 16
	if len(cands) < workers {
		workers = len(cands)
	}
	ch := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ci := range ch {
				c := cands[ci]
				ok, _ := VerifyByHash(client, segs, c[0], c[1], baseURL, ua, sampleSize, groups, timeout)
				out[ci] = ok
			}
		}()
	}
	for i := range cands {
		ch <- i
	}
	close(ch)
	wg.Wait()
	return out
}

func CandidateByStructure(segs []Segment, groups [][]HashSeg, maxCand int) [][2]int {
	if len(groups) == 0 || len(segs) == 0 || maxCand <= 0 {
		return nil
	}
	total := len(segs)
	durs := make([]float64, total)
	for i, s := range segs {
		durs[i] = s.Dur
	}
	sort.Float64s(durs)
	median := durs[total/2]
	shortTh := math.Min(2.0, median*0.6)

	cands := [][2]int{}
	seen := make(map[[2]int]bool, maxCand)
	for _, g := range groups {
		l := len(g)
		if l < 2 {
			continue
		}
		for i := l - 1; i < total; i++ {
			if segs[i].Dur < shortTh {
				start := i - l + 2 // idx (1-based)
				end := i + 1       // idx (1-based)
				if start < 1 {
					continue
				}
				c := [2]int{start, end}
				if seen[c] {
					continue
				}
				seen[c] = true
				cands = append(cands, c)
				if len(cands) >= maxCand {
					return cands
				}
			}
		}
	}
	return cands
}
