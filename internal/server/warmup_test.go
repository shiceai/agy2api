package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// 新号预热的情况随账号列表和 /live 返回：预热关闭时没有；开启后刚导入的账号给出进度、压低后的上限和间隔。
func TestWarmupVisibleOnAccounts(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	importAndKey(t, srv)

	var list struct {
		Accounts []accountView `json:"accounts"`
	}
	var live struct {
		Warmup map[string]warmupStateView `json:"warmup"`
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/accounts", nil, ""), &list)
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/live", nil, ""), &live)
	if list.Accounts[0].Warmup != nil || len(live.Warmup) != 0 {
		t.Fatalf("预热关闭时不应有预热情况：%+v %+v", list.Accounts[0].Warmup, live.Warmup)
	}

	body := `{"enabled":true,"initial_concurrency":1,"successes":20,"age_minutes":120,"initial_interval_ms":1500,"min_interval_ms":0}`
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/settings/warmup", nil, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("开启预热应成功，实际 %d", resp.StatusCode)
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/accounts", nil, ""), &list)
	w := list.Accounts[0].Warmup
	if w == nil {
		t.Fatal("刚导入的账号应在预热中")
	}
	// 号龄几乎为 0、还没有成功：进度约 0，上限压到 1（正常 15），间隔约 1.5 秒。
	if w.Progress > 0.01 || w.Limit != 1 || w.Normal != 15 || w.IntervalMs < 1480 || w.IntervalMs > 1500 ||
		w.SuccessTarget != 20 || w.AgeTarget != 120 {
		t.Fatalf("预热情况不对：%+v", w)
	}

	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/live", nil, ""), &live)
	if lw, ok := live.Warmup[strconv.FormatInt(list.Accounts[0].ID, 10)]; !ok || lw.Limit != 1 {
		t.Fatalf("/live 应带上预热情况：%+v", live.Warmup)
	}
}
