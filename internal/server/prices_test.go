package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

// waitUsage 等用量记录异步落库，返回按 id 倒序的前 n 条。
func waitUsage(t *testing.T, st *store.Store, n int) []*store.UsageLog {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		logs, _, err := st.UsageLogs(context.Background(), store.UsageFilter{}, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(logs) >= n {
			return logs
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d usage records, got %d", n, len(logs))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func usageDetailCost(t *testing.T, l *store.UsageLog) *store.UsageCost {
	t.Helper()
	var d store.UsageDetail
	if err := json.Unmarshal(l.Detail, &d); err != nil {
		t.Fatal(err)
	}
	return d.Cost
}

// 价格接口：列表按号池里的模型家族分组；手动设价后补算之前的用量，之后的请求记录时就算好；删除恢复无价格。
func TestPricesEndpoints(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	postImport(t, srv, "/api/admin/accounts/import", `{"refresh_token":"rt-1"}`)
	resp := doReq(t, srv, http.MethodPost, "/api/admin/keys", nil, `{"name":"k"}`)
	var key struct {
		Secret string `json:"secret"`
	}
	decode(t, resp, &key)
	generate := func() {
		resp := doReq(t, srv, http.MethodPost, "/v1beta/models/gemini-3-pro-high:generateContent",
			map[string]string{"x-goog-api-key": key.Secret}, `{"contents":[{"role":"user","parts":[{"text":"hello world"}]}]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("generate: %d %s", resp.StatusCode, readBody(resp))
		}
		readBody(resp)
	}
	type view struct {
		AutoSync bool `json:"auto_sync"`
		Models   []struct {
			Model string   `json:"model"`
			IDs   []string `json:"ids"`
			Price *struct {
				Source string `json:"source"`
			} `json:"price"`
		} `json:"models"`
	}
	getView := func(resp *http.Response) view {
		t.Helper()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("prices: %d %s", resp.StatusCode, readBody(resp))
		}
		var v view
		decode(t, resp, &v)
		return v
	}
	row := func(v view, model string) (ids []string, source string, ok bool) {
		for _, m := range v.Models {
			if m.Model == model {
				if m.Price != nil {
					source = m.Price.Source
				}
				return m.IDs, source, true
			}
		}
		return nil, "", false
	}

	v := getView(doReq(t, srv, http.MethodGet, "/api/admin/prices", nil, ""))
	if ids, source, ok := row(v, "gemini-3-pro"); v.AutoSync || !ok || source != "" || len(ids) < 2 {
		t.Fatalf("initial view: %+v", v)
	}

	// 没有价格时请求不算费用。
	generate()
	if logs := waitUsage(t, st, 1); logs[0].CostNanos != nil {
		t.Fatalf("unpriced request got cost %d", *logs[0].CostNanos)
	}

	// 设价后补算：2 个输入 token × $1/M + 3 个输出 × $2/M = $0.000008。
	v = getView(doReq(t, srv, http.MethodPut, "/api/admin/prices/gemini-3-pro", nil, `{"input":1,"output":2}`))
	if _, source, _ := row(v, "gemini-3-pro"); source != "manual" {
		t.Fatalf("after set: %+v", v)
	}
	logs := waitUsage(t, st, 1)
	if c := logs[0].CostNanos; c == nil || *c != 8000 || !usageDetailCost(t, logs[0]).Backfilled {
		t.Fatalf("backfilled cost: %v", c)
	}

	// 之后的请求记录时就算好。
	generate()
	logs = waitUsage(t, st, 2)
	if c := logs[0].CostNanos; c == nil || *c != 8000 {
		t.Fatalf("recorded cost: %v", c)
	}
	if cost := usageDetailCost(t, logs[0]); cost == nil || cost.Backfilled || cost.Model != "gemini-3-pro" || cost.Input != 1_000_000 {
		t.Fatalf("recorded cost detail: %+v", cost)
	}

	if resp := doReq(t, srv, http.MethodPut, "/api/admin/prices/gemini-3-pro", nil, `{"input":-1,"output":2}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative price: %d", resp.StatusCode)
	}
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/prices/bad%20name", nil, `{"input":1,"output":2}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad model name: %d", resp.StatusCode)
	}
	v = getView(doReq(t, srv, http.MethodDelete, "/api/admin/prices/gemini-3-pro", nil, ""))
	if _, source, _ := row(v, "gemini-3-pro"); source != "" {
		t.Fatalf("after delete: %+v", v)
	}
	if resp := doReq(t, srv, http.MethodDelete, "/api/admin/prices/gemini-3-pro", nil, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again: %d", resp.StatusCode)
	}
}
