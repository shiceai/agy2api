package antigravity

import (
	"encoding/json"
	"testing"
)

// AI 积分在付费档位的 availableCredits 里：int64 可能写成字符串，值为 0 时 proto3 省略 creditAmount；
// 没有付费档位或没给积分时为 nil。
func TestSubscriptionCredits(t *testing.T) {
	for _, tc := range []struct {
		body string
		want *float64
	}{
		{`{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"1200"}]}}`, ptr(1200)},
		{`{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":35.5}]}}`, ptr(35.5)},
		{`{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI"}]}}`, ptr(0)},
		{`{"paidTier":{"id":"g1-pro-tier"}}`, nil},
		{`{"currentTier":{"id":"free-tier"}}`, nil},
	} {
		var r loadCodeAssistResponse
		if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
			t.Fatal(err)
		}
		got := r.subscription().Credits
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: credits = %v, want %v", tc.body, deref(got), deref(tc.want))
		}
	}
}

func ptr(f float64) *float64 { return &f }

func deref(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}
