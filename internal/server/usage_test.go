package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

// 用量汇总和日志接口：经网关产生记录后按条件查询，非法参数返回 400。
func TestUsageEndpoints(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)

	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	for _, path := range []string{
		"/v1beta/models/gemini-3-pro-high:generateContent",
		"/v1beta/models/gemini-3-pro-high:streamGenerateContent?alt=sse",
		"/v1beta/models/gemini-3-pro:generateContent",
	} {
		readBody(doReq(t, srv, http.MethodPost, path, auth, body))
	}
	// 用量异步写入，关掉服务等它写完。
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	type logRecord struct {
		RequestID     string `json:"request_id"`
		RequestModel  string `json:"request_model"`
		ResolvedModel string `json:"resolved_model"`
		Reasoning     string `json:"reasoning"`
		AccountID     int64  `json:"account_id"`
		AccountEmail  string `json:"account_email"`
		APIKeyID      int64  `json:"api_key_id"`
		APIKeyName    string `json:"api_key_name"`
		Stream        bool   `json:"stream"`
		CachedTokens  *int64 `json:"cached_tokens"`
		FirstTokenMs  *int64 `json:"first_token_ms"`
	}
	var logs struct {
		Records []logRecord `json:"records"`
		Total   int64       `json:"total"`
	}

	// 全部记录：流式有首字延迟，非流式没有；家族名记录解析后的模型和推理强度。
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs", nil, ""), &logs)
	if logs.Total != 3 {
		t.Fatalf("应有 3 条记录：%+v", logs)
	}
	var streamRec, syncRec logRecord
	for _, r := range logs.Records {
		if r.Stream {
			streamRec = r
		} else if r.RequestModel == "gemini-3-pro" {
			syncRec = r
		}
	}
	if streamRec.FirstTokenMs == nil || *streamRec.FirstTokenMs < 0 || streamRec.CachedTokens == nil {
		t.Fatalf("流式记录应有首字延迟和缓存数：%+v", streamRec)
	}
	if syncRec.FirstTokenMs != nil || syncRec.ResolvedModel != "gemini-3-pro-low" || syncRec.Reasoning != "low" {
		t.Fatalf("家族名非流式记录应无首字延迟、记录解析后的模型和档位：%+v", syncRec)
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs?model=gemini-3-pro-high&stream=sync", nil, ""), &logs)
	if logs.Total != 1 || len(logs.Records) != 1 || logs.Records[0].Stream ||
		logs.Records[0].AccountEmail != "mock-user@example.com" || logs.Records[0].APIKeyName != "test" {
		t.Fatalf("按模型和类型筛选不对：%+v", logs)
	}
	reqID := logs.Records[0].RequestID
	path := fmt.Sprintf("/api/admin/usage/logs?request_id=%s&account_id=%d&api_key_id=%d&status=success",
		reqID, logs.Records[0].AccountID, logs.Records[0].APIKeyID)
	decode(t, doReq(t, srv, http.MethodGet, path, nil, ""), &logs)
	if logs.Total != 1 || logs.Records[0].RequestID != reqID {
		t.Fatalf("按请求 ID、账号、Key 筛选不对：%+v", logs)
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs?status=error", nil, ""), &logs)
	if logs.Total != 0 || len(logs.Records) != 0 {
		t.Fatalf("没有错误记录时应为空：%+v", logs)
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs?limit=2&offset=2", nil, ""), &logs)
	if logs.Total != 3 || len(logs.Records) != 1 {
		t.Fatalf("分页不对：%+v", logs)
	}

	for _, q := range []string{"status=bad", "stream=bad", "account_id=x", "since=x", "until=x"} {
		resp := doReq(t, srv, http.MethodGet, "/api/admin/usage/logs?"+q, nil, "")
		readBody(resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 应返回 400，实际 %d", q, resp.StatusCode)
		}
	}
}

// 概览：趋势固定 24 个连续小时、最后一个是当前小时；超出范围的记录不计入，各小时请求数之和等于近 24 小时请求数。
func TestOverviewHourly(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()

	now := time.Now().UnixMilli()
	if err := st.InsertUsage(context.Background(),
		&store.UsageRecord{RequestModel: "m", StatusCode: 200, TotalTokens: 7, CreatedAt: now},
		&store.UsageRecord{RequestModel: "m", StatusCode: 500, CreatedAt: now},
		&store.UsageRecord{RequestModel: "m", StatusCode: 200, TotalTokens: 9, CreatedAt: now - 30*3600_000},
	); err != nil {
		t.Fatal(err)
	}
	var ov struct {
		Requests24h int64                `json:"requests_24h"`
		Hourly      []*store.HourlyUsage `json:"hourly"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/overview", nil, ""), &ov)
	if len(ov.Hourly) != 24 {
		t.Fatalf("应有 24 个小时，实际 %d", len(ov.Hourly))
	}
	// 按起点找记录所在的小时，不假定它是最后一个：写入和查询之间可能刚好跨过整点。
	hour := now / 3600_000 * 3600_000
	var cur *store.HourlyUsage
	for _, h := range ov.Hourly {
		if h.Start == hour {
			cur = h
		}
	}
	if cur == nil || cur.Requests != 2 || cur.Errors != 1 || cur.TotalTokens != 7 {
		t.Fatalf("记录所在小时不对：%+v", cur)
	}
	if last := ov.Hourly[23].Start; last < hour || last > hour+3600_000 {
		t.Fatalf("最后一个应是当前小时，实际起点 %d，记录所在小时 %d", last, hour)
	}
	var sum int64
	for i, h := range ov.Hourly {
		if i > 0 && h.Start-ov.Hourly[i-1].Start != 3600_000 {
			t.Fatalf("小时不连续：%d → %d", ov.Hourly[i-1].Start, h.Start)
		}
		sum += h.Requests
	}
	if sum != 2 || ov.Requests24h != 2 {
		t.Fatalf("超出 24 小时的记录不应计入：趋势合计 %d，近 24 小时请求 %d", sum, ov.Requests24h)
	}
}

// 用量记客户端 IP 和 UA；单条详情接口返回详情，不存在的返回 404。
func TestUsageLogDetail(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	auth := importAndKey(t, srv)
	auth["User-Agent"] = "detail-test/2.0"
	readBody(doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent", auth,
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	var logs struct {
		Records []struct {
			ID        int64  `json:"id"`
			ClientIP  string `json:"client_ip"`
			UserAgent string `json:"user_agent"`
		} `json:"records"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/logs", nil, ""), &logs)
	if len(logs.Records) != 1 || logs.Records[0].ClientIP != "127.0.0.1" || logs.Records[0].UserAgent != "detail-test/2.0" {
		t.Fatalf("应记客户端 IP 和 UA：%+v", logs.Records)
	}
	var one struct {
		Record struct {
			ID     int64             `json:"id"`
			Detail store.UsageDetail `json:"detail"`
		} `json:"record"`
	}
	decode(t, doReq(t, srv, http.MethodGet, fmt.Sprintf("/api/admin/usage/logs/%d", logs.Records[0].ID), nil, ""), &one)
	if one.Record.ID != logs.Records[0].ID || len(one.Record.Detail.Attempts) != 1 || one.Record.Detail.Timings == nil {
		t.Fatalf("详情接口应返回完整详情：%+v", one.Record)
	}
	if resp := doReq(t, srv, http.MethodGet, "/api/admin/usage/logs/99999", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的记录应返回 404，实际 %d", resp.StatusCode)
	}
}
