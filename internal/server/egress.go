package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/store"
)

// 代理测试：都从服务器发起。经代理取出口信息，再测两个延迟。两个第三方都只看到代理的出口 IP。
//   - 出口：ipinfo.io 给 IP、国家、地区、城市和运营商；取不到时（如被限流）退回 Cloudflare 的 trace，只有 IP 和国家。
//     这是"这一刻经代理访问它们看到的出口"，按域名分流的代理访问 Google 时可能不同。
//   - 延迟：新建连接经代理请求两次 Google 的 generate_204（Chrome、Android 检测联网用的地址，返回空的 204）。
//     到代理是服务器和代理建立 TCP 连接的耗时；到 Google 是第二次请求在已建好的连接上的往返，不含建连和握手，
//     约等于连接复用时每个请求经代理多出的网络延迟。只到 Google 的接入点，不含 Google 内部到 Cloud Code 后端的一段：
//     不碰 Cloud Code 接口，不带账号信息，避免用账号的出口发官方客户端不会发的请求。

// 测试用的地址，测试时替换。
var (
	egressGeoURL   = "https://ipinfo.io/json"
	egressTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	latencyURL     = "https://www.google.com/generate_204"
)

const (
	egressTimeout = 15 * time.Second
	// egressRecheck 是自动重新测试的间隔：后台每 egressScan 扫一次，超过它没测试的启用代理重新测试。
	egressRecheck    = 24 * time.Hour
	egressScan       = time.Hour
	egressStartDelay = 30 * time.Second
)

// proxyClient 返回经代理出网的一次性客户端（自己的连接池，用完 CloseIdleConnections）。
func proxyClient(proxyURL string) (*http.Client, error) {
	u, err := antigravity.ParseProxy(proxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: egressTimeout, Transport: &http.Transport{
		Proxy: http.ProxyURL(u),
		OnProxyConnectResponse: func(_ context.Context, _ *url.URL, _ *http.Request, res *http.Response) error {
			if res.StatusCode != http.StatusOK {
				return &antigravity.ProxyError{Err: fmt.Errorf("代理拒绝连接：%s", res.Status)}
			}
			return nil
		},
		TLSHandshakeTimeout: 10 * time.Second,
	}}, nil
}

// testProxy 测试一个代理：先取出口，再测延迟。连不上代理时只有 Error。
func testProxy(ctx context.Context, proxyURL string) store.ProxyCheck {
	client, err := proxyClient(proxyURL)
	if err != nil {
		return store.ProxyCheck{Error: err.Error()}
	}
	defer client.CloseIdleConnections()
	c, err := egressGeo(ctx, client)
	if err != nil {
		if antigravity.IsProxyError(err) {
			return store.ProxyCheck{Error: "连不上代理：" + proxyCause(err)}
		}
		var traceErr error
		if c, traceErr = egressTrace(ctx, client); traceErr != nil {
			if antigravity.IsProxyError(traceErr) {
				return store.ProxyCheck{Error: "连不上代理：" + proxyCause(traceErr)}
			}
			return store.ProxyCheck{Error: "经代理取不到出口 IP：" + unwrapURLErr(traceErr)}
		}
	}
	connect, rtt, err := latency(ctx, proxyURL)
	c.ConnectMs = connect
	if err != nil {
		c.Error = "出口正常，但经代理连不上 Google：" + err.Error()
		return c
	}
	c.LatencyMs = rtt
	return c
}

// egressGeo 经代理取 ipinfo.io 的出口信息。
func egressGeo(ctx context.Context, client *http.Client) (store.ProxyCheck, error) {
	body, err := proxyGet(ctx, client, egressGeoURL)
	if err != nil {
		return store.ProxyCheck{}, err
	}
	var v struct {
		IP, City, Region, Country, Org string
	}
	if err := json.Unmarshal(body, &v); err != nil || v.IP == "" {
		return store.ProxyCheck{}, errors.New("出口信息格式不对")
	}
	return store.ProxyCheck{IP: v.IP, Country: v.Country, Region: v.Region, City: v.City, Org: v.Org}, nil
}

// egressTrace 经代理取 Cloudflare trace 里的出口 IP 和国家（ipinfo 取不到时用）。
func egressTrace(ctx context.Context, client *http.Client) (store.ProxyCheck, error) {
	body, err := proxyGet(ctx, client, egressTraceURL)
	if err != nil {
		return store.ProxyCheck{}, err
	}
	var c store.ProxyCheck
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "ip":
			c.IP = v
		case "loc":
			c.Country = v
		}
	}
	if c.IP == "" {
		return c, errors.New("没有返回出口 IP")
	}
	return c, nil
}

func proxyGet(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("返回 HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<16))
}

// latency 经代理新建连接请求两次 Google 的 generate_204。connect 是第一次请求里和代理建立 TCP 连接的耗时（到代理），
// rtt 是第二次请求复用这条连接的往返（到 Google），都是毫秒，测出来时至少 1。连上了代理、但请求失败时 connect 仍有值。
func latency(ctx context.Context, proxyURL string) (connect, rtt int64, err error) {
	client, err := proxyClient(proxyURL)
	if err != nil {
		return 0, 0, err
	}
	defer client.CloseIdleConnections()
	for i := range 2 {
		var t requestTiming
		d, err := t.get(ctx, client, latencyURL)
		if c, ok := t.connectTime(); i == 0 && ok {
			connect = max(c.Milliseconds(), 1)
		}
		if err != nil {
			return connect, 0, err
		}
		if i == 1 {
			rtt = max(d.Milliseconds(), 1)
		}
	}
	return connect, rtt, nil
}

// requestTiming 用 httptrace 记下一次请求的耗时：connect 是建立 TCP 连接的耗时（配了代理时连的是代理），
// 请求本身从拿到连接算到收到响应首字节，不含建连和握手（复用连接时拿到连接是立即的）。
// 双栈地址可能并发拨号，回调会并发，加锁。connected 单独记：时钟精度低时（如 Windows）本机建连可能量出 0。
type requestTiming struct {
	mu        sync.Mutex
	dialing   map[string]time.Time
	connected bool
	connect   time.Duration
	gotConn   time.Time
	first     time.Time
}

// connectTime 返回建立 TCP 连接的耗时；这次请求没有新建连接、或没连上时 ok 为 false。
func (t *requestTiming) connectTime() (d time.Duration, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connect, t.connected
}

// get 发一次 GET 并读完响应体（让连接回到连接池），返回从拿到连接到收到响应首字节的耗时。
func (t *requestTiming) get(ctx context.Context, client *http.Client, target string) (time.Duration, error) {
	trace := &httptrace.ClientTrace{
		ConnectStart: func(_, addr string) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.dialing == nil {
				t.dialing = map[string]time.Time{}
			}
			t.dialing[addr] = time.Now()
		},
		ConnectDone: func(_, addr string, err error) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if start, ok := t.dialing[addr]; ok && err == nil && !t.connected {
				t.connected, t.connect = true, time.Since(start)
			}
		},
		GotConn: func(httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.gotConn = time.Now()
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.first = time.Now()
		},
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if antigravity.IsProxyError(err) {
			return 0, errors.New(proxyCause(err))
		}
		return 0, errors.New(unwrapURLErr(err))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("返回 HTTP %d", resp.StatusCode)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gotConn.IsZero() || t.first.Before(t.gotConn) {
		return 0, errors.New("没有测到响应时间")
	}
	return t.first.Sub(t.gotConn), nil
}

// unwrapURLErr 去掉 Go 错误里"Get "https://…": "这类前缀，只留原因（不含代理地址和密码）。
func unwrapURLErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// startEgressChecks 启动后台测试：启动后 egressStartDelay 先扫一次（停机较久或升级后标成未测试的代理不必等上
// 一个小时），之后每 egressScan 扫一次，超过 egressRecheck 没测试的启用代理重新测试。
func (s *Service) startEgressChecks() {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		select {
		case <-s.bgStop:
			return
		case <-time.After(egressStartDelay):
			s.recheckStaleProxies()
		}
		t := time.NewTicker(egressScan)
		defer t.Stop()
		for {
			select {
			case <-s.bgStop:
				return
			case <-t.C:
				s.recheckStaleProxies()
			}
		}
	}()
}

func (s *Service) recheckStaleProxies() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	proxies, err := s.store.ListProxies(ctx, s.enc)
	if err != nil {
		slog.Error("读取代理失败，跳过这次后台测试", "err", err)
		return
	}
	cutoff := time.Now().Add(-egressRecheck).UnixMilli()
	var stale []*store.Proxy
	for _, p := range proxies {
		if p.Status == store.StatusEnabled && p.CheckedAt < cutoff {
			stale = append(stale, p)
		}
	}
	if len(stale) > 0 {
		slog.Info("后台测试代理", "proxies", len(stale))
		s.checkProxies(ctx, stale)
	}
}
