package core

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// Fetch 抓取 URL 全文，仅返回 200 时的 body
func Fetch(client *http.Client, url, ua string, timeout time.Duration) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// FetchRange 通过 Range 请求抓取片段前 size 字节（用于采样哈希，对应 PHP fetch_sample）
func FetchRange(client *http.Client, url, ua string, size int, timeout time.Duration) ([]byte, error) {
	if size <= 0 {
		size = 1
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", size-1))
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	return body, nil
}

// isHTTPLink 判断是否为 http(s) 链接
func isHTTPLink(u string) bool {
	return len(u) >= 7 && (u[:7] == "http://" || u[:7] == "HTTP://") ||
		len(u) >= 8 && (u[:8] == "https://" || u[:8] == "HTTPS://")
}
