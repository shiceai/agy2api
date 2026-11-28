package antigravity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDeviceUserAgent(t *testing.T) {
	if DefaultUserAgent() != userAgentFor(currentBuilds()[0]) {
		t.Fatalf("DefaultUserAgent 应是最新核验构建：%s", DefaultUserAgent())
	}
	allowed := map[string]bool{}
	for _, b := range currentBuilds() {
		allowed[userAgentFor(b)] = true
	}

	if DeviceUserAgent("A@Example.com ") != DeviceUserAgent("a@example.com") {
		t.Fatal("同一账号（大小写、空白不同）应得到同一画像")
	}
	counts := map[string]int{}
	for i := 0; i < 2000; i++ {
		ua := DeviceUserAgent(fmt.Sprintf("user%d@gmail.com", i))
		if !allowed[ua] {
			t.Fatalf("只能用核验过的构建，实际 %s", ua)
		}
		counts[ua]++
	}
	// 表顶权重 60，留足随机波动的余量。
	if latest := counts[DefaultUserAgent()]; latest < 1050 || latest > 1350 {
		t.Fatalf("最新构建应占约 60%%，实际 %d/2000：%v", latest, counts)
	}
}

// 优先级：统一 UA > 账号保存的画像（没被淘汰的）> 按邮箱派生 > 最新核验构建。保存的版本比活动表里最旧的一档还旧时
// 已被淘汰，改按邮箱派生；比活动表还新的保持不动（只升不降）；不是派生形态的原样用。
func TestUserAgentFor(t *testing.T) {
	defer restoreBuilds(t)
	if err := SetActiveBuilds([]Build{{"2.35.2", "1029311855"}, {"2.34.0", "1026637980"}, {"2.33.1", "1026001617"}}); err != nil {
		t.Fatal(err)
	}
	const (
		stored  = "antigravity/hub/2.33.1 (aidev_client; os_type=linux; arch=amd64; cl=1026001617)"
		retired = "antigravity/hub/2.31.0 (aidev_client; os_type=linux; arch=amd64; cl=1017595969)"
		newer   = "antigravity/hub/2.40.0 (aidev_client; os_type=linux; arch=amd64; cl=2000000000)"
		other   = "custom-client/1.0"
	)
	c := NewClient(Options{})
	cases := []struct{ stored, key, want string }{
		{"", "", DefaultUserAgent()},
		{"", "a@example.com", DeviceUserAgent("a@example.com")},
		{stored, "a@example.com", stored},
		{retired, "a@example.com", DeviceUserAgent("a@example.com")},
		{retired, "", DefaultUserAgent()},
		{newer, "a@example.com", newer},
		{other, "a@example.com", other},
	}
	for _, tc := range cases {
		if got := c.UserAgentFor(tc.stored, tc.key); got != tc.want {
			t.Fatalf("UserAgentFor(%q, %q) = %q，期望 %q", tc.stored, tc.key, got, tc.want)
		}
	}
	// 官方发新版、活动表前进后，原来还在表里的 2.33.1 也被淘汰，换到当前的构建。
	if err := SetActiveBuilds([]Build{{"2.37.0", "1032000000"}, {"2.36.1", "1031000000"}, {"2.35.2", "1029311855"}}); err != nil {
		t.Fatal(err)
	}
	if got := c.UserAgentFor(stored, "a@example.com"); got != DeviceUserAgent("a@example.com") || !strings.Contains(got, "antigravity/hub/2.3") || strings.Contains(got, "2.33.1") {
		t.Fatalf("活动表前进后应换到当前构建，实际 %q", got)
	}
	c = NewClient(Options{UserAgent: "antigravity/hub/custom"})
	if c.UserAgentFor(stored, "a@example.com") != "antigravity/hub/custom" || c.UserAgentFor("", "") != "antigravity/hub/custom" {
		t.Fatal("配置了统一 UA 时所有账号都用它")
	}
}

// 同步时拿到邮箱之前用默认 UA，之后的 Cloud Code 请求用该账号自己的画像。
func TestSyncUsesAccountUserAgent(t *testing.T) {
	var mu sync.Mutex
	uas := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		uas[r.URL.Path] = r.Header.Get("User-Agent")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3600}`))
		case r.URL.Path == "/userinfo":
			_, _ = w.Write([]byte(`{"email":"someone@example.com"}`))
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"p"}`))
		default:
			_, _ = w.Write([]byte(`{"models":{}}`))
		}
	}))
	defer srv.Close()

	c := NewClient(Options{TokenURL: srv.URL + "/token", UserInfoURL: srv.URL + "/userinfo", UpstreamBases: []string{srv.URL}})
	res, err := c.Sync(context.Background(), Credential{RefreshToken: "rt"})
	if err != nil {
		t.Fatal(err)
	}
	want := DeviceUserAgent("someone@example.com")
	if res.Credential.UserAgent != want {
		t.Fatalf("新账号同步后应得到派生画像，实际 %q", res.Credential.UserAgent)
	}
	if uas["/userinfo"] != DefaultUserAgent() {
		t.Fatalf("拿邮箱前用默认 UA，实际 %q", uas["/userinfo"])
	}
	for _, path := range []string{"/v1internal:loadCodeAssist", "/v1internal:fetchAvailableModels"} {
		if uas[path] != want {
			t.Fatalf("%s 应用账号画像 %q，实际 %q", path, want, uas[path])
		}
	}
}

// 已有画像的账号同步时保持原画像，统一 UA 不会写进账号画像。
func TestSyncKeepsStoredUserAgent(t *testing.T) {
	const stored = "antigravity/hub/2.31.0 (aidev_client; os_type=linux; arch=amd64; cl=1017595969)"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			_, _ = w.Write([]byte(`{"access_token":"at","expires_in":3600}`))
		case r.URL.Path == "/userinfo":
			_, _ = w.Write([]byte(`{"email":"someone@example.com"}`))
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"p"}`))
		default:
			_, _ = w.Write([]byte(`{"models":{}}`))
		}
	}))
	defer srv.Close()
	for _, override := range []string{"", "antigravity/hub/custom"} {
		c := NewClient(Options{TokenURL: srv.URL + "/token", UserInfoURL: srv.URL + "/userinfo", UpstreamBases: []string{srv.URL}, UserAgent: override})
		res, err := c.Sync(context.Background(), Credential{RefreshToken: "rt", UserAgent: stored})
		if err != nil {
			t.Fatal(err)
		}
		if res.Credential.UserAgent != stored {
			t.Fatalf("统一 UA=%q 时应保持账号原画像，实际 %q", override, res.Credential.UserAgent)
		}
	}
}
