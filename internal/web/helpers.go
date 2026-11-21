package web

import (
	"os"
	"path/filepath"
	"strings"
)

// filepathSafe 安全拼接路径（拒绝 .. 逃逸）
func filepathSafe(base, rel string) string {
	p := filepath.Join(base, rel)
	if strings.HasPrefix(filepath.Clean(p), filepath.Clean(base)+string(os.PathSeparator)) ||
		filepath.Clean(p) == filepath.Clean(base) {
		return p
	}
	return base
}

func osPID() int {
	return os.Getpid()
}

// cacheStat 统计缓存目录文件数与占用字节
func (s *Server) cacheStat() (int, int64) {
	files := 0
	var bytes int64 = 0
	_ = filepath.Walk(s.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, ".m3u8") {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}
