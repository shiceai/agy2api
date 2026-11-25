package antigravity

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 账号绑定的代理（Credential.Proxy）：这个账号的令牌刷新、身份查询、控制面和数据面请求都经它出网。
// 每个代理一对 HTTP 客户端（控制面、数据面），连接按代理分别复用。代理只在 TCP 层转发，TLS 端到端，
// 上游看到的 TLS 指纹和 HTTP/2 与直连相同。连不上代理时报 ProxyError，不回落直连。
// Proxy 为空时直连（进程设了 HTTPS_PROXY 等环境变量时走它）。

// ProxyError 表示请求没能经账号绑定的代理连到上游：连不上代理、SOCKS 握手失败，或代理拒绝 CONNECT。
type ProxyError struct {
	Err error
}

func (e *ProxyError) Error() string { return "proxy: " + e.Err.Error() }
func (e *ProxyError) Unwrap() error { return e.Err }

// IsProxyError 报告错误是否是代理故障（请求没到达上游）。Go 把连不上代理包装成 Op 为 proxyconnect 的
// net.OpError，SOCKS 握手失败的 Op 以 socks 开头；CONNECT 被拒由 OnProxyConnectResponse 转成 ProxyError。
func IsProxyError(err error) bool {
	var pe *ProxyError
	if errors.As(err, &pe) {
		return true
	}
	var oe *net.OpError
	return errors.As(err, &oe) && (oe.Op == "proxyconnect" || strings.HasPrefix(oe.Op, "socks"))
}

// ProxySchemes 是支持的代理协议，都由 net/http 原生处理。Go 的 socks5 和 socks5h 行为相同：
// 都把目标域名交给代理解析，不在本机查 DNS。
var ProxySchemes = []string{"http", "https", "socks5", "socks5h"}

// ParseProxy 校验代理地址：协议在 ProxySchemes 里，带主机和端口，不带路径。
func ParseProxy(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("代理地址格式不对")
	}
	ok := false
	for _, s := range ProxySchemes {
		ok = ok || u.Scheme == s
	}
	if !ok {
		return nil, fmt.Errorf("不支持的代理协议 %q，可用 http、https、socks5、socks5h", u.Scheme)
	}
	if u.Hostname() == "" || u.Port() == "" {
		return nil, fmt.Errorf("代理地址要有主机和端口")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("代理地址不能带路径或参数")
	}
	return u, nil
}

// route 是一个出口（直连或某个代理）上的两个 HTTP 客户端。
type route struct {
	control *http.Client // 控制面和 OAuth：整体超时 30 秒
	data    *http.Client // 数据面：不设整体超时，按请求 ctx 控制（流式需要长连接）
}

// route 返回经 proxy 出网的客户端，空串为直连。按代理地址缓存，同一代理复用连接。
func (c *Client) route(proxy string) (*route, error) {
	if proxy == "" {
		return &c.direct, nil
	}
	if v, ok := c.routes.Load(proxy); ok {
		return v.(*route), nil
	}
	u, err := ParseProxy(proxy)
	if err != nil {
		return nil, &ProxyError{Err: err}
	}
	fixed := http.ProxyURL(u)
	r := &route{
		control: &http.Client{Timeout: 30 * time.Second, Transport: newControlTransport(fixed)},
		data:    &http.Client{Transport: newDataTransport(fixed)},
	}
	v, _ := c.routes.LoadOrStore(proxy, r)
	return v.(*route), nil
}

// ForgetProxy 丢掉经 proxy 出网的客户端并关闭它的空闲连接。代理被删除或改了地址后调用，否则旧地址的
// 连接池一直留在内存里。正在进行的请求不受影响；之后再用到这个地址会重新创建，所以多调用一次也无妨。
func (c *Client) ForgetProxy(proxy string) {
	if proxy == "" {
		return
	}
	if v, ok := c.routes.LoadAndDelete(proxy); ok {
		r := v.(*route)
		r.control.CloseIdleConnections()
		r.data.CloseIdleConnections()
	}
}

// rejectProxyConnect 把代理对 CONNECT 的非 200 响应（如 407 认证失败）转成 ProxyError。
func rejectProxyConnect(_ context.Context, _ *url.URL, _ *http.Request, res *http.Response) error {
	if res.StatusCode != http.StatusOK {
		return &ProxyError{Err: fmt.Errorf("代理拒绝连接：%s", res.Status)}
	}
	return nil
}

// newControlTransport 构造控制面的 Transport：取值与 http.DefaultTransport 相同（直连时控制面就用它），
// 只换代理。显式构造，不 Clone 全局的 DefaultTransport（用过之后带着填好的 TLS 配置）。
func newControlTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:                  proxy,
		OnProxyConnectResponse: rejectProxyConnect,
		DialContext:            (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           100,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ExpectContinueTimeout:  time.Second,
	}
}

// newDataTransport 构造数据面的 Transport：Go 默认 TLS 配置、允许 HTTP/2。
//
// 官方客户端 2.31.0 实抓：cloudcode 走 HTTP/2，ALPN 为 h2、http/1.1，TLS 指纹与 OAuth 请求相同
// （都是 Go 默认配置）；2.28.2 还是 HTTP/1.1、不带 ALPN。UA 声明的是 2.31.x，传输层跟着 2.31.0。
func newDataTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:                  proxy,
		OnProxyConnectResponse: rejectProxyConnect,
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 60 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true, // 自定义了 DialContext，要显式打开 HTTP/2
		TLSHandshakeTimeout:    10 * time.Second,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    10,
		MaxConnsPerHost:        20,
		IdleConnTimeout:        10 * time.Minute,
		ExpectContinueTimeout:  time.Second,
	}
}
