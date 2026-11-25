// Package logx 构造 log/slog 日志器，并让请求 ID 随 ctx 自动出现在日志里。
//
// 用 slog.InfoContext 等带 ctx 的方法记日志时，ctx 里的请求 ID 会作为 request_id 字段附加，
// 调用方不用自己传。token、API Key 等密钥一律不写进日志。
package logx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
)

type ctxKey struct{}

// WithRequestID 把请求 ID 放进 ctx。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID 取出 ctx 里的请求 ID，没有时返回空串。
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// NewRequestID 生成一个 16 位十六进制的随机请求 ID。
func NewRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// New 构造文本格式的日志器，写到 w，低于 level 的日志丢弃。
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(contextHandler{slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})})
}

// ParseLevel 解析 debug、info、warn、error（不区分大小写）。
func ParseLevel(s string) (slog.Level, error) {
	var level slog.Level
	err := level.UnmarshalText([]byte(s))
	return level, err
}

// contextHandler 在每条日志上附加 ctx 里的请求 ID。
type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
