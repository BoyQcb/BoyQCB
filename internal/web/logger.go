package web

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger 按日期写日志到 logs/YYYY-MM-DD.log（对齐 PHP write_log）
type Logger struct {
	mu  sync.Mutex
	dir string
}

// NewLogger 构造日志器（自动建目录）
func NewLogger(logDir string) *Logger {
	_ = os.MkdirAll(logDir, 0755)
	return &Logger{dir: logDir}
}

// Write 追加一条日志
func (l *Logger) Write(level, src, msg string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf("[%s] [%s] [%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05"), level, src, msg)
	f := filepath.Join(l.dir, time.Now().Format("2006-01-02")+".log")
	if d := filepath.Dir(f); d != "" {
		_ = os.MkdirAll(d, 0755)
	}
	fh, err := os.OpenFile(f, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer fh.Close()
	_, _ = fh.WriteString(line)
}

// Tail 读取最近 limit 行日志
func (l *Logger) Tail(limit int) []string {
	if l == nil {
		return nil
	}
	f := filepath.Join(l.dir, time.Now().Format("2006-01-02")+".log")
	data, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	all := []string{}
	start := 0
	for i, b := range data {
		if b == '\n' {
			all = append(all, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		all = append(all, string(data[start:]))
	}
	if limit <= 0 || limit > len(all) {
		return all
	}
	return all[len(all)-limit:]
}
