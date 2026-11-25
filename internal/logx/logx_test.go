package logx

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestRequestIDAttachedFromContext(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo).With("component", "test")

	ctx := WithRequestID(context.Background(), "abc123")
	logger.InfoContext(ctx, "with id")
	logger.Info("without id")
	logger.DebugContext(ctx, "dropped")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("应输出 2 行（debug 被过滤），实际 %d 行：\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "request_id=abc123") || !strings.Contains(lines[0], "component=test") {
		t.Fatalf("第一行应带 request_id 和 With 的字段：%s", lines[0])
	}
	if strings.Contains(lines[1], "request_id") {
		t.Fatalf("ctx 没有请求 ID 时不应输出 request_id：%s", lines[1])
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Fatalf("ParseLevel(%q) = %v, %v；期望 %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Fatal("无法识别的级别应返回错误")
	}
}
