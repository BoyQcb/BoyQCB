package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProxyTestResult 代理连通性测试结果
type ProxyTestResult struct {
	OK          bool   `json:"ok"`
	Status      int    `json:"status,omitempty"`       // HTTP 状态码
	ElapsedMS   int64  `json:"elapsed_ms"`             // 耗时（毫秒）
	RemoteAddr  string `json:"remote_addr,omitempty"`  // 实际建立连接的对端地址（走代理时即代理节点）
	ContentType string `json:"content_type,omitempty"` // 响应类型
	Bytes       int    `json:"bytes,omitempty"`        // 读取到的字节数
	Snippet     string `json:"snippet,omitempty"`      // 响应前若干字节（文本）
	Err         string `json:"err,omitempty"`          // 失败原因
}

// NewClientWithProxy 构造使用指定代理的 HTTP 客户端。
// proxyRaw 语义同 ParseProxyURL：空串表示直连（不继承环境变量，便于测试对比）。
func NewClientWithProxy(timeout time.Duration, proxyRaw string) *http.Client {
	return newHTTPClient(timeout, func(req *http.Request) (*url.URL, error) {
		return ParseProxyURL(proxyRaw, nil)
	})
}

// TestProxy 用指定代理请求目标地址，返回状态码 / 耗时 / 出口信息。
// target 为空时用 ipify 探测出口 IP。
func TestProxy(proxyRaw, target string, timeout time.Duration, ua string) ProxyTestResult {
	target = strings.TrimSpace(target)
	// 默认探测站先用明文 http（多数 HTTP 代理对 CONNECT/https 目标支持不稳定），
	// 该站会直接返回出口 IP，便于确认代理是否真的生效
	if target == "" {
		target = "http://myip.ipip.net/"
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if ua == "" {
		ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"
	}

	// 包装 DialContext 以记录真实对端地址：直连时为源站 IP，走代理时为代理节点 IP
	var remote string
	client := newHTTPClient(timeout, func(req *http.Request) (*url.URL, error) {
		return ParseProxyURL(proxyRaw, nil)
	})
	if tr, ok := client.Transport.(*http.Transport); ok {
		base := tr.DialContext
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := base(ctx, network, addr)
			if err == nil && conn != nil && remote == "" {
				remote = conn.RemoteAddr().String()
			}
			return conn, err
		}
	}

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return ProxyTestResult{OK: false, Err: "目标地址无效: " + err.Error()}
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		msg := err.Error()
		return ProxyTestResult{OK: false, ElapsedMS: elapsed, Err: msg}
	}
	defer resp.Body.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(resp.Body, head)
	return ProxyTestResult{
		OK:          resp.StatusCode < 400,
		Status:      resp.StatusCode,
		ElapsedMS:   elapsed,
		RemoteAddr:  remote,
		ContentType: resp.Header.Get("Content-Type"),
		Bytes:       n,
		Snippet:     strings.TrimSpace(string(head[:n])),
	}
}
