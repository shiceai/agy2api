package antigravity

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 基址回退：连接失败、408、5xx 都换下一个基址；429 只有控制面换；其它 4xx 不换。
// 两个基址都要回退时，返回最后一个响应（错误体可读），交给调用方分类。
func TestBaseFallbackPolicy(t *testing.T) {
	cases := []struct {
		name      string
		status    int // 第一个基址返回的状态，-1 表示连不上
		data      bool
		wantCalls int // 第二个基址被调用的次数
	}{
		{"数据面 5xx 换基址", 500, true, 1},
		{"数据面 408 换基址", 408, true, 1},
		{"数据面连不上换基址", -1, true, 1},
		{"数据面 429 不换", 429, true, 0},
		{"数据面 400 不换", 400, true, 0},
		{"控制面 429 换基址", 429, false, 1},
		{"控制面 5xx 换基址", 503, false, 1},
		{"控制面 403 不换", 403, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"error":{"code":%d}}`, tc.status)
			}))
			firstURL := first.URL
			if tc.status == -1 {
				first.Close() // 关掉后连接被拒绝
			} else {
				defer first.Close()
			}
			var calls atomic.Int32
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"response":{}}`))
			}))
			defer second.Close()

			c := NewClient(Options{UpstreamBases: []string{firstURL, second.URL}})
			var status int
			var err error
			if tc.data {
				var resp *Response
				resp, err = c.Generate(context.Background(), "", "at", "", []byte(`{}`), false, false)
				if err == nil {
					status = resp.Status
					resp.Body.Close()
				}
			} else {
				status, err = c.postV1Internal(context.Background(), &Credential{AccessToken: "at"}, "loadCodeAssist", map[string]any{}, nil, nil)
			}
			if got := int(calls.Load()); got != tc.wantCalls {
				t.Fatalf("第二个基址应被调用 %d 次，实际 %d（status=%d err=%v）", tc.wantCalls, got, status, err)
			}
			if tc.wantCalls == 0 && status != tc.status {
				t.Fatalf("不回退时应返回第一个基址的状态 %d，实际 %d %v", tc.status, status, err)
			}
		})
	}
}

// 所有基址都返回可回退的状态时，返回最后一个响应，错误体仍可读。
func TestBaseFallbackReturnsLastResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":503,"status":"UNAVAILABLE"}}`))
	}))
	defer srv.Close()
	c := NewClient(Options{UpstreamBases: []string{srv.URL, srv.URL}})
	resp, err := c.Generate(context.Background(), "", "at", "", []byte(`{}`), false, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Status != http.StatusServiceUnavailable || string(body) != `{"error":{"code":503,"status":"UNAVAILABLE"}}` {
		t.Fatalf("应返回最后一个 503 和完整错误体：%d %s", resp.Status, body)
	}
}

// 请求被取消（客户端断开、超时）时不换基址：换了也是同一个已取消的请求，只会把真正的错误换成第二个基址的。
func TestBaseFallbackStopsWhenCanceled(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body) // 读完请求体，服务端才能察觉客户端断开
		<-r.Context().Done()
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	defer second.Close()

	c := NewClient(Options{UpstreamBases: []string{first.URL, second.URL}})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Generate(ctx, "", "at", "", []byte(`{}`), false, false)
	if err == nil || !strings.Contains(err.Error(), first.URL) {
		t.Fatalf("应直接返回第一个基址的取消错误，实际 %v", err)
	}
}

// Server-Timing 取 dur；没有或格式不对时 ok 为 false。
func TestServerTiming(t *testing.T) {
	cases := map[string]time.Duration{
		"gfet4t7; dur=8354":                8354 * time.Millisecond,
		"cache;desc=hit, gfet4t7;dur=12.5": 12500 * time.Microsecond,
	}
	for header, want := range cases {
		r := &Response{Header: http.Header{"Server-Timing": {header}}}
		if got, ok := r.ServerTiming(); !ok || got != want {
			t.Fatalf("%q 应解析为 %v，实际 %v %v", header, want, got, ok)
		}
	}
	for _, header := range []string{"", "gfet4t7", "gfet4t7; dur=abc"} {
		r := &Response{Header: http.Header{"Server-Timing": {header}}}
		if _, ok := r.ServerTiming(); ok {
			t.Fatalf("%q 不应解析出时长", header)
		}
	}
}
