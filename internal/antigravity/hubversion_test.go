package antigravity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHubVersionNewer(t *testing.T) {
	cases := []struct {
		latest, shipped string
		want            bool
	}{
		{"2.35.2", "2.34.0", true},
		{"2.35.2", "2.35.2", false},
		{"2.34.0", "2.35.2", false},
		{"2.35.10", "2.35.2", true}, // 逐段数字比较，不是字典序
		{"2.36", "2.35.9", true},
		{"", "2.35.2", false},       // 解析不了不误报
		{"2.35.x", "2.35.2", false}, // 非数字段
	}
	for _, c := range cases {
		if got := HubVersionNewer(c.latest, c.shipped); got != c.want {
			t.Errorf("HubVersionNewer(%q, %q) = %v，期望 %v", c.latest, c.shipped, got, c.want)
		}
	}
}

func TestFetchLatestHubVersion(t *testing.T) {
	const manifest = "version: 2.35.2\nfiles:\n  - url: https://example.invalid/Antigravity.zip\n    size: 1\npath: Antigravity.zip\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(manifest))
	}))
	defer srv.Close()

	v, err := fetchLatestHubVersionFrom(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("解析清单失败：%v", err)
	}
	if v != "2.35.2" {
		t.Fatalf("版本应为 2.35.2，实际 %q", v)
	}
}

func TestFetchLatestHubVersionMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("files:\n  - url: x\n"))
	}))
	defer srv.Close()
	if _, err := fetchLatestHubVersionFrom(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("没有 version 字段时应返回错误")
	}
}
