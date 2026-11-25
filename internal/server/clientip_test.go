package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shiceai/agy2api/internal/config"
	"github.com/shiceai/agy2api/internal/mockupstream"
)

// 客户端 IP 只认可信反代的转发头：X-Forwarded-For 从右往左跳过可信反代，第一个不可信的就是客户端。
func TestResolveClientIP(t *testing.T) {
	trusted, err := parsePrefixes(strings.Split(config.DefaultTrustedProxies, ","))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		remote string
		xff    []string
		realIP string
		want   string
	}{
		{"直连、没有转发头", "203.0.113.7:5000", nil, "", "203.0.113.7"},
		{"直连的不可信对端伪造转发头，不认", "203.0.113.7:5000", []string{"1.1.1.1"}, "2.2.2.2", "203.0.113.7"},
		{"本机反代", "127.0.0.1:40000", []string{"198.51.100.9"}, "", "198.51.100.9"},
		{"客户端在左边伪造，取反代追加的最右一跳", "127.0.0.1:40000", []string{"6.6.6.6, 198.51.100.9"}, "", "198.51.100.9"},
		{"经过两层内网反代", "10.0.0.2:40000", []string{"198.51.100.9, 10.0.0.5"}, "", "198.51.100.9"},
		{"多个 X-Forwarded-For 头合起来看", "10.0.0.2:40000", []string{"198.51.100.9", "10.0.0.5"}, "", "198.51.100.9"},
		{"全是可信地址时取最左边", "127.0.0.1:40000", []string{"10.0.0.3, 10.0.0.5"}, "", "10.0.0.3"},
		{"写坏的一跳：停在最后一个可信反代", "127.0.0.1:40000", []string{"garbage, 10.0.0.5"}, "", "10.0.0.5"},
		{"带端口的一跳", "127.0.0.1:40000", []string{"198.51.100.9:1234"}, "", "198.51.100.9"},
		{"没有 X-Forwarded-For 时用 X-Real-IP", "127.0.0.1:40000", nil, "198.51.100.9", "198.51.100.9"},
		{"IPv6 回环反代", "[::1]:40000", []string{"2001:db8::1"}, "", "2001:db8::1"},
		{"IPv4 映射的 IPv6 对端", "[::ffff:127.0.0.1]:40000", []string{"198.51.100.9"}, "", "198.51.100.9"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.remote
		for _, v := range tc.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if tc.realIP != "" {
			r.Header.Set("X-Real-IP", tc.realIP)
		}
		if got, ok := resolveClientIP(r, trusted); !ok || got.String() != tc.want {
			t.Errorf("%s：期望 %s，实际 %v %v", tc.name, tc.want, got, ok)
		}
	}

	// none：谁都不信，转发头一律不看。
	none, err := parsePrefixes([]string{"none"})
	if err != nil || len(none) != 0 {
		t.Fatalf("none 应解析成空列表：%v %v", none, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr, r.Header["X-Forwarded-For"] = "127.0.0.1:1", []string{"198.51.100.9"}
	if got, _ := resolveClientIP(r, none); got.String() != "127.0.0.1" {
		t.Fatalf("不信任任何反代时应取对端，实际 %v", got)
	}
}

func TestParsePrefixes(t *testing.T) {
	got, err := parsePrefixes([]string{"10.1.2.3", "192.168.1.77/24", "2001:db8::/32", "::ffff:10.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.1.2.3/32", "192.168.1.0/24", "2001:db8::/32", "10.9.9.9/32"}
	for i, p := range got {
		if p.String() != want[i] {
			t.Fatalf("第 %d 项期望 %s，实际 %s", i, want[i], p)
		}
	}
	if _, err := parsePrefixes([]string{"10.0.0.0/33"}); err == nil {
		t.Fatal("写错的 CIDR 应报错")
	}
	if _, err := parsePrefixes([]string{"example.com"}); err == nil {
		t.Fatal("域名应报错")
	}
}

// 管理端白名单：白名单外的地址访问管理接口和页面返回 403（口令校验之前），网关接口和健康检查不受影响；
// 客户端 IP 按可信反代的转发头取。
func TestAdminAllowlist(t *testing.T) {
	svc, _ := newTestServiceWith(t, mockupstream.Handler(), func(c *config.Config) {
		c.TrustedProxies = []string{"127.0.0.1"}
		c.AdminAllow = []string{"198.51.100.0/24"}
	})
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	from := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }
	cases := []struct {
		path    string
		headers map[string]string
		want    int
	}{
		{"/api/admin/session", from("198.51.100.9"), http.StatusOK},
		{"/api/admin/session", from("203.0.113.7"), http.StatusForbidden},
		{"/api/admin/session", nil, http.StatusForbidden}, // 本机直连，不在白名单里
		{"/", from("203.0.113.7"), http.StatusForbidden},
		{"/v1beta/models", from("203.0.113.7"), http.StatusUnauthorized}, // 网关接口照常走 API Key 鉴权
		{"/v1beta/nope", from("203.0.113.7"), http.StatusNotFound},
		{"/healthz", from("203.0.113.7"), http.StatusOK},
	}
	for _, tc := range cases {
		resp := doReq(t, srv, http.MethodGet, tc.path, tc.headers, "")
		body := readBody(resp)
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %v：期望 %d，实际 %d %s", tc.path, tc.headers, tc.want, resp.StatusCode, body)
		}
		if tc.want == http.StatusForbidden && !strings.Contains(body, "not allowed from this address") {
			t.Fatalf("%s：403 的说明不对：%s", tc.path, body)
		}
	}
}
