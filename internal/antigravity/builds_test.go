package antigravity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// restoreBuilds 把活动表恢复成内置 baseline，供修改活动表的测试 defer 调用。
func restoreBuilds(t *testing.T) {
	t.Helper()
	weighted, err := weightedFromJSON(builtinUABuildsRaw)
	if err != nil {
		t.Fatalf("内置 ua-builds.json 应有效：%v", err)
	}
	activeBuilds.Store(&weighted)
}

func TestParseUABuildsValid(t *testing.T) {
	builds, err := parseUABuilds([]byte(`{"builds":[{"version":"2.35.2","cl":"1029311855"},{"version":"2.34.0","cl":"1026637980"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 2 || builds[0].Version != "2.35.2" || builds[0].CL != "1029311855" {
		t.Fatalf("解析结果不对：%+v", builds)
	}
}

func TestParseUABuildsInvalid(t *testing.T) {
	cases := map[string]string{
		"空列表":    `{"builds":[]}`,
		"版本非数字":  `{"builds":[{"version":"2.x.0","cl":"1"}]}`,
		"CL 非数字": `{"builds":[{"version":"2.35.2","cl":"10a"}]}`,
		"CL 为空":  `{"builds":[{"version":"2.35.2","cl":""}]}`,
		"版本重复":   `{"builds":[{"version":"2.35.2","cl":"1"},{"version":"2.35.2","cl":"2"}]}`,
		"坏 JSON": `{`,
	}
	for name, raw := range cases {
		if _, err := parseUABuilds([]byte(raw)); err == nil {
			t.Fatalf("%s 应报错", name)
		}
	}
}

// 多于 len(buildWeights) 条时只取最新几条按固定权重赋权，表顶随之更新。
func TestSetActiveBuildsReplacesAndWindows(t *testing.T) {
	defer restoreBuilds(t)
	err := SetActiveBuilds([]Build{
		{"2.40.0", "2000000000"},
		{"2.39.0", "1900000000"},
		{"2.38.0", "1800000000"},
		{"2.37.0", "1700000000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := currentBuilds()
	if len(got) != len(buildWeights) {
		t.Fatalf("应只保留最新 %d 条，实际 %d", len(buildWeights), len(got))
	}
	if LatestShippedHubVersion() != "2.40.0" {
		t.Fatalf("表顶应是 2.40.0，实际 %s", LatestShippedHubVersion())
	}
	if got[0].weight != 60 || got[1].weight != 28 || got[2].weight != 12 {
		t.Fatalf("权重不对：%+v", got)
	}
	ua := DefaultUserAgent()
	if !strings.Contains(ua, "antigravity/hub/2.40.0") || !strings.Contains(ua, "cl=2000000000") {
		t.Fatalf("DefaultUserAgent 应反映刷新后的表顶：%s", ua)
	}
}

// 内容无效时整表拒绝，保留原表。
func TestSetActiveBuildsRejectKeepsPrevious(t *testing.T) {
	defer restoreBuilds(t)
	before := LatestShippedHubVersion()
	if err := SetActiveBuilds([]Build{{"2.50.0", "bad"}}); err == nil {
		t.Fatal("非法 CL 应被拒绝")
	}
	if err := SetActiveBuilds(nil); err == nil {
		t.Fatal("空表应被拒绝")
	}
	if LatestShippedHubVersion() != before {
		t.Fatalf("拒绝后应保留原表顶 %s，实际 %s", before, LatestShippedHubVersion())
	}
}

func TestFetchRemoteBuilds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"builds":[{"version":"2.36.0","cl":"1050000000"}]}`))
	}))
	defer srv.Close()
	builds, err := FetchRemoteBuilds(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 1 || builds[0].Version != "2.36.0" || builds[0].CL != "1050000000" {
		t.Fatalf("拉取结果不对：%+v", builds)
	}
}

func TestFetchRemoteBuildsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := FetchRemoteBuilds(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("404 应报错")
	}
}
