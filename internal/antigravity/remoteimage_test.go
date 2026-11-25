package antigravity

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

var pngBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2001:4860:4860::8888", true},
		{"127.0.0.1", false},          // 环回
		{"10.0.0.1", false},           // 私网
		{"192.168.1.1", false},        // 私网
		{"172.16.0.1", false},         // 私网
		{"169.254.169.254", false},    // 链路本地（云元数据）
		{"100.64.0.1", false},         // CGNAT
		{"198.18.0.1", false},         // 基准测试
		{"192.0.2.1", false},          // 文档
		{"224.0.0.1", false},          // 组播
		{"::1", false},                // IPv6 环回
		{"fd00::1", false},            // ULA
		{"fe80::1", false},            // 链路本地
		{"2002:0808:0808::1", false},  // 6to4
		{"2001:0:0:0:0:0:0:1", false}, // Teredo
		{"::ffff:127.0.0.1", false},   // IPv4-mapped 环回
	}
	for _, c := range cases {
		if got := isPublicIP(net.ParseIP(c.ip)); got != c.want {
			t.Errorf("isPublicIP(%s)=%v，期望 %v", c.ip, got, c.want)
		}
	}
}

// safeDial 在解析到非公网 IP 或非默认端口时直接拒绝（不发起连接）。
func TestSafeDialRejects(t *testing.T) {
	d := &net.Dialer{}
	if _, err := safeDial(context.Background(), d, "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("环回地址应被拒绝")
	}
	if _, err := safeDial(context.Background(), d, "tcp", "example.com:8080"); err == nil {
		t.Fatal("非默认端口应被拒绝")
	}
}

func TestCollectImageTargets(t *testing.T) {
	body := map[string]any{"contents": []any{
		map[string]any{"parts": []any{
			map[string]any{"text": "hi"},
			map[string]any{"fileData": map[string]any{"mimeType": "image/png", "fileUri": "https://example.com/a.png"}},
			map[string]any{"fileData": map[string]any{"mimeType": "image/png", "fileUri": "https://generativelanguage.googleapis.com/v1beta/files/abc"}}, // 上游托管，跳过
			map[string]any{"fileData": map[string]any{"mimeType": "application/pdf", "fileUri": "https://example.com/doc.pdf"}},                          // 非图片，跳过
			map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "x"}},                                                           // 已内联，跳过
		}},
	}}
	targets := collectImageTargets(body)
	if len(targets) != 1 || targets[0].url != "https://example.com/a.png" {
		t.Fatalf("应只找到 1 个待内联图片，实际 %+v", targets)
	}
}

func TestInlineRemoteImagesEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes)
	}))
	defer srv.Close()
	// 替换客户端，绕过 SSRF 限制指向本地假服务器。
	old := imageClientFactory
	imageClientFactory = func() *http.Client { return srv.Client() }
	defer func() { imageClientFactory = old }()

	body := map[string]any{"contents": []any{
		map[string]any{"parts": []any{
			map[string]any{"text": "look"},
			map[string]any{"fileData": map[string]any{"mimeType": "image/png", "fileUri": srv.URL + "/a.png"}},
		}},
	}}
	n, err := InlineRemoteImages(context.Background(), body)
	if err != nil || n != 1 {
		t.Fatalf("应内联 1 张，实际 n=%d err=%v", n, err)
	}
	part := body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[1].(map[string]any)
	if _, ok := part["fileData"]; ok {
		t.Fatal("fileData 应被替换掉")
	}
	inline, ok := part["inlineData"].(map[string]any)
	if !ok || inline["mimeType"] != "image/png" {
		t.Fatalf("应替换成 image/png 的 inlineData，实际 %v", part)
	}
	if inline["data"] != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("内联数据 base64 不对")
	}
	// 文本 part 不动。
	if body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"] != "look" {
		t.Fatal("文本 part 不应改动")
	}
}

func TestSniffImageMime(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0}
	if m, ok := sniffImageMime(jpeg, ""); !ok || m != "image/jpeg" {
		t.Fatalf("JPEG 识别失败：%q %v", m, ok)
	}
	if _, ok := sniffImageMime([]byte("not an image"), "text/plain"); ok {
		t.Fatal("非图片应识别失败")
	}
}
