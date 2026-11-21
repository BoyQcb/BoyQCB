package core

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cache 处理后的 m3u8 落盘缓存（目录：cache/<subdir>/<hash>.m3u8）
type Cache struct {
	Dir string        // 缓存根目录
	TTL time.Duration // 有效时长
}

// CacheKey 由原始链接得到缓存文件名 hash.m3u8
func CacheKey(link string) string {
	sum := md5.Sum([]byte(link))
	return hex.EncodeToString(sum[:]) + ".m3u8"
}

// Path 返回缓存文件绝对路径
func (c *Cache) Path(subdir, key string) string {
	if subdir == "" {
		return filepath.Join(c.Dir, key)
	}
	return filepath.Join(c.Dir, subdir, key)
}

// URL 由基础前缀拼接浏览器可访问的缓存地址
func (c *Cache) URL(subdir, key, basePrefix string) string {
	if subdir == "" {
		return strings.TrimRight(basePrefix, "/") + "/" + key
	}
	return strings.TrimRight(basePrefix, "/") + "/" + subdir + "/" + key
}

// Hit 检查缓存是否存在且未过期
func (c *Cache) Hit(subdir, key string) (bool, time.Time) {
	st, err := os.Stat(c.Path(subdir, key))
	if err != nil {
		return false, time.Time{}
	}
	return true, st.ModTime()
}

// Write 原子写入缓存（tmp + rename，防并发写坏）
func (c *Cache) Write(subdir, key, content string) error {
	p := c.Path(subdir, key)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp%d", p, os.Getpid())
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// CleanAll 清空全部缓存（站点子目录 + 根级散落），返回删除数
func (c *Cache) CleanAll() int {
	return c.walkAndRemove(func(string) bool { return true }, "")
}

// CleanRoot 仅清理根级散落缓存（不属于任何站点子目录），返回删除数
func (c *Cache) CleanRoot() int {
	n := 0
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".m3u8") {
			_ = os.Remove(filepath.Join(c.Dir, e.Name()))
			n++
		}
	}
	return n
}

// CleanSites 清理指定站点子目录内的全部缓存，返回删除数
func (c *Cache) CleanSites(subdirs []string) int {
	n := 0
	for _, sub := range subdirs {
		sub = strings.TrimSpace(strings.Trim(sub, "/\\"))
		if sub == "" {
			continue
		}
		dir := filepath.Join(c.Dir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range entries {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".m3u8") {
				continue
			}
			_ = os.Remove(filepath.Join(dir, f.Name()))
			n++
		}
	}
	return n
}

// CleanPreview 清理缓存目录内的临时/预览残留文件（*.tmp*），返回删除数
func (c *Cache) CleanPreview() int {
	n := 0
	_ = filepath.Walk(c.Dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.Contains(info.Name(), ".tmp") {
			_ = os.Remove(p)
			n++
		}
		return nil
	})
	return n
}

// walkAndRemove 遍历缓存目录按条件删除 m3u8 文件；subOnly 非空时只处理该子目录
func (c *Cache) walkAndRemove(match func(name string) bool, subOnly string) int {
	n := 0
	roots := []string{c.Dir}
	if subOnly != "" {
		roots = []string{filepath.Join(c.Dir, subOnly)}
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				subFiles, err := os.ReadDir(filepath.Join(root, e.Name()))
				if err != nil {
					continue
				}
				for _, f := range subFiles {
					if !strings.HasSuffix(f.Name(), ".m3u8") || !match(f.Name()) {
						continue
					}
					_ = os.Remove(filepath.Join(root, e.Name(), f.Name()))
					n++
				}
				continue
			}
			if !strings.HasSuffix(e.Name(), ".m3u8") || !match(e.Name()) {
				continue
			}
			_ = os.Remove(filepath.Join(root, e.Name()))
			n++
		}
	}
	return n
}

// CleanExpired 清理过期缓存文件，返回删除数
func (c *Cache) CleanExpired() int {
	cutoff := time.Now().Add(-c.TTL)
	n := 0
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() {
			subFile, err := os.ReadDir(filepath.Join(c.Dir, e.Name()))
			if err != nil {
				continue
			}
			for _, f := range subFile {
				if strings.HasSuffix(f.Name(), ".m3u8") {
					if info, err := f.Info(); err == nil && info.ModTime().Before(cutoff) {
						_ = os.Remove(filepath.Join(c.Dir, e.Name(), f.Name()))
						n++
					}
				}
			}
		} else if strings.HasSuffix(e.Name(), ".m3u8") {
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(c.Dir, e.Name()))
				n++
			}
		}
	}
	return n
}

// CleanByDays 清理 N 天前的缓存（all=0 全部），返回删除数
func (c *Cache) CleanByDays(days int) int {
	var cutoff time.Time
	if days > 0 {
		cutoff = time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	} else {
		cutoff = time.Now().Add(365 * 24 * time.Hour) // 未来时间=全部删
	}
	n := 0
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() {
			subFile, err := os.ReadDir(filepath.Join(c.Dir, e.Name()))
			if err != nil {
				continue
			}
			for _, f := range subFile {
				if strings.HasSuffix(f.Name(), ".m3u8") {
					if info, err := f.Info(); err == nil && info.ModTime().Before(cutoff) {
						_ = os.Remove(filepath.Join(c.Dir, e.Name(), f.Name()))
						n++
					}
				}
			}
		} else if strings.HasSuffix(e.Name(), ".m3u8") {
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(c.Dir, e.Name()))
				n++
			}
		}
	}
	return n
}
