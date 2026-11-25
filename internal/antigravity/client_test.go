package antigravity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDataHTTPClientUsesHTTP2(t *testing.T) {
	tr, ok := NewClient(Options{}).direct.data.Transport.(*http.Transport)
	if !ok {
		t.Fatal("数据面应使用 *http.Transport")
	}
	// TLSNextProto 非 nil（哪怕是空 map）会关掉 HTTP/2，ClientHello 也不再带 ALPN。
	if !tr.ForceAttemptHTTP2 || tr.TLSNextProto != nil || tr.TLSClientConfig != nil {
		t.Fatalf("数据面应允许 HTTP/2 并用 Go 默认 TLS 配置：force=%v nextProto=%v tls=%v",
			tr.ForceAttemptHTTP2, tr.TLSNextProto != nil, tr.TLSClientConfig != nil)
	}

	// 对一个支持 HTTP/2 的 TLS 服务实际协商一次（只为信任测试证书补上 RootCAs）。
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.TLSClientConfig.NextProtos = nil
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Proto != "HTTP/2.0" {
		t.Fatalf("应协商出 HTTP/2，实际 %s", resp.Proto)
	}
}

// 请求头对齐官方客户端：令牌刷新用 Go 默认 UA；Cloud Code 请求带 Antigravity UA，不带 Accept。
func TestRequestHeaders(t *testing.T) {
	var mu sync.Mutex
	headers := map[string]http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers[r.URL.Path] = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	defer srv.Close()

	c := NewClient(Options{TokenURL: srv.URL + "/token", UpstreamBases: []string{srv.URL}})
	cred := Credential{RefreshToken: "rt"}
	if err := c.Refresh(context.Background(), &cred); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		resp, err := c.Generate(context.Background(), "", "at", "", []byte(`{}`), stream, false)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	if ua := headers["/token"].Get("User-Agent"); strings.HasPrefix(ua, "antigravity/") || !strings.HasPrefix(ua, "Go-http-client/") {
		t.Fatalf("令牌刷新应用 Go 默认 UA，实际 %q", ua)
	}
	for _, path := range []string{"/v1internal:generateContent", "/v1internal:streamGenerateContent"} {
		h := headers[path]
		if h == nil {
			t.Fatalf("没有收到 %s", path)
		}
		if h.Get("User-Agent") != DefaultUserAgent || h.Get("Content-Type") != "application/json" || h.Get("Authorization") != "Bearer at" {
			t.Fatalf("%s 请求头不对：%v", path, h)
		}
		if _, ok := h["Accept"]; ok {
			t.Fatalf("%s 不应带 Accept：%v", path, h)
		}
	}
}
