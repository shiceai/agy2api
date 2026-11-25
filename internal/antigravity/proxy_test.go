package antigravity

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// forwardProxy 是测试用的 HTTP 正向代理：转发明文 HTTP 请求并计数，CONNECT 一律回 407。
func forwardProxy(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			http.Error(w, "auth", http.StatusProxyAuthRequired)
			return
		}
		n.Add(1)
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// 绑定了代理的账号：令牌刷新、身份、控制面、生成都经代理；没绑的直连。
func TestRequestsGoThroughAccountProxy(t *testing.T) {
	var upstream atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3600}`))
		case "/userinfo":
			_, _ = w.Write([]byte(`{"email":"a@example.com"}`))
		default:
			_, _ = w.Write([]byte(`{"response":{"candidates":[]}}`))
		}
	}))
	defer up.Close()
	proxy, through := forwardProxy(t)
	c := NewClient(Options{TokenURL: up.URL + "/token", UserInfoURL: up.URL + "/userinfo", UpstreamBases: []string{up.URL}})
	ctx := context.Background()

	cred := Credential{RefreshToken: "rt", Proxy: proxy.URL}
	if err := c.Identify(ctx, &cred); err != nil || cred.Email != "a@example.com" {
		t.Fatalf("identify: %v %+v", err, cred)
	}
	resp, err := c.Generate(ctx, cred.Proxy, cred.AccessToken, "", []byte(`{}`), false, false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if through.Load() != 3 || upstream.Load() != 3 {
		t.Fatalf("via proxy: proxy %d upstream %d", through.Load(), upstream.Load())
	}

	direct := Credential{RefreshToken: "rt"}
	if err := c.Refresh(ctx, &direct); err != nil {
		t.Fatal(err)
	}
	if through.Load() != 3 || upstream.Load() != 4 {
		t.Fatalf("direct: proxy %d upstream %d", through.Load(), upstream.Load())
	}
}

// 代理连不上时报代理故障，不回落直连，也不换基址重试。
func TestProxyDownDoesNotFallBack(t *testing.T) {
	var upstream atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstream.Add(1) }))
	defer up.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	l.Close()

	c := NewClient(Options{TokenURL: up.URL + "/token", UpstreamBases: []string{up.URL, up.URL}})
	ctx := context.Background()
	err = c.Refresh(ctx, &Credential{RefreshToken: "rt", Proxy: dead})
	if err == nil || !IsProxyError(err) {
		t.Fatalf("refresh via dead proxy: %v", err)
	}
	if _, err := c.Generate(ctx, dead, "at", "", []byte(`{}`), false, false); err == nil || !IsProxyError(err) {
		t.Fatalf("generate via dead proxy: %v", err)
	}
	if upstream.Load() != 0 {
		t.Fatalf("fell back to direct: %d upstream requests", upstream.Load())
	}
}

// 代理拒绝 CONNECT（如 407 认证失败）也算代理故障。
func TestProxyConnectRejected(t *testing.T) {
	proxy, _ := forwardProxy(t)
	c := NewClient(Options{UpstreamBases: []string{"https://upstream.invalid"}})
	_, err := c.Generate(context.Background(), proxy.URL, "at", "", []byte(`{}`), false, false)
	if err == nil || !IsProxyError(err) {
		t.Fatalf("CONNECT rejected: %v", err)
	}
}

func TestParseProxy(t *testing.T) {
	for raw, ok := range map[string]bool{
		"http://1.2.3.4:8080":           true,
		"https://u:p@proxy.example:443": true,
		"socks5://u:p@1.2.3.4:1080":     true,
		"socks5h://u:p@1.2.3.4:1080":    true,
		"ftp://1.2.3.4:21":              false,
		"http://1.2.3.4":                false,
		"http://1.2.3.4:80/path":        false,
		"1.2.3.4:8080":                  false,
	} {
		if _, err := ParseProxy(raw); (err == nil) != ok {
			t.Errorf("ParseProxy(%q) err=%v, want ok=%v", raw, err, ok)
		}
	}
}

// socksServer 是测试用的最小 SOCKS5 代理（无认证，只支持 CONNECT），记下每次请求的目标（域名带 "domain:" 前缀）。
func socksServer(t *testing.T) (string, chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	targets := make(chan string, 16)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 262)
				// 问候：VER NMETHODS METHODS，回复不需要认证。
				if _, err := io.ReadFull(c, buf[:2]); err != nil {
					return
				}
				if _, err := io.ReadFull(c, buf[:buf[1]]); err != nil {
					return
				}
				_, _ = c.Write([]byte{5, 0})
				// 请求：VER CMD RSV ATYP DST.ADDR DST.PORT
				if _, err := io.ReadFull(c, buf[:4]); err != nil {
					return
				}
				var host string
				switch buf[3] {
				case 1:
					_, _ = io.ReadFull(c, buf[:4])
					host = net.IP(buf[:4]).String()
				case 3:
					_, _ = io.ReadFull(c, buf[:1])
					n := int(buf[0])
					_, _ = io.ReadFull(c, buf[:n])
					host = "domain:" + string(buf[:n])
				default:
					return
				}
				_, _ = io.ReadFull(c, buf[:2])
				port := int(buf[0])<<8 | int(buf[1])
				targets <- host
				up, err := net.Dial("tcp", net.JoinHostPort(strings.TrimPrefix(host, "domain:"), strconv.Itoa(port)))
				if err != nil {
					_, _ = c.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}(c)
		}
	}()
	return l.Addr().String(), targets
}

// socks5 和 socks5h 都经 SOCKS 代理出网，目标域名交给代理解析（不在本机查 DNS）。
func TestSocksProxyResolvesRemotely(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"candidates":[]}}`))
	}))
	defer up.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(up.URL, "http://"))
	addr, targets := socksServer(t)
	c := NewClient(Options{UpstreamBases: []string{"http://localhost:" + port}})
	for _, scheme := range []string{"socks5", "socks5h"} {
		resp, err := c.Generate(context.Background(), scheme+"://"+addr, "at", "", []byte(`{}`), false, false)
		if err != nil {
			t.Fatalf("%s: %v", scheme, err)
		}
		resp.Body.Close()
		if got := <-targets; got != "domain:localhost" {
			t.Fatalf("%s: proxy got target %q, want the unresolved domain", scheme, got)
		}
	}
}

// 代理删除或改地址后 ForgetProxy 丢掉旧地址的客户端；之后再用到这个地址会重新创建。
func TestForgetProxy(t *testing.T) {
	c := NewClient(Options{})
	const p = "http://127.0.0.1:18080"
	r1, err := c.route(p)
	if err != nil {
		t.Fatal(err)
	}
	if r2, _ := c.route(p); r2 != r1 {
		t.Fatal("同一个代理地址应复用同一组客户端")
	}
	c.ForgetProxy(p)
	if _, ok := c.routes.Load(p); ok {
		t.Fatal("ForgetProxy 后不应再留着这个地址的客户端")
	}
	if r3, _ := c.route(p); r3 == r1 {
		t.Fatal("再用到这个地址时应重新创建客户端")
	}
	c.ForgetProxy("")                       // 直连：什么也不做
	c.ForgetProxy("http://127.0.0.1:18081") // 没用过的地址：什么也不做
}
