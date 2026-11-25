package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

// 用量保留：默认开、90 天；天数要在 32～3650；手动清理只删超过保留期的记录，记下结果，Key 的总量用量不变。
func TestUsageRetention(t *testing.T) {
	svc, st := newTestService(t)
	srv := httptest.NewServer(svc.Router())
	defer srv.Close()
	ctx := context.Background()

	var v retentionView
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/retention", nil, ""), &v)
	if !v.Settings.Enabled || v.Settings.Days != 90 || v.Last != nil {
		t.Fatalf("defaults: %+v", v)
	}
	if resp := doReq(t, srv, http.MethodPut, "/api/admin/usage/retention", nil, `{"enabled":true,"days":7}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("days 7: %d", resp.StatusCode)
	}
	decode(t, doReq(t, srv, http.MethodPut, "/api/admin/usage/retention", nil, `{"enabled":false,"days":40}`), &v)
	if v.Settings.Enabled || v.Settings.Days != 40 {
		t.Fatalf("saved: %+v", v.Settings)
	}

	_, key, err := st.CreateAPIKey(ctx, store.APIKeySettings{Name: "k"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var recs []*store.UsageRecord
	for i := range 3 {
		recs = append(recs, &store.UsageRecord{RequestID: "old", APIKeyID: key.ID, TotalTokens: 10, StatusCode: 200,
			CreatedAt: now.AddDate(0, 0, -50).Add(time.Duration(i) * time.Minute).UnixMilli()})
	}
	recs = append(recs, &store.UsageRecord{RequestID: "new", APIKeyID: key.ID, TotalTokens: 1, StatusCode: 200, CreatedAt: now.UnixMilli()})
	if err := st.InsertUsage(ctx, recs...); err != nil {
		t.Fatal(err)
	}
	decode(t, doReq(t, srv, http.MethodGet, "/api/admin/usage/retention", nil, ""), &v)
	if v.Records != 4 || v.Expired != 3 || v.DBBytes <= 0 {
		t.Fatalf("before purge: %+v", v)
	}

	decode(t, doReq(t, srv, http.MethodPost, "/api/admin/usage/retention/run", nil, ""), &v)
	if v.Records != 1 || v.Expired != 0 || v.Last == nil || v.Last.Deleted != 3 || !v.Last.Manual || v.Last.Days != 40 {
		t.Fatalf("after purge: %+v last %+v", v, v.Last)
	}
	if total, _ := st.APIKeyTokensSince(ctx, key.ID, 0); total != 31 {
		t.Fatalf("key total usage: %d", total)
	}
}
