package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

var (
	zenProxyCount atomic.Uint64

	zenProxyCooldowns   = map[int]time.Time{} // 代理索引 -> 冷却截止
	zenProxyCooldownsMu sync.Mutex
)

// proxyClientCache 按代理 URL 缓存钉定代理的 HTTP 客户端（uTLS Chrome 指纹 + h2）。
// key 为代理 URL；"" 表示直连。zen 与 cline 上游共用，请求级轮转时
// 每次上游尝试显式挑选代理并用对应客户端发出。
var (
	proxyClientCacheMu sync.Mutex
	proxyClientCache   = map[string]*http.Client{}
)

// proxyClientFor 返回钉定到指定代理的 HTTP 客户端（缓存复用）。
// proxyURL 为空时返回直连客户端。
func proxyClientFor(proxyURL string) *http.Client {
	proxyClientCacheMu.Lock()
	defer proxyClientCacheMu.Unlock()
	if c, ok := proxyClientCache[proxyURL]; ok {
		return c
	}
	var transport *http.Transport
	if proxyURL == "" {
		transport = buildTransport(func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
			return d.DialContext(ctx, network, addr)
		})
	} else {
		transport = buildTransport(func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialViaProxy(ctx, proxyURL, network, addr)
		})
	}
	c := &http.Client{Transport: transport}
	proxyClientCache[proxyURL] = c
	return c
}

// cooldownUpstreamProxy 标记某出口代理冷却,冷却期内轮询跳过
func cooldownUpstreamProxy(idx int, d time.Duration) {
	if idx < 0 {
		return
	}
	if d <= 0 {
		d = 10 * time.Minute
	}
	if d > maxCooldown {
		d = maxCooldown
	}
	zenProxyCooldownsMu.Lock()
	zenProxyCooldowns[idx] = time.Now().Add(d)
	zenProxyCooldownsMu.Unlock()
}

func zenProxyAvailable(idx int) bool {
	zenProxyCooldownsMu.Lock()
	defer zenProxyCooldownsMu.Unlock()
	until, ok := zenProxyCooldowns[idx]
	if !ok {
		return true
	}
	if time.Now().After(until) {
		delete(zenProxyCooldowns, idx)
		return true
	}
	return false
}

// zenProxyCooldownStatus 返回仍处于冷却的出口及其解除时刻。
// 时刻用 RFC3339（带时区）而不是服务器格式化的 "15:04:05"：面板按浏览器本地
// 时区渲染，服务器格式化只会给出容器时区（UTC）的读数，用户看到的是错的钟点。
func zenProxyCooldownStatus() map[string]string {
	cfg := getZenConfig()
	zenProxyCooldownsMu.Lock()
	defer zenProxyCooldownsMu.Unlock()
	out := map[string]string{}
	for idx, until := range zenProxyCooldowns {
		if idx >= 0 && idx < len(cfg.Proxies) {
			if time.Now().Before(until) {
				out[cfg.Proxies[idx]] = until.UTC().Format(time.RFC3339)
			}
		}
	}
	return out
}

// pickUpstreamProxy 按策略为一次上游尝试选择代理,返回 (代理URL, 索引);
// 无代理配置返回 ("", -1)。跳过冷却中的代理;全部冷却时返回轮转位。
// 由调用方在每次上游尝试时显式调用 —— 请求级轮转（round_robin 一比一）,
// 不依赖连接复用时机。
func pickUpstreamProxy() (string, int) {
	cfg := getZenConfig()
	n := len(cfg.Proxies)
	if n == 0 {
		return "", -1
	}
	idx := int(zenProxyCount.Add(1)-1) % n
	switch cfg.ProxyStrategy {
	case "random":
		idx = int(time.Now().UnixNano() % int64(n))
	case "fill":
		idx = 0
	}
	// 冷却跳过:线性探测下一个可用代理
	for i := 0; i < n; i++ {
		if zenProxyAvailable(idx) {
			break
		}
		idx = (idx + 1) % n
	}
	return cfg.Proxies[idx], idx
}

func maskProxyURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("***")
	return u.String()
}

func buildZenTransport() *http.Transport {
	// 共享 zenHTTPClient 现仅作为直连兜底（如模型同步）；带代理的请求
	// 走 proxyClientFor() 按次钉定代理。拨号不再隐式挑选代理。
	return buildTransport(func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, network, addr)
	})
}

// buildTransport 构造 Bun/BoringSSL 指纹(h1,官方 CLI 实测只用 http/1.1)+
// 可注入拨号的 Transport。注意: Bun 指纹 ALPN 只报 http/1.1,握手协商出 h1,
// 因此不能再 RegisterProtocol("https"→h2),否则 h2 帧解析器会对 h1 明文
// 连接报错(frame too large)。标准 Transport 按 ALPN 自动走 h1。
func buildTransport(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Transport {
	t := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
	}
	t.DialContext = dial
	t.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			raw.Close()
			return nil, err
		}
		uconn := utls.UClient(raw, &utls.Config{
			ServerName: host,
			NextProtos: []string{"http/1.1"},
		}, utls.HelloCustom)
		if err := uconn.ApplyPreset(bunSpecForConn()); err != nil {
			raw.Close()
			return nil, err
		}
		if err := uconn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return uconn, nil
	}
	return t
}

// dialViaProxy 统一拨号:http/https 走 CONNECT,socks5 走 SOCKS5 握手
func dialViaProxy(ctx context.Context, raw, network, addr string) (net.Conn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("bad proxy url: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
		return dialHTTPProxy(ctx, u, network, addr)
	case "socks5", "socks5h":
		auth := &proxy.Auth{}
		if u.User != nil {
			auth.User = u.User.Username()
			auth.Password, _ = u.User.Password()
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return nil, err
		}
		type ctxDialer interface {
			DialContext(context.Context, string, string) (net.Conn, error)
		}
		if cd, ok := d.(ctxDialer); ok {
			return cd.DialContext(ctx, network, addr)
		}
		// 旧接口无 ctx:包装
		type result struct {
			c   net.Conn
			err error
		}
		ch := make(chan result, 1)
		go func() {
			c, err := d.Dial(network, addr)
			ch <- result{c, err}
		}()
		select {
		case <-ctx.Done():
			// 后台拨号可能已成功: 异步取出并关闭,避免连接泄漏
			go func() {
				if r := <-ch; r.c != nil {
					r.c.Close()
				}
			}()
			return nil, ctx.Err()
		case r := <-ch:
			return r.c, r.err
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
}

// dialHTTPProxy 通过 http(s) 代理建立 CONNECT 隧道
func dialHTTPProxy(ctx context.Context, u *url.URL, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	rawConn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		tlsConn := tls.Client(rawConn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			rawConn.Close()
			return nil, err
		}
		rawConn = tlsConn
	}

	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	if u.User != nil {
		cred := base64.StdEncoding.EncodeToString([]byte(u.User.String()))
		req.Header.Set("Proxy-Authorization", "Basic "+cred)
	}
	// CONNECT 握手阶段加截止时间: 代理接受 TCP 却不响应 CONNECT 时不能永久
	// 挂起; 客户端取消(ctx.Done)时同步中断。隧道建立后清除截止,不影响后续使用
	rawConn.SetDeadline(time.Now().Add(30 * time.Second))
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			rawConn.Close()
		case <-stop:
		}
	}()
	if err := req.Write(rawConn); err != nil {
		rawConn.Close()
		return nil, err
	}

	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		rawConn.Close()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rawConn.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("proxy CONNECT %s: %s %s", u.Host, resp.Status, strings.TrimSpace(string(b)))
	}
	rawConn.SetDeadline(time.Time{})
	return rawConn, nil
}
