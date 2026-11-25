package pricing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shiceai/agy2api/internal/store"
)

// newTestService 用临时库和假价格页构造服务：Google 页返回 google 的内容，Anthropic 页返回 anthropic 的内容
// （为空时返回 500）。
func newTestService(t *testing.T, google, anthropic *atomic.Value) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := google
		if strings.HasPrefix(r.URL.Path, "/anthropic") {
			page = anthropic
		}
		body, _ := page.Load().([]byte)
		if len(body) == 0 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	s := New(st)
	s.sources[0].URL = srv.URL + "/google"
	s.sources[1].URL = srv.URL + "/anthropic"
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, st
}

func pageValue(data []byte) *atomic.Value {
	v := &atomic.Value{}
	v.Store(data)
	return v
}

func insertUsage(t *testing.T, st *store.Store, model string, prompt, output int64) int64 {
	t.Helper()
	cached, thoughts := int64(0), int64(0)
	rec := &store.UsageRecord{RequestID: "r-" + model, ResolvedModel: model, PromptTokens: prompt, OutputTokens: output,
		CachedTokens: &cached, ThoughtsTokens: &thoughts, TotalTokens: prompt + output, StatusCode: 200}
	if err := st.InsertUsage(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	logs, _, err := st.UsageLogs(context.Background(), store.UsageFilter{RequestID: rec.RequestID}, 1, 0)
	if err != nil || len(logs) != 1 {
		t.Fatalf("usage logs: %v %d", err, len(logs))
	}
	return logs[0].ID
}

func usageCost(t *testing.T, st *store.Store, id int64) (*int64, *store.UsageCost) {
	t.Helper()
	l, err := st.UsageLogByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var d store.UsageDetail
	if len(l.Detail) > 0 {
		if err := json.Unmarshal(l.Detail, &d); err != nil {
			t.Fatal(err)
		}
	}
	return l.CostNanos, d.Cost
}

// 同步：两个来源互不影响，失败的保留原价格；同步后补算之前没价格的用量记录，没有价格的保持未算。
func TestSyncAndBackfill(t *testing.T) {
	ctx := context.Background()
	google, anthropic := pageValue(readFixture(t, "google_pricing.md")), pageValue(nil)
	s, st := newTestService(t, google, anthropic)

	neonID := insertUsage(t, st, "gemini-4-neon-high", 1000, 100)
	ossID := insertUsage(t, st, "gpt-oss-120b-medium", 1000, 100)

	status, err := s.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Sources) != 2 || !status.Sources[0].OK || status.Sources[0].Prices < 10 || status.Sources[1].OK ||
		!strings.Contains(status.Sources[1].Error, "500") {
		t.Fatalf("status: %+v", status)
	}
	if saved, _ := s.Status(ctx); saved.AttemptAt != status.AttemptAt {
		t.Fatalf("status not saved: %+v", saved)
	}
	// 1000 × $3/M + 100 × $15/M = $0.0045
	nanos, cost := usageCost(t, st, neonID)
	if nanos == nil || *nanos != 4_500_000 || cost == nil || cost.Model != "gemini-4-neon" || !cost.Backfilled || cost.Input != 3_000_000 {
		t.Fatalf("neon backfill: %v %+v", nanos, cost)
	}
	if nanos, _ := usageCost(t, st, ossID); nanos != nil {
		t.Fatalf("unpriced model got cost %d", *nanos)
	}

	// Anthropic 恢复后再同步：读出价格；Google 页面改版只读出很少的型号时保留原价格。
	anthropic.Store(readFixture(t, "anthropic_pricing.md"))
	neon := s.Book().official["gemini-4-neon"]
	page := string(readFixture(t, "google_pricing.md"))
	google.Store([]byte(page[:strings.Index(page, "## Gemini 4 Hydrogen")]))
	status, err = s.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Sources[0].OK || !strings.Contains(status.Sources[0].Error, "八成") || status.Sources[0].SuccessAt == 0 || !status.Sources[1].OK {
		t.Fatalf("second status: %+v", status)
	}
	if _, ok := s.Book().Find("claude-opus-5-5-high"); !ok {
		t.Fatal("anthropic prices not loaded")
	}
	if got := s.Book().official["gemini-4-neon"]; got.Price.Input != neon.Price.Input || len(s.Book().official) < 20 {
		t.Fatalf("google prices should be kept: %+v (%d)", got, len(s.Book().official))
	}
}

// 手动设价：优先于官方价，补算没价格的记录，已算过的不变；删除手动价恢复官方价。
func TestManualPrice(t *testing.T) {
	ctx := context.Background()
	s, st := newTestService(t, pageValue(readFixture(t, "google_pricing.md")), pageValue(readFixture(t, "anthropic_pricing.md")))
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	ossID := insertUsage(t, st, "gpt-oss-120b-medium", 2000, 1000)

	if err := s.SetManual(ctx, "gpt-oss-120b", Price{Rates: Rates{Input: 100_000, Output: 500_000}}); err != nil {
		t.Fatal(err)
	}
	// 2000 × $0.1/M + 1000 × $0.5/M = $0.0007
	if nanos, cost := usageCost(t, st, ossID); nanos == nil || *nanos != 700_000 || cost.Source != SourceManual {
		t.Fatalf("manual backfill: %v %+v", nanos, cost)
	}
	// 手动价优先于官方价。
	if err := s.SetManual(ctx, "gemini-4-neon", Price{Rates: Rates{Input: 1, Output: 1}}); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Book().Find("gemini-4-neon-high"); e.Source != SourceManual {
		t.Fatalf("manual should win: %+v", e)
	}
	if err := s.DeleteManual(ctx, "gemini-4-neon"); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Book().Find("gemini-4-neon-high"); e.Source != SourceOfficial {
		t.Fatalf("official after delete: %+v", e)
	}
	// 改价不影响已算过的记录。
	if err := s.SetManual(ctx, "gpt-oss-120b", Price{Rates: Rates{Input: 900_000, Output: 900_000}}); err != nil {
		t.Fatal(err)
	}
	if nanos, _ := usageCost(t, st, ossID); *nanos != 700_000 {
		t.Fatalf("priced record changed: %d", *nanos)
	}

	if err := s.SetManual(ctx, "bad name!", Price{Rates: Rates{Input: 1, Output: 1}}); err != ErrInvalidModel {
		t.Fatalf("invalid model: %v", err)
	}
	if err := s.SetManual(ctx, "x", Price{}); err != ErrInvalidPrice {
		t.Fatalf("invalid price: %v", err)
	}
	if err := s.DeleteManual(ctx, "not-set"); err != store.ErrNotFound {
		t.Fatalf("delete missing: %v", err)
	}
}
