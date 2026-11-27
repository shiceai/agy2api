package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/mockupstream"
	"github.com/shiceai/agy2api/internal/store"
)

// 保存失败请求的正文：默认关；开启后上游返回错误的请求在用量详情里有摘要和"已保存"，能下载发给上游的请求体；
// 清空后下载 404。没开时只有摘要。
func TestRequestCaptureAdmin(t *testing.T) {
	mock := mockupstream.Handler()
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "enerateContent") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad request from upstream","status":"INVALID_ARGUMENT"}}`))
			return
		}
		mock.ServeHTTP(w, r)
	})
	svc, st := newTestServiceWith(t, upstream)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	var view requestCaptureView
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/request-bodies", nil, ""), &view)
	if view.Settings.Enabled || view.Count != 0 || view.MaxCount <= 0 || view.PerMinute <= 0 {
		t.Fatalf("默认应关闭、没有存的：%+v", view)
	}
	if resp := doReq(t, srv, http.MethodPost, "/api/admin/accounts/import", nil, `{"refresh_token":"mock-refresh-token"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("import account: %d %s", resp.StatusCode, readBody(resp))
	}
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"capture"}`), &key)
	generate := func(text string) {
		t.Helper()
		resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent",
			map[string]string{"x-goog-api-key": key.Secret}, `{"contents":[{"role":"user","parts":[{"text":"`+text+`"}]}]}`)
		if body := readBody(resp); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("上游 400 应原样返回 400：%d %s", resp.StatusCode, body)
		}
	}

	// 没开：有摘要和上游原文，不标保存、不能下载。
	generate("first")
	logs := waitUsage(t, st, 1)
	var d store.UsageDetail
	_ = json.Unmarshal(logs[0].Detail, &d)
	if d.Request == nil || d.Request.Turns != 1 || d.Error == nil || !strings.Contains(d.Error.Raw, "bad request from upstream") || d.RequestBody != "" {
		t.Fatalf("没开时应只有摘要和上游原文：%+v %+v %q", d.Request, d.Error, d.RequestBody)
	}
	firstID := strconv.FormatInt(logs[0].ID, 10)
	if resp := doReq(t, srv, http.MethodGet, "/api/admin/usage/logs/"+firstID+"/request-body", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("没存时下载应 404：%d", resp.StatusCode)
	}

	// 开启：存下请求体，能下载。
	decode(t, doReq(t, srv, http.MethodPut, "/api/admin/usage/request-bodies", nil, `{"enabled":true}`), &view)
	if !view.Settings.Enabled {
		t.Fatalf("应已开启：%+v", view)
	}
	generate("second-secret-word")
	logs = waitUsage(t, st, 2)
	_ = json.Unmarshal(logs[0].Detail, &d)
	if d.RequestBody != store.RequestBodySaved {
		t.Fatalf("开启后应标记已保存：%q", d.RequestBody)
	}
	path := "/api/admin/usage/logs/" + strconv.FormatInt(logs[0].ID, 10) + "/request-body"
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp := doReq(t, srv, http.MethodGet, path, nil, "")
		body := readBody(resp)
		if resp.StatusCode == http.StatusOK {
			if !strings.Contains(body, "second-secret-word") || !strings.Contains(body, `"request"`) ||
				!strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
				t.Fatalf("下载的应是发给上游的请求体：%s %.300s", resp.Header.Get("Content-Disposition"), body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("应能下载已保存的正文：%d %s", resp.StatusCode, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/request-bodies", nil, ""), &view)
	if view.Count != 1 || view.Bytes <= 0 {
		t.Fatalf("应存了 1 条：%+v", view)
	}

	// 清空：开关不变，下载 404。
	decode(t, doReq(t, srv, http.MethodDelete, "/api/admin/usage/request-bodies", nil, ""), &view)
	if view.Count != 0 || !view.Settings.Enabled {
		t.Fatalf("清空后应没有存的、开关不变：%+v", view)
	}
	if resp := doReq(t, srv, http.MethodGet, path, nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("清空后下载应 404：%d", resp.StatusCode)
	}
}
