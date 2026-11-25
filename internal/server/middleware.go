package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/store"
)

type ctxKey int

const (
	ctxKeyAPIKey ctxKey = iota
)

// requestLog 为每个请求生成请求 ID，放进 ctx 和 X-Request-Id 响应头；
// 请求结束后记一条访问日志，并兜住 handler 的 panic。
// 访问日志只记路径不记查询串，避免查询串里的密钥（如 ?key=）落进日志。
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := logx.NewRequestID()
		ctx := logx.WithRequestID(r.Context(), id)
		w.Header().Set("X-Request-Id", id)
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()

		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec) // 交给 net/http 静默中止连接
				}
				slog.ErrorContext(ctx, "处理请求时 panic", "panic", rec, "stack", string(debug.Stack()))
				if ww.Status() == 0 {
					ww.WriteHeader(http.StatusInternalServerError)
				}
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK // handler 什么都没写，net/http 会补 200
			}
			level := slog.LevelInfo
			if r.URL.Path == "/healthz" {
				level = slog.LevelDebug
			}
			slog.Log(ctx, level, "请求完成",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", r.RemoteAddr,
			)
		}()

		next.ServeHTTP(ww, r.WithContext(ctx))
	})
}

// apiKeyAuth 校验入站 API Key，查内存缓存，不读库。支持 x-goog-api-key、Authorization: Bearer、x-api-key，
// 都没有时再看 ?key= 查询串（Gemini 官方的写法之一）。请求日志只记路径、不记查询串，Key 不会写进我们的日志；
// 前面有反向代理时，它的访问日志可能记下完整地址。
func (s *Service) apiKeyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := extractAPIKey(r)
		if key == "" {
			geminiError(w, http.StatusUnauthorized, "UNAUTHENTICATED",
				"missing API key: send x-goog-api-key, Authorization: Bearer, x-api-key, or ?key=")
			return
		}
		rec, ok := s.keys.Lookup(key)
		if !ok {
			geminiError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid API key")
			return
		}
		if keyExpired(rec, time.Now()) {
			geminiError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "API key expired")
			return
		}
		s.keys.Touch(rec.ID)
		ctx := context.WithValue(r.Context(), ctxKeyAPIKey, rec)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func apiKeyFromContext(ctx context.Context) *store.APIKey {
	if v, ok := ctx.Value(ctxKeyAPIKey).(*store.APIKey); ok {
		return v
	}
	return nil
}

func extractAPIKey(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("x-goog-api-key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("x-api-key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("Authorization")); v != "" {
		if lower := strings.ToLower(v); strings.HasPrefix(lower, "bearer ") {
			return strings.TrimSpace(v[len("bearer "):])
		}
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

// adminAuth 用静态密码保护管理端。密码放在 Authorization: Bearer 或 X-Admin-Password。
// 用简单的静态口令，没有登录会话。错误口令太多的地址先锁定一段时间，锁定中口令对了也返回 429（见 adminguard.go）；
// 没带口令不算一次猜测。
func (s *Service) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AllowNoAdminAuth {
			next.ServeHTTP(w, r)
			return
		}
		addr, _ := parseHostAddr(r.RemoteAddr)
		now := time.Now()
		if wait, locked := s.guard.Locked(addr, now); locked {
			adminLocked(w, wait)
			return
		}
		provided := adminPassword(r)
		if provided == "" {
			adminError(w, http.StatusUnauthorized, "invalid admin password")
			return
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.AdminPassword)) != 1 {
			if lock, distinct := s.guard.Fail(addr, provided, now); lock > 0 {
				slog.WarnContext(r.Context(), "管理口令错误太多，已锁定这个地址", "remote", guardKey(addr), "distinct", distinct, "lock", lock)
				adminLocked(w, lock)
				return
			}
			adminError(w, http.StatusUnauthorized, "invalid admin password")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func adminPassword(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Admin-Password")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("Authorization")); v != "" {
		if lower := strings.ToLower(v); strings.HasPrefix(lower, "bearer ") {
			return strings.TrimSpace(v[len("bearer "):])
		}
	}
	return ""
}
