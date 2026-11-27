package relay

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/store"
)

// captureBody 是带各种 part 的请求体：两轮对话（含一张内联图片、一个文件引用、函数调用和结果）、系统提示、工具、生成参数。
func captureBody() map[string]any {
	return map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "你是助手"}}},
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"text": "看看这张图"},
				map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "AAAAAAAA"}},
				map[string]any{"fileData": map[string]any{"mimeType": "application/pdf", "fileUri": "gs://x"}},
			}},
			map[string]any{"role": "model", "parts": []any{
				map[string]any{"text": "好的"},
				map[string]any{"functionCall": map[string]any{"name": "f"}},
			}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": "f"}}}},
		},
		"tools": []any{
			map[string]any{"functionDeclarations": []any{map[string]any{"name": "f"}, map[string]any{"name": "g"}}},
			map[string]any{"googleSearch": map[string]any{}},
		},
		"generationConfig": map[string]any{"maxOutputTokens": 1024, "thinkingConfig": map[string]any{"thinkingLevel": "low"},
			"responseJsonSchema": map[string]any{"type": "object"}},
		"safetySettings": []any{map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}},
	}
}

// 上游返回错误：用量详情里有请求的结构摘要（不含文字）和上游错误原文；没开保存正文时不存、不标。
func TestFailureRecordsRequestSummary(t *testing.T) {
	e := newEnv(t, "a")
	e.up.script["a"] = []int{400}
	ctx := logx.WithRequestID(context.Background(), "req-summary")
	if _, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: captureBody()}); err == nil {
		t.Fatal("应失败")
	}
	_, d := e.usageByRequest("req-summary")
	s := d.Request
	if s == nil {
		t.Fatal("失败的请求应有结构摘要")
	}
	if s.Bytes <= 0 || s.Turns != 3 || s.Roles["user"] != 2 || s.Roles["model"] != 1 || s.TextChars != 7 || s.SystemChars != 4 ||
		s.FunctionCalls != 1 || s.FunctionResponses != 1 || s.Tools != 2 || len(s.OtherTools) != 1 || s.OtherTools[0] != "googleSearch" {
		t.Fatalf("摘要的计数不对：%+v", s)
	}
	if len(s.Media) != 2 || s.Media[0].MimeType != "application/pdf" || s.Media[0].Files != 1 ||
		s.Media[1].MimeType != "image/png" || s.Media[1].Count != 1 || s.Media[1].Bytes != 6 {
		t.Fatalf("媒体按类型汇总不对：%+v", s.Media)
	}
	if s.Generation["maxOutputTokens"] == nil || s.Generation["thinkingConfig"] == nil || s.Generation["responseJsonSchema"] != true {
		t.Fatalf("生成参数应保留关键字段，结构化输出只记有没有：%+v", s.Generation)
	}
	if len(s.Safety) != 1 || s.Safety[0] != "HARM_CATEGORY_HARASSMENT=BLOCK_NONE" {
		t.Fatalf("安全设置：%+v", s.Safety)
	}
	if d.Error == nil || !strings.Contains(d.Error.Raw, `"scripted"`) {
		t.Fatalf("应记上游错误原文：%+v", d.Error)
	}
	if d.RequestBody != "" {
		t.Fatalf("没开保存正文时不应标记：%q", d.RequestBody)
	}
	if _, _, err := e.store.RequestBody(context.Background(), "req-summary"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("没开保存正文时不应存：%v", err)
	}
}

// 开了保存正文：失败请求把发给上游的请求体（含信封）压缩存下，用量详情标 saved；成功的请求不存、不记摘要。
func TestRequestCaptureSavesFailedBody(t *testing.T) {
	e := newEnv(t, "a")
	e.relay.SetRequestCapture(true)
	e.up.script["a"] = []int{400}
	ctx := logx.WithRequestID(context.Background(), "req-capture")
	if _, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: captureBody()}); err == nil {
		t.Fatal("应失败")
	}
	resp, err := e.relay.Generate(logx.WithRequestID(context.Background(), "req-ok"), Request{Model: "gemini-2.5-flash", Body: captureBody()})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Close()

	// usageByRequest 先关闭 relay：用量记录和排队的正文都写完。
	_, d := e.usageByRequest("req-capture")
	if d.RequestBody != store.RequestBodySaved {
		t.Fatalf("应标记已保存：%q", d.RequestBody)
	}
	gz, size, err := e.store.RequestBody(context.Background(), "req-capture")
	if err != nil {
		t.Fatalf("应存下正文：%v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	if int64(len(raw)) != size || !strings.Contains(string(raw), `"request"`) || !strings.Contains(string(raw), "看看这张图") {
		t.Fatalf("存的应是发给上游的完整请求体（含信封）：%d/%d %.200s", len(raw), size, raw)
	}

	_, d = e.usageByRequest("req-ok")
	if d.Request != nil || d.RequestBody != "" {
		t.Fatalf("成功的请求不记摘要、不存正文：%+v %q", d.Request, d.RequestBody)
	}
	if _, _, err := e.store.RequestBody(context.Background(), "req-ok"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("成功的请求不应存正文：%v", err)
	}
}

// 限速：每分钟最多 CapturePerMinute 条，超出的跳过，过了一分钟恢复；排队满也跳过；没开时什么都不做。
func TestCaptureRateLimitAndQueue(t *testing.T) {
	c := &capturer{queue: make(chan captureJob, 100), done: make(chan struct{})}
	now := time.Now()
	if got := c.offer("r", []byte("x"), now); got != "" {
		t.Fatalf("没开时应返回空：%q", got)
	}
	c.enabled.Store(true)
	for i := range CapturePerMinute {
		if got := c.offer("r", []byte("x"), now.Add(time.Duration(i)*time.Second)); got != store.RequestBodySaved {
			t.Fatalf("第 %d 条应保存：%q", i+1, got)
		}
	}
	if got := c.offer("r", []byte("x"), now.Add(30*time.Second)); got != store.RequestBodySkipped {
		t.Fatalf("一分钟内超过 %d 条应跳过：%q", CapturePerMinute, got)
	}
	if got := c.offer("r", []byte("x"), now.Add(61*time.Second)); got != store.RequestBodySaved {
		t.Fatalf("最早的过了一分钟后应恢复：%q", got)
	}

	full := &capturer{queue: make(chan captureJob, 1), done: make(chan struct{})}
	full.enabled.Store(true)
	if full.offer("r1", []byte("x"), now) != store.RequestBodySaved || full.offer("r2", []byte("x"), now) != store.RequestBodySkipped {
		t.Fatal("排队满时应跳过")
	}
}
