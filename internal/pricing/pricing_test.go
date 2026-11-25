package pricing

import (
	"os"
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func byModel(entries []Entry) map[string]Entry {
	m := map[string]Entry{}
	for _, e := range entries {
		m[e.Model] = e
	}
	return m
}

// Google 价格页（2026-11-23 快照）：单一价、按提示长度两档、限时价都读出来；按图片、语音计价的跳过。
func TestParseGoogle(t *testing.T) {
	entries, skipped, err := parseGoogle(readFixture(t, "google_pricing.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := byModel(entries)

	neon := m["gemini-4-neon"]
	if neon.Name != "Gemini 4 Neon" || neon.Vendor != VendorGoogle || neon.Price.Input != 3_000_000 ||
		neon.Price.Output != 15_000_000 || *neon.Price.CacheRead != 150_000 || neon.Price.Tier != nil || neon.Price.Promo != nil {
		t.Fatalf("neon: %+v", neon)
	}
	pro := m["gemini-2.5-pro"].Price
	if pro.Input != 1_250_000 || pro.Tier == nil || pro.Tier.Threshold != 200_000 || pro.Tier.Input != 2_500_000 ||
		pro.Tier.Output != 15_000_000 || *pro.Tier.CacheRead != 250_000 {
		t.Fatalf("2.5 pro: %+v tier %+v", pro, pro.Tier)
	}
	// "$0.75 through December 31, 2026. $1.50 starting January 1, 2027."：限时价到太平洋时间 2027-01-01 0 点。
	flash := m["gemini-3.8-flash"].Price
	end := time.Date(2027, 1, 1, 8, 0, 0, 0, time.UTC).UnixMilli()
	if flash.Input != 1_500_000 || flash.Promo == nil || flash.Promo.End != end || flash.Promo.Input != 750_000 ||
		flash.Promo.Output != 3_750_000 || *flash.Promo.CacheRead != 75_000 {
		t.Fatalf("3.8 flash: %+v promo %+v", flash, flash.Promo)
	}
	if _, ok := m["gemini-3-flash-preview"]; !ok {
		t.Fatal("gemini-3-flash-preview missing")
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Model] = s.Reason
	}
	if reasons["gemini-3.1-flash-image"] == "" || reasons["gemini-2.5-flash-preview-tts"] == "" {
		t.Fatalf("image/tts models should be skipped: %v", reasons)
	}
	if _, _, err := parseGoogle([]byte("# Pricing\n\nnothing here")); err == nil {
		t.Fatal("page without model sections should fail")
	}
}

// Anthropic 价格页：显示名换算成型号 ID，退役的不读，短、长提示两行合成分档价。
func TestParseAnthropic(t *testing.T) {
	entries, _, err := parseAnthropic(readFixture(t, "anthropic_pricing.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := byModel(entries)
	opus := m["claude-opus-5-5"]
	if opus.Name != "Claude Opus 5.5" || opus.Price.Input != 4_000_000 || opus.Price.Output != 20_000_000 ||
		*opus.Price.CacheRead != 200_000 {
		t.Fatalf("opus 5.5: %+v", opus)
	}
	if s := m["claude-sonnet-5-5"].Price; s.Input != 2_000_000 || *s.CacheRead != 100_000 {
		t.Fatalf("sonnet 5.5: %+v", s)
	}
	haiku := m["claude-haiku-5-5"].Price
	if haiku.Input != 100_000 || haiku.Tier == nil || haiku.Tier.Threshold != 100_000 || haiku.Tier.Input != 500_000 {
		t.Fatalf("haiku 5.5: %+v tier %+v", haiku, haiku.Tier)
	}
	if _, ok := m["claude-opus-4-1"]; ok {
		t.Fatal("retired model should be skipped")
	}
	if _, _, err := parseAnthropic([]byte("# Pricing\n\n## Other\n")); err == nil {
		t.Fatal("page without Model pricing should fail")
	}
}

// 算费用：输入扣掉缓存部分，缓存按缓存价，输出和思考按输出价；限时价按时间，分档按提示长度。
func TestCost(t *testing.T) {
	p := Price{Rates: Rates{Input: 2_000_000, Output: 12_000_000, CacheRead: i64(200_000)},
		Tier: &Tier{Threshold: 200_000, Rates: Rates{Input: 4_000_000, Output: 18_000_000, CacheRead: i64(400_000)}}}
	now := time.Now()
	u := Usage{Prompt: 1000, Cached: 400, Output: 100, Thoughts: 50}
	a := p.At(u.Prompt, now)
	// 600 × $2/M + 400 × $0.2/M + 150 × $12/M = $0.0012 + $0.00008 + $0.0018 = $0.00308
	if a.Tier || a.Nanos(u) != 3_080_000 {
		t.Fatalf("base cost: %+v %d", a, a.Nanos(u))
	}
	if a := p.At(200_001, now); !a.Tier || a.Input != 4_000_000 {
		t.Fatalf("tier not applied: %+v", a)
	}
	if a := p.At(200_000, now); a.Tier {
		t.Fatal("tier applies only above the threshold")
	}

	promo := Price{Rates: Rates{Input: 1_500_000, Output: 7_500_000},
		Promo: &Promo{End: now.Add(time.Hour).UnixMilli(), Rates: Rates{Input: 750_000, Output: 3_750_000}}}
	if a := promo.At(10, now); !a.Promo || a.Input != 750_000 {
		t.Fatalf("promo not applied: %+v", a)
	}
	if a := promo.At(10, now.Add(2*time.Hour)); a.Promo || a.Input != 1_500_000 {
		t.Fatalf("promo after end: %+v", a)
	}
	// 没有缓存价时缓存部分按输入价：(2 + 1) × $0.3/M + 1 × $2.5/M = $0.0000034。
	r := Rates{Input: 300_000, Output: 2_500_000}
	if got := r.Nanos(Usage{Prompt: 3, Cached: 1, Output: 1}); got != 3400 {
		t.Fatalf("no cache price: %d", got)
	}
	// 四舍五入到十亿分之一美元：1 × $0.0015/M = 1.5 纳美元 → 2。
	if got := (Rates{Input: 1500}).Nanos(Usage{Prompt: 1}); got != 2 {
		t.Fatalf("rounding: %d", got)
	}
}

// 查价：模型 ID、去掉档位的家族名、别名依次查，同一个名字手动价优先。
func TestBookFind(t *testing.T) {
	b := NewBook([]Entry{
		{Model: "gemini-4-neon", Source: SourceOfficial, Price: Price{Rates: Rates{Input: 3, Output: 15}}},
		{Model: "gemini-3-flash-preview", Source: SourceOfficial, Price: Price{Rates: Rates{Input: 5, Output: 30}}},
		{Model: "claude-opus-5-5", Source: SourceOfficial, Price: Price{Rates: Rates{Input: 4, Output: 20}}},
		{Model: "claude-opus-5-5", Source: SourceManual, Price: Price{Rates: Rates{Input: 1, Output: 1}}},
		{Model: "gpt-oss-120b", Source: SourceManual, Price: Price{Rates: Rates{Input: 9, Output: 9}}},
	})
	cases := map[string]string{
		"gemini-4-neon-high":      "gemini-4-neon/official",
		"gemini-4-neon":           "gemini-4-neon/official",
		"gemini-3-flash":          "gemini-3-flash-preview/official",
		"claude-opus-5-5-low":     "claude-opus-5-5/manual",
		"gpt-oss-120b-medium":     "gpt-oss-120b/manual",
		"gemini-pro-orchestrator": "",
		"tab_flash_lite_preview":  "",
	}
	for model, want := range cases {
		e, ok := b.Find(model)
		got := ""
		if ok {
			got = e.Model + "/" + e.Source
		}
		if got != want {
			t.Errorf("Find(%q) = %q, want %q", model, got, want)
		}
	}
	if e, ok := b.Official("claude-opus-5-5-high"); !ok || e.Source != SourceOfficial {
		t.Fatalf("Official: %+v %v", e, ok)
	}
}
