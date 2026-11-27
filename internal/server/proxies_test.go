package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shiceai/agy2api/internal/store"
)

// testForwardProxy 是测试用的 HTTP 正向代理：转发明文 HTTP 请求（mock 上游是 http）并计数。
func testForwardProxy(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(forwardHandler(&n))
	t.Cleanup(srv.Close)
	return srv, &n
}

// forwardHandler 是正向代理的处理函数，每个请求给 n 加一。
func forwardHandler(n *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})
}

type testProxyView struct {
	ID            int64  `json:"id"`
	Host          string `json:"host"`
	Username      string `json:"username"`
	EgressIP      string `json:"egress_ip"`
	EgressCountry string `json:"egress_country"`
	CheckError    string `json:"check_error"`
	Accounts      []struct {
		ID int64 `json:"id"`
	} `json:"accounts"`
}

func listProxies(t *testing.T, srv *httptest.Server) ([]testProxyView, string) {
	t.Helper()
	resp := doReq(t, srv, http.MethodGet, "/api/admin/proxies", nil, "")
	raw := readBody(resp)
	var out struct {
		Proxies []testProxyView `json:"proxies"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("proxies: %v %s", err, raw)
	}
	return out.Proxies, raw
}

// 添加代理：完整地址和 host:port:user:pass 都行，重复的跳过，格式不对的报错；接口不返回密码。
func TestAddProxies(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	body, _ := json.Marshal(map[string]string{"scheme": "socks5", "text": "http://u1:secret1@10.0.0.1:8080\n10.0.0.2:1080:u2:secret2\nsocks5h://u3:secret3@10.0.0.3:1080\n\nbad line\nhttp://u1:secret1@10.0.0.1:8080\n"})
	resp := doReq(t, srv, http.MethodPost, "/api/admin/proxies", nil, string(body))
	var res struct {
		Results []proxyAddResult `json:"results"`
	}
	decode(t, resp, &res)
	statuses := []string{}
	for _, r := range res.Results {
		statuses = append(statuses, r.Status)
		if strings.Contains(r.Line, "secret") {
			t.Fatalf("password echoed: %q", r.Line)
		}
	}
	if strings.Join(statuses, ",") != "added,added,added,invalid,duplicate" {
		t.Fatalf("statuses: %v", statuses)
	}
	list, raw := listProxies(t, srv)
	if len(list) != 3 || list[1].Host != "10.0.0.2:1080" || list[1].Username != "u2" ||
		!strings.Contains(raw, `"scheme":"socks5h"`) || strings.Contains(raw, "secret") {
		t.Fatalf("list: %s", raw)
	}
	id := strconv.FormatInt(list[0].ID, 10)
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/proxies/"+id, nil, `{"name":"东京 1"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %s", resp.StatusCode, readBody(resp))
	}
	if resp := doReq(t, srv, http.MethodDelete, "/api/admin/proxies/"+id, nil, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

// 添加账号时自动分配代理：确认身份、取项目和模型、生成请求都经代理；代理还绑着账号时不能删；
// 没有空闲代理时自动分配报错；改成直连后不再经代理。
func TestAccountProxyBinding(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	proxy, through := testForwardProxy(t)
	body, _ := json.Marshal(map[string]string{"text": proxy.URL})
	readBody(doReq(t, srv, http.MethodPost, "/api/admin/proxies", nil, string(body)))
	list, _ := listProxies(t, srv)
	proxyID := strconv.FormatInt(list[0].ID, 10)

	imported := postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-1","proxy":{"mode":"auto"}}`)
	a, err := st.GetAccount(context.Background(), svc.enc, imported.Account.ID)
	if err != nil || a.ProxyID != list[0].ID {
		t.Fatalf("account proxy: %v %+v", err, a)
	}
	afterImport := through.Load()
	if afterImport < 4 { // 令牌、身份、loadCodeAssist、fetchAvailableModels
		t.Fatalf("import requests via proxy: %d", afterImport)
	}

	resp := doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"k"}`)
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &key)
	generate := func() int {
		resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent",
			map[string]string{"x-goog-api-key": key.Secret}, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		readBody(resp)
		return resp.StatusCode
	}
	if code := generate(); code != http.StatusOK || through.Load() <= afterImport {
		t.Fatalf("generate via proxy: %d (proxy %d -> %d)", code, afterImport, through.Load())
	}

	if resp := doReq(t, srv, http.MethodDelete, "/api/admin/proxies/"+proxyID, nil, ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete bound proxy: %d", resp.StatusCode)
	}
	// 唯一空闲的代理最近检测失败：自动分配不用它。
	bad := &store.Proxy{Name: "bad", URL: "http://127.0.0.1:1"}
	if err := st.CreateProxy(context.Background(), svc.enc, bad); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProxyCheck(context.Background(), bad.ID, store.ProxyCheck{Error: "连不上代理"}); err != nil {
		t.Fatal(err)
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/import", nil, `{"refresh_token":"rt-2","proxy":{"mode":"auto"}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("auto without free proxy: %d %s", resp.StatusCode, readBody(resp))
	}

	ids := `{"ids":[` + strconv.FormatInt(a.ID, 10) + `],"mode":"none"}`
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/assign-proxy", nil, ids); resp.StatusCode != http.StatusOK {
		t.Fatalf("assign none: %d %s", resp.StatusCode, readBody(resp))
	}
	before := through.Load()
	if code := generate(); code != http.StatusOK || through.Load() != before {
		t.Fatalf("direct after unbind: %d proxy %d -> %d", code, before, through.Load())
	}

	// 只用绑定了代理的账号：唯一的账号没绑代理，请求没有账号可用。
	if resp := doReq(t, srv, http.MethodGet, "/api/admin/settings/scheduling", nil, ""); resp.StatusCode == http.StatusOK {
		var sched struct {
			Settings map[string]any `json:"settings"`
		}
		decode(t, resp, &sched)
		sched.Settings["require_proxy"] = true
		b, _ := json.Marshal(sched.Settings)
		if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/scheduling", nil, string(b)); resp.StatusCode != http.StatusOK {
			t.Fatalf("require proxy: %d %s", resp.StatusCode, readBody(resp))
		}
	}
	if code := generate(); code == http.StatusOK {
		t.Fatal("unbound account should not be used when proxy is required")
	}
}

// 代理连不上：请求失败、不回落直连，账号暂停调度，尝试记录为代理故障。
func TestProxyDownPausesAccount(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ctx := context.Background()
	id := postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-1"}`).Account.ID

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	l.Close()
	p := &store.Proxy{Name: "dead", URL: dead}
	if err := st.CreateProxy(ctx, svc.enc, p); err != nil {
		t.Fatal(err)
	}
	body := `{"proxy_id":` + strconv.FormatInt(p.ID, 10) + `}`
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/accounts/"+strconv.FormatInt(id, 10)+"/proxy", nil, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("bind: %d %s", resp.StatusCode, readBody(resp))
	}
	svc.relay.InvalidateToken(id) // 下次请求要经代理刷新令牌

	resp := doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"k"}`)
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &key)
	resp = doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent",
		map[string]string{"x-goog-api-key": key.Secret}, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	readBody(resp)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("request through dead proxy succeeded")
	}
	logs := waitUsage(t, st, 1)
	var d store.UsageDetail
	_ = json.Unmarshal(logs[0].Detail, &d)
	if len(d.Attempts) == 0 || d.Attempts[0].Result != "proxy" || d.Error == nil || d.Error.Kind != "proxy" {
		t.Fatalf("usage detail: %+v", d)
	}
	if paused := svc.relay.Paused(); paused[id].IsZero() {
		t.Fatal("account not paused after proxy failure")
	}
}

// testEndpoints 把测试用的三个地址换成本地服务：ipinfo（geoStatus 不是 200 时失败）、Cloudflare trace、generate_204。
func testEndpoints(t *testing.T, geoStatus int) {
	t.Helper()
	geo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if geoStatus != http.StatusOK {
			w.WriteHeader(geoStatus)
			return
		}
		_, _ = w.Write([]byte(`{"ip":"203.0.113.9","city":"Tokyo","region":"Tokyo","country":"JP","org":"AS64500 Example"}`))
	}))
	trace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fl=1\nip=203.0.113.10\nloc=US\n"))
	}))
	ping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(func() { geo.Close(); trace.Close(); ping.Close() })
	oldGeo, oldTrace, oldPing := egressGeoURL, egressTraceURL, latencyURL
	egressGeoURL, egressTraceURL, latencyURL = geo.URL, trace.URL, ping.URL
	t.Cleanup(func() { egressGeoURL, egressTraceURL, latencyURL = oldGeo, oldTrace, oldPing })
}

type testCheckView struct {
	EgressIP      string `json:"egress_ip"`
	EgressCountry string `json:"egress_country"`
	EgressCity    string `json:"egress_city"`
	EgressOrg     string `json:"egress_org"`
	ConnectMs     int64  `json:"connect_ms"`
	LatencyMs     int64  `json:"latency_ms"`
	CheckError    string `json:"check_error"`
}

func checkOneProxy(t *testing.T, geoStatus int) (testCheckView, int64) {
	t.Helper()
	testEndpoints(t, geoStatus)
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	proxy, through := testForwardProxy(t)
	body, _ := json.Marshal(map[string]string{"text": proxy.URL})
	readBody(doReq(t, srv, http.MethodPost, "/api/admin/proxies", nil, string(body)))
	resp := doReq(t, srv, http.MethodPost, "/api/admin/proxies/check", nil, `{"ids":[]}`)
	var out struct {
		Proxies []testCheckView `json:"proxies"`
	}
	decode(t, resp, &out)
	if len(out.Proxies) != 1 {
		t.Fatalf("proxies: %+v", out.Proxies)
	}
	return out.Proxies[0], through.Load()
}

// 测试代理：经代理取 ipinfo 的出口信息，再测到代理和到 Google 的延迟（经代理请求两次 generate_204）。
func TestTestProxy(t *testing.T) {
	v, through := checkOneProxy(t, http.StatusOK)
	if v.EgressIP != "203.0.113.9" || v.EgressCountry != "JP" || v.EgressCity != "Tokyo" || v.EgressOrg != "AS64500 Example" ||
		v.ConnectMs <= 0 || v.LatencyMs <= 0 || v.CheckError != "" || through != 3 {
		t.Fatalf("test: %+v via %d", v, through)
	}
}

// ipinfo 取不到时退回 Cloudflare trace，只有 IP 和国家。
func TestTestProxyFallsBackToTrace(t *testing.T) {
	v, _ := checkOneProxy(t, http.StatusTooManyRequests)
	if v.EgressIP != "203.0.113.10" || v.EgressCountry != "US" || v.EgressCity != "" || v.ConnectMs <= 0 || v.LatencyMs <= 0 || v.CheckError != "" {
		t.Fatalf("fallback: %+v", v)
	}
}

// 测延迟：第二次请求复用第一次的连接（代理只收到一条新连接），所以到 Google 不含建连；
// 代理连得上、经代理请求失败时，到代理的耗时仍然记下。
func TestLatency(t *testing.T) {
	testEndpoints(t, http.StatusOK)
	var requests, conns atomic.Int64
	proxy := httptest.NewUnstartedServer(forwardHandler(&requests))
	proxy.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	proxy.Start()
	t.Cleanup(proxy.Close)

	connect, rtt, err := latency(context.Background(), proxy.URL)
	if err != nil || connect <= 0 || rtt <= 0 || requests.Load() != 2 || conns.Load() != 1 {
		t.Fatalf("latency: connect=%d rtt=%d err=%v requests=%d conns=%d", connect, rtt, err, requests.Load(), conns.Load())
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	latencyURL = "http://" + l.Addr().String() // testEndpoints 的 Cleanup 会换回来
	l.Close()
	connect, rtt, err = latency(context.Background(), proxy.URL)
	if err == nil || connect <= 0 || rtt != 0 {
		t.Fatalf("google down: connect=%d rtt=%d err=%v", connect, rtt, err)
	}
}

// 停用代理：自动分配不用它，不能绑到它上面，绑定它的账号暂停调度（不改成直连）；启用后恢复。
func TestDisableProxy(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	proxy, through := testForwardProxy(t)
	body, _ := json.Marshal(map[string]string{"text": proxy.URL})
	readBody(doReq(t, srv, http.MethodPost, "/api/admin/proxies", nil, string(body)))
	list, _ := listProxies(t, srv)
	ids := `{"ids":[` + strconv.FormatInt(list[0].ID, 10) + `]}`
	id := postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-1","proxy":{"mode":"auto"}}`).Account.ID

	if resp := doReq(t, srv, http.MethodPost, "/api/admin/proxies/disable", nil, ids); resp.StatusCode != http.StatusOK {
		t.Fatalf("disable: %d %s", resp.StatusCode, readBody(resp))
	}
	a, err := st.GetAccount(context.Background(), svc.enc, id)
	if err != nil || !a.ProxyDisabled {
		t.Fatalf("account proxy disabled: %v %+v", err, a)
	}
	resp := doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"k"}`)
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &key)
	generate := func() int {
		resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent",
			map[string]string{"x-goog-api-key": key.Secret}, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		readBody(resp)
		return resp.StatusCode
	}
	before := through.Load()
	if code := generate(); code == http.StatusOK || through.Load() != before {
		t.Fatalf("account with disabled proxy used: %d (proxy %d -> %d)", code, before, through.Load())
	}
	bind := `{"ids":[` + strconv.FormatInt(id, 10) + `],"mode":"proxy","proxy_id":` + strconv.FormatInt(list[0].ID, 10) + `}`
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/assign-proxy", nil, bind); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bind to disabled proxy: %d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/proxies/enable", nil, ids); resp.StatusCode != http.StatusOK {
		t.Fatalf("enable: %d", resp.StatusCode)
	}
	if code := generate(); code != http.StatusOK || through.Load() == before {
		t.Fatalf("after enable: %d", code)
	}
}

// 不带协议的几种常见写法都能认出来，协议按选的来。
func TestNormalizeProxy(t *testing.T) {
	cases := []struct{ in, scheme, want string }{
		{"socks5h://u:p@1.2.3.4:1080", "http", "socks5h://u:p@1.2.3.4:1080"},
		{"user:pass@1.2.3.4:8080", "socks5h", "socks5h://user:pass@1.2.3.4:8080"},
		{"1.2.3.4:8080@user:pass", "http", "http://user:pass@1.2.3.4:8080"},
		{"1.2.3.4:8080:user:pass", "http", "http://user:pass@1.2.3.4:8080"},
		{"user:pass:proxy.example.com:1080", "socks5", "socks5://user:pass@proxy.example.com:1080"},
		{"1.2.3.4:8080", "https", "https://1.2.3.4:8080"},
		{"user@1.2.3.4:8080", "http", "http://user@1.2.3.4:8080"},
		{"user:p@ss@1.2.3.4:8080", "http", "http://user:p%40ss@1.2.3.4:8080"},
		{"u:p@[2001:db8::1]:1080", "socks5", "socks5://u:p@[2001:db8::1]:1080"},
	}
	for _, c := range cases {
		got, err := normalizeProxy(c.in, c.scheme)
		if err != nil || got != c.want {
			t.Errorf("normalizeProxy(%q, %q) = %q, %v; want %q", c.in, c.scheme, got, err, c.want)
		}
	}
	for _, bad := range []string{"1.2.3", "user:pass@host", "a:b:c", "1.2.3.4:99999", "user:pass:host:port"} {
		if got, err := normalizeProxy(bad, "http"); err == nil {
			t.Errorf("normalizeProxy(%q) = %q, want error", bad, got)
		}
	}
}
