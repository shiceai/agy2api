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
	"net/url"
	"strings"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/store"
)

// 代理测试：经代理取出口信息，再测到 Google 的延迟。两个第三方都只看到代理的出口 IP。
//   - 出口：ipinfo.io 给 IP、国家、地区、城市和运营商；取不到时（如被限流）退回 Cloudflare 的 trace，只有 IP 和国家。
//     这是"这一刻经代理访问它们看到的出口"，按域名分流的代理访问 Google 时可能不同。
//   - 延迟：新建连接请求 Google 的 generate_204（Chrome、Android 检测联网用的地址，返回空的 204），
//     计连代理、TLS 握手和请求的总时间。不碰 Cloud Code 接口，不带账号信息。

// 测试用的地址，测试时替换。
var (
	egressGeoURL   = "https://ipinfo.io/json"
	egressTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	latencyURL     = "https://www.google.com/generate_204"
)

const (
	egressTimeout = 15 * time.Second
	// egressRecheck 是自动重新测试的间隔：后台每 egressScan 扫一次，超过它没测试的启用代理重新测试。
	egressRecheck = 24 * time.Hour
	egressScan    = time.Hour
)

// proxyClient 返回经代理出网的一次性客户端；fresh 为 true 时不复用连接（测延迟要包括建连）。
func proxyClient(proxyURL string, fresh bool) (*http.Client, error) {
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
		DisableKeepAlives:   fresh,
	}}, nil
}

// testProxy 测试一个代理：先取出口，再测延迟。连不上代理时只有 Error。
func testProxy(ctx context.Context, proxyURL string) store.ProxyCheck {
	client, err := proxyClient(proxyURL, false)
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
	ms, err := latency(ctx, proxyURL)
	if err != nil {
		c.Error = "出口正常，但经代理连不上 Google：" + err.Error()
		return c
	}
	c.LatencyMs = ms
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

// latency 经代理新建连接请求 Google 的 generate_204，返回往返毫秒数（至少 1）。
func latency(ctx context.Context, proxyURL string) (int64, error) {
	client, err := proxyClient(proxyURL, true)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latencyURL, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		if antigravity.IsProxyError(err) {
			return 0, errors.New(proxyCause(err))
		}
		return 0, errors.New(unwrapURLErr(err))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	resp.Body.Close()
	ms := max(time.Since(start).Milliseconds(), 1)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("返回 HTTP %d", resp.StatusCode)
	}
	return ms, nil
}

// unwrapURLErr 去掉 Go 错误里"Get "https://…": "这类前缀，只留原因（不含代理地址和密码）。
func unwrapURLErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// startEgressChecks 启动后台测试：每 egressScan 扫一次，超过 egressRecheck 没测试的启用代理重新测试。
func (s *Service) startEgressChecks() {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
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
