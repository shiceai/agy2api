package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/mockupstream"
)

// 被拒上下文拦截：默认开；两个账号都对一个请求回了不带原因的 429 后记下它，原样重试在本地返回 400、不再发上游；
// 管理端能看到、删除单条、全部删除。
func TestRejectedContextsAdmin(t *testing.T) {
	mock := mockupstream.Handler()
	var generates, users atomic.Int64
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟上游的 userinfo 固定一个邮箱：按导入顺序给不同邮箱，导入两个账号。
		if strings.HasSuffix(r.URL.Path, "/userinfo") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"email":"user%d@example.com","verified_email":true}`, users.Add(1))
			return
		}
		if strings.Contains(r.URL.Path, "enerateContent") {
			generates.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		mock.ServeHTTP(w, r)
	})
	svc, _ := newTestServiceWith(t, upstream)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	var view rejectedContextsView
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/rejected-contexts", nil, ""), &view)
	if !view.Settings.Enabled || view.Total != 0 || view.TTLDays != 7 || view.Max <= 0 {
		t.Fatalf("默认应开启、没有记录：%+v", view)
	}
	for _, rt := range []string{"rt-1", "rt-2"} {
		postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"`+rt+`"}`)
	}
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"rp-app"}`), &key)
	const body = `{"contents":[{"role":"user","parts":[{"text":"设定"}]},{"role":"model","parts":[{"text":"回复"}]},{"role":"user","parts":[{"text":"继续"}]}]}`
	generate := func() (int, string) {
		resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent", map[string]string{"x-goog-api-key": key.Secret}, body)
		return resp.StatusCode, readBody(resp)
	}

	if status, msg := generate(); status != http.StatusTooManyRequests {
		t.Fatalf("两个号都 429 时应判为请求被拒：%d %s", status, msg)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		decode(t, doReq(t, srv, http.MethodGet, "/api/admin/rejected-contexts", nil, ""), &view)
		if view.Total == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("应记下被拒的上下文：%+v", view)
		}
		time.Sleep(20 * time.Millisecond)
	}
	c := view.Contexts[0]
	if c.Turns != 3 || c.APIKeyName != "rp-app" || c.RequestID == "" || len(c.Fingerprint) != 32 {
		t.Fatalf("被拒上下文记录不对：%+v", c)
	}

	before := generates.Load()
	status, msg := generate()
	if status != http.StatusBadRequest || !strings.Contains(msg, "previously rejected") {
		t.Fatalf("原样重试应在本地返回 400：%d %s", status, msg)
	}
	if generates.Load() != before {
		t.Fatal("包含被拒上下文的请求不应发往上游")
	}

	if resp := doReq(t, srv, http.MethodDelete, "/api/admin/rejected-contexts/nope", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删除不存在的应 404：%d", resp.StatusCode)
	}
	decode(t, doReq(t, srv, http.MethodDelete, "/api/admin/rejected-contexts/"+c.Fingerprint, nil, ""), &view)
	if view.Total != 0 {
		t.Fatalf("删除后应没有记录：%+v", view)
	}
	decode(t, doReq(t, srv, http.MethodPut, "/api/admin/rejected-contexts", nil, `{"enabled":false}`), &view)
	if view.Settings.Enabled {
		t.Fatalf("应已关闭：%+v", view)
	}
	decode(t, doReq(t, srv, http.MethodDelete, "/api/admin/rejected-contexts", nil, ""), &view)
	if view.Total != 0 || view.Settings.Enabled {
		t.Fatalf("清空后应没有记录、开关不变：%+v", view)
	}
}
