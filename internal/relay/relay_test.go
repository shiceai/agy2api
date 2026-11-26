package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiceai/agy2api/internal/antigravity"
	"github.com/shiceai/agy2api/internal/cryptox"
	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/store"
)

// 假上游脚本里的特殊状态：dropConn 直接断开连接，模拟传输失败；其余是带特定错误体的 403、429。
const (
	dropConn          = -1
	tos403            = 4031 // 条款封禁
	validation403     = 4032 // 需要验证
	modelForbidden403 = 4033 // 无权使用该模型
	unknown403        = 4034 // 原因不明
	transient429      = 4291 // 瞬时限流，不带恢复时间
	stallStream       = 2001 // 流式发一帧后不再发数据，直到请求被取消
	hugeBody          = 2002 // 非流式返回超过 antigravity 上限（64 MiB）的 200 响应
	midStream429      = 2003 // 流式发一帧正常内容后推来 429 额度耗尽的错误帧
	inBand503         = 2004 // 回 200，但正文（流式是第一帧）是 503 错误
	dropBeforeFrame   = 2005 // 流式回了 200 响应头，第一帧之前断开连接
	stallBeforeFrame  = 2006 // 流式回了 200 响应头，之后不发数据，直到请求被取消
)

// quotaExhausted 是额度耗尽的 429 错误体，带 90 秒重置时间。
var quotaExhausted = map[string]any{"error": map[string]any{
	"code": 429, "status": "RESOURCE_EXHAUSTED",
	"details": []any{
		map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "QUOTA_EXHAUSTED"},
		map[string]any{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "90s"},
	},
}}

// fakeUpstream 是可编程的 Cloud Code 上游。账号用 refresh token 区分：
// 令牌端点签发 "at|<refresh token>|<序号>"，生成端点据此识别账号，
// 依次返回该账号脚本里的状态码，脚本用完后返回 200。
type fakeUpstream struct {
	mu         sync.Mutex
	script     map[string][]int
	rotate     map[string]string // 刷新时把 refresh token 轮换成新值（一次性）
	revoked    map[string]bool   // 刷新时返回 invalid_grant
	tokenCalls map[string]int
	genCalls   map[string]int
	lastBody   map[string]map[string]any // 每个账号最近一次生成请求的信封
	lastUA     map[string]string         // 每个账号最近一次生成请求的 User-Agent
	catalogs   map[string][]string       // 每个账号 fetchAvailableModels 返回的模型
	remaining  map[string]float64        // 每个账号目录里各模型的剩余额度，没设置的不带 quotaInfo
	paidTier   map[string]string         // 每个账号 loadCodeAssist 返回的付费档位名称
	fetchCalls int
	fetchDelay time.Duration // 取模型目录前先等这么久，测并发刷新用
	loadCalls  int
	// summaryCalls 是 retrieveUserQuotaSummary（每周额度）的调用次数。
	summaryCalls int
}

func newFakeUpstream() *fakeUpstream {
	return &fakeUpstream{
		script:     map[string][]int{},
		rotate:     map[string]string{},
		revoked:    map[string]bool{},
		tokenCalls: map[string]int{},
		genCalls:   map[string]int{},
		lastBody:   map[string]map[string]any{},
		lastUA:     map[string]string{},
		catalogs:   map[string][]string{},
		remaining:  map[string]float64{},
		paidTier:   map[string]string{},
	}
}

func (f *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/token":
		f.handleToken(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1internal:") && strings.Contains(r.URL.Path, "enerateContent"):
		f.handleGenerate(w, r)
	case r.URL.Path == "/v1internal:fetchAvailableModels":
		f.handleFetchModels(w, r)
	case r.URL.Path == "/v1internal:loadCodeAssist":
		f.handleLoadCodeAssist(w, r)
	case r.URL.Path == "/v1internal:retrieveUserQuotaSummary":
		f.mu.Lock()
		f.summaryCalls++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.3,"resetTime":"2030-01-01T00:00:00Z"}]}]}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeUpstream) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	rt := r.PostForm.Get("refresh_token")
	f.mu.Lock()
	f.tokenCalls[rt]++
	if f.revoked[rt] {
		f.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
		return
	}
	resp := map[string]any{
		"access_token": fmt.Sprintf("at|%s|%d", rt, f.tokenCalls[rt]),
		"expires_in":   3600,
	}
	if next, ok := f.rotate[rt]; ok {
		resp["refresh_token"] = next
		delete(f.rotate, rt)
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (f *fakeUpstream) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var envelope map[string]any
	_ = json.NewDecoder(r.Body).Decode(&envelope)
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "|")
	if len(parts) != 3 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": 401}})
		return
	}
	rt := parts[1]
	f.mu.Lock()
	f.genCalls[rt]++
	f.lastBody[rt] = envelope
	f.lastUA[rt] = r.Header.Get("User-Agent")
	status := http.StatusOK
	if s := f.script[rt]; len(s) > 0 {
		status, f.script[rt] = s[0], s[1:]
	}
	f.mu.Unlock()

	switch status {
	case stallStream:
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", frame(rt, nil))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	case hugeBody:
		w.Header().Set("Content-Type", "application/json")
		chunk := []byte(strings.Repeat("a", 1<<20))
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"parts":[{"text":"`)
		for i := 0; i < 65; i++ {
			if _, err := w.Write(chunk); err != nil {
				return // 网关读到上限就断开
			}
		}
		_, _ = io.WriteString(w, `"}]}}]}}`)
	case dropConn:
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	case http.StatusOK:
		w.Header().Set("Server-Timing", "gfet4t7; dur=42")
		if strings.Contains(r.URL.Path, "stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", frame(rt, nil))
			fmt.Fprintf(w, "data: %s\n\n", frame(rt, map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 7, "totalTokenCount": 10, "cachedContentTokenCount": 2, "thoughtsTokenCount": 4}))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(frame(rt, map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 2, "totalTokenCount": 5, "thoughtsTokenCount": 1}))
	case http.StatusTooManyRequests:
		writeJSON(w, status, quotaExhausted)
	case midStream429:
		raw, _ := json.Marshal(quotaExhausted)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", frame(rt, nil), raw)
	case dropBeforeFrame, stallBeforeFrame:
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if status == stallBeforeFrame {
			<-r.Context().Done()
		} else if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	case inBand503:
		const body = `{"error":{"code":503,"status":"UNAVAILABLE","message":"overloaded"}}`
		if strings.Contains(r.URL.Path, "stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	case tos403:
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
			"code": 403, "status": "PERMISSION_DENIED",
			"message": "Gemini has been disabled in this account for violation of Terms of Service.",
		}})
	case validation403:
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
			"code": 403, "message": "Please verify your account",
			"details": []any{map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "VALIDATION_REQUIRED"}},
		}})
	case modelForbidden403:
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"code": 403, "message": "The caller does not have access to model x"}})
	case unknown403:
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"code": 403, "message": "forbidden"}})
	case transient429:
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"code": 429, "status": "RESOURCE_EXHAUSTED"}})
	default:
		w.Header().Set("X-Cloudaicompanion-Trace-Id", "err-trace-"+rt)
		writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": "scripted"}})
	}
}

func (f *fakeUpstream) handleFetchModels(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "|")
	if len(parts) != 3 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": 401}})
		return
	}
	if f.fetchDelay > 0 {
		time.Sleep(f.fetchDelay)
	}
	f.mu.Lock()
	f.fetchCalls++
	models := map[string]any{}
	for _, id := range f.catalogs[parts[1]] {
		m := map[string]any{"displayName": id, "model": "ENUM_" + id}
		if rem, ok := f.remaining[parts[1]]; ok {
			m["quotaInfo"] = map[string]any{"remainingFraction": rem, "resetTime": "2026-11-23T10:00:00Z"}
		}
		models[id] = m
	}
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (f *fakeUpstream) handleLoadCodeAssist(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "|")
	if len(parts) != 3 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": 401}})
		return
	}
	f.mu.Lock()
	f.loadCalls++
	paid := f.paidTier[parts[1]]
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"cloudaicompanionProject": "p-" + parts[1],
		"currentTier":             map[string]any{"id": "standard-tier", "name": "Antigravity"},
		"paidTier":                map[string]any{"id": "g1-pro-tier", "name": paid},
	})
}

func frame(rt string, usage map[string]any) []byte {
	inner := map[string]any{
		"candidates":   []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "from " + rt}}}}},
		"modelVersion": "upstream-model",
		"responseId":   "resp-" + rt,
	}
	if usage != nil {
		inner["usageMetadata"] = usage
	}
	raw, _ := json.Marshal(map[string]any{"response": inner, "traceId": "trace-" + rt})
	return raw
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeUpstream) last(rt string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBody[rt]
}

func (f *fakeUpstream) calls(rt string) (token, gen int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls[rt], f.genCalls[rt]
}

type env struct {
	t     *testing.T
	relay *Relay
	up    *fakeUpstream
	store *store.Store
	enc   *cryptox.Cipher
	ids   map[string]int64 // refresh token → 账号 ID
}

// newEnv 构造 relay 和假上游，accounts 是各账号的 refresh token，按顺序入库。
func newEnv(t *testing.T, accounts ...string) *env {
	t.Helper()
	up := newFakeUpstream()
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	enc, err := cryptox.New([]byte("test-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, up: up, store: st, enc: enc, ids: map[string]int64{}}
	for _, rt := range accounts {
		a := &store.Account{Email: rt + "@example.com", ProjectID: "p-" + rt, RefreshToken: rt}
		if err := st.CreateAccount(context.Background(), enc, a); err != nil {
			t.Fatal(err)
		}
		e.ids[rt] = a.ID
	}
	client := antigravity.NewClient(antigravity.Options{
		TokenURL:      srv.URL + "/token",
		UserInfoURL:   srv.URL + "/userinfo",
		UpstreamBases: []string{srv.URL},
	})
	e.relay = New(client, st, enc)
	t.Cleanup(func() { e.relay.Close(context.Background()) })
	// 这些测试发的是同样的请求，靠轮询分散到不同账号；会话粘性另有测试（affinity_test.go）。
	// 换号次数用完时立即返回，不等冷却；等冷却另有测试（TestCooldownWait）。
	p := e.relay.Policy()
	p.Sticky = false
	p.CooldownWait = 0
	e.relay.SetPolicy(p)
	if err := e.relay.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

// setCatalog 给账号写入模型目录并重新加载号池。
func (e *env) setCatalog(rt string, models ...string) {
	e.t.Helper()
	a, err := e.store.GetAccount(context.Background(), e.enc, e.ids[rt])
	if err != nil {
		e.t.Fatal(err)
	}
	catalog := make([]antigravity.ModelInfo, 0, len(models))
	for _, m := range models {
		catalog = append(catalog, antigravity.ModelInfo{ID: m})
	}
	a.ModelsJSON = antigravity.MarshalModels(catalog)
	if err := e.store.UpdateAccountSync(context.Background(), e.enc, a); err != nil {
		e.t.Fatal(err)
	}
	if err := e.relay.Reload(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

// generate 发一次请求并读完、关闭响应，返回状态码、响应体和服务账号的 refresh token。
func (e *env) generate(model string, stream bool) (int, string, string, error) {
	e.t.Helper()
	return e.generateBody(model, stream, map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
	})
}

func (e *env) generateBody(model string, stream bool, body map[string]any) (int, string, string, error) {
	e.t.Helper()
	resp, err := e.relay.Generate(context.Background(), Request{Model: model, Body: body, Stream: stream})
	if err != nil {
		return 0, "", "", err
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Close()
	return resp.Status, string(out), e.account(resp.call.lease.Account.ID), nil
}

func (e *env) account(id int64) string {
	for rt, v := range e.ids {
		if v == id {
			return rt
		}
	}
	return fmt.Sprintf("unknown(%d)", id)
}

// servedBy 连续请求 n 次，返回各账号服务的次数。
func (e *env) servedBy(model string, n int) map[string]int {
	e.t.Helper()
	got := map[string]int{}
	for i := 0; i < n; i++ {
		status, _, rt, err := e.generate(model, false)
		if err != nil || status != http.StatusOK {
			e.t.Fatalf("第 %d 次请求 %s 失败：%d %v", i+1, model, status, err)
		}
		got[rt]++
	}
	return got
}

func TestGenerateRetryAndFailover(t *testing.T) {
	cases := []struct {
		name   string
		script map[string][]int
		// 期望由哪个账号返回 200，以及每个账号的生成调用次数。
		servedBy string
		genCalls map[string]int
	}{
		{"401 刷新令牌后同账号重试成功", map[string][]int{"a": {401}}, "a", map[string]int{"a": 2, "b": 0}},
		{"刷新后仍 401 换号", map[string][]int{"a": {401, 401}}, "b", map[string]int{"a": 2, "b": 1}},
		{"429 冷却后换号", map[string][]int{"a": {429}}, "b", map[string]int{"a": 1, "b": 1}},
		{"503 冷却后换号", map[string][]int{"a": {503}}, "b", map[string]int{"a": 1, "b": 1}},
		{"连接断开换号", map[string][]int{"a": {dropConn}}, "b", map[string]int{"a": 1, "b": 1}},
		{"5xx 换号", map[string][]int{"a": {500}}, "b", map[string]int{"a": 1, "b": 1}},
		{"200 但正文是 503 错误，同样换号", map[string][]int{"a": {inBand503}}, "b", map[string]int{"a": 1, "b": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "a", "b")
			e.up.script = tc.script
			status, body, rt, err := e.generate("gemini-2.5-flash", false)
			if err != nil || status != http.StatusOK || rt != tc.servedBy {
				t.Fatalf("期望 %s 返回 200，实际 %s %d %v %s", tc.servedBy, rt, status, err, body)
			}
			if !strings.Contains(body, "from "+tc.servedBy) || strings.Contains(body, `"response"`) {
				t.Fatalf("响应体应是 %s 的解包结果：%s", tc.servedBy, body)
			}
			for acc, want := range tc.genCalls {
				if _, got := e.up.calls(acc); got != want {
					t.Fatalf("账号 %s 生成调用应为 %d 次，实际 %d", acc, want, got)
				}
			}
		})
	}
}

func TestUnauthorizedRefreshesToken(t *testing.T) {
	e := newEnv(t, "a")
	e.up.script["a"] = []int{401}
	if status, _, _, err := e.generate("gemini-2.5-flash", false); err != nil || status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d %v", status, err)
	}
	if token, _ := e.up.calls("a"); token != 2 {
		t.Fatalf("401 后应重新刷新令牌，令牌端点应调用 2 次，实际 %d", token)
	}
}

// 冷却按"账号 × 模型"生效：冷却中的账号不再接这个模型的请求，同族其它模型不受影响。
func TestCooldownByModel(t *testing.T) {
	for _, code := range []int{429, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			e := newEnv(t, "a", "b")
			e.up.script["a"] = []int{code}
			if _, _, rt, _ := e.generate("gemini-2.5-flash", false); rt != "b" {
				t.Fatalf("应换到 b，实际 %s", rt)
			}
			if got := e.servedBy("gemini-2.5-flash", 3); got["a"] != 0 {
				t.Fatalf("a 的 gemini-2.5-flash 冷却中不应再被选中，实际 %v", got)
			}
			if got := e.servedBy("gemini-3-pro-high", 4); got["a"] == 0 || got["b"] == 0 {
				t.Fatalf("同族其它模型不受影响，应在两个账号间轮询，实际 %v", got)
			}
		})
	}
}

// relayError 断言 err 是 *Error 并返回它。
func relayError(t *testing.T, err error) *Error {
	t.Helper()
	var re *Error
	if !errors.As(err, &re) {
		t.Fatalf("应返回 *Error，实际 %T %v", err, err)
	}
	return re
}

// 全部限流：返回 429 和最早恢复时间；之后全部冷却中时直接返回 429，不再打上游。
func TestAllAccountsLimited(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.up.script = map[string][]int{"a": {429}, "b": {429}}
	_, _, _, err := e.generate("gemini-2.5-flash", false)
	re := relayError(t, err)
	if re.Status != http.StatusTooManyRequests || re.RetryAfter < 85*time.Second || re.RetryAfter > 91*time.Second {
		t.Fatalf("全部限流时应返回 429 和约 90 秒的 Retry-After，实际 %d %v", re.Status, re.RetryAfter)
	}
	_, _, _, err = e.generate("gemini-2.5-flash", false)
	if re := relayError(t, err); re.Status != http.StatusTooManyRequests || re.RetryAfter < 85*time.Second || !errors.Is(err, ErrNoAccount) {
		t.Fatalf("全部冷却中时应直接返回 429，实际 %d %v %v", re.Status, re.RetryAfter, err)
	}
	if _, a := e.up.calls("a"); a != 1 {
		t.Fatalf("冷却中不应再调用上游，a 被调用 %d 次", a)
	}
}

func TestNoAccount(t *testing.T) {
	e := newEnv(t)
	_, _, _, err := e.generate("gemini-2.5-flash", false)
	if re := relayError(t, err); re.Status != http.StatusServiceUnavailable || !errors.Is(err, ErrNoAccount) {
		t.Fatalf("没有账号时应返回 503（ErrNoAccount），实际 %d %v", re.Status, err)
	}
}

// 最多尝试 MaxAttempts 个账号，可配置；全是 5xx 时返回 503。
func TestMaxAttempts(t *testing.T) {
	for _, attempts := range []int{3, 2} {
		e := newEnv(t, "a", "b", "c", "d")
		p := DefaultPolicy()
		p.MaxAttempts = attempts
		e.relay.SetPolicy(p)
		e.up.script = map[string][]int{"a": {500}, "b": {500}, "c": {500}, "d": {500}}
		_, _, _, err := e.generate("gemini-2.5-flash", false)
		if re := relayError(t, err); re.Status != http.StatusServiceUnavailable {
			t.Fatalf("全部 5xx 时应返回 503，实际 %d", re.Status)
		}
		total := 0
		for _, acc := range []string{"a", "b", "c", "d"} {
			_, n := e.up.calls(acc)
			total += n
		}
		if total != attempts {
			t.Fatalf("最多尝试 %d 个账号，实际调用 %d 次", attempts, total)
		}
	}
}

// 请求本身的错误（4xx）不换号，保留状态码和上游说明，并记一条用量。
func TestClientErrorReturned(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.up.script["a"] = []int{400}
	ctx := logx.WithRequestID(context.Background(), "req-400")
	_, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: map[string]any{}})
	re := relayError(t, err)
	if re.Status != http.StatusBadRequest || re.Code != "INVALID_ARGUMENT" || re.Message != "scripted" {
		t.Fatalf("应返回 400 和上游说明，实际 %+v", re)
	}
	if _, n := e.up.calls("b"); n != 0 {
		t.Fatalf("400 不应换号，b 被调用了 %d 次", n)
	}
	if err := e.relay.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, _ := e.store.RecentUsage(context.Background(), 10)
	if len(records) != 1 || records[0].StatusCode != 400 || records[0].RequestID != "req-400" || records[0].AccountID != e.ids["a"] {
		t.Fatalf("应记一条 400 的用量：%+v", records)
	}
}

// 403 不换号、改写成 503；条款封禁停用账号，需要验证、无权使用模型冷却该模型，原因不明的不动账号。
func TestForbiddenHandling(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		disabled string        // 期望的停用原因，空表示不停用
		cooldown time.Duration // 期望该模型的冷却，0 表示没有
	}{
		{"条款封禁", tos403, ReasonTOSViolation, 0},
		{"需要验证", validation403, "", 10 * time.Minute},
		{"无权使用模型", modelForbidden403, "", 5 * time.Minute},
		{"原因不明", unknown403, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "a", "b")
			e.up.script["a"] = []int{tc.code}
			_, _, _, err := e.generate("gemini-2.5-flash", false)
			if re := relayError(t, err); re.Status != http.StatusServiceUnavailable || strings.Contains(re.Message, "Terms") {
				t.Fatalf("403 应改写成 503 且不带上游细节，实际 %+v", re)
			}
			if _, n := e.up.calls("b"); n != 0 {
				t.Fatalf("403 不应在本次请求里换号，b 被调用 %d 次", n)
			}
			acc, _ := e.store.GetAccount(context.Background(), e.enc, e.ids["a"])
			if tc.disabled != "" {
				if acc.Status != store.StatusDisabled || acc.DisabledReason != tc.disabled {
					t.Fatalf("应停用 a 并记原因 %q，实际 %s %q", tc.disabled, acc.Status, acc.DisabledReason)
				}
				if got := e.servedBy("gemini-2.5-flash", 2); got["a"] != 0 {
					t.Fatalf("停用后不应再选 a：%v", got)
				}
				return
			}
			if acc.Status != store.StatusEnabled {
				t.Fatalf("不应停用 a：%s", acc.Status)
			}
			cds := e.relay.Cooldowns()[e.ids["a"]]
			if tc.cooldown == 0 {
				if len(cds) != 0 {
					t.Fatalf("不应冷却：%+v", cds)
				}
				return
			}
			if left := time.Until(cds[0].Until); len(cds) != 1 || cds[0].Model != "gemini-2.5-flash" || left < tc.cooldown-5*time.Second || left > tc.cooldown {
				t.Fatalf("应冷却 gemini-2.5-flash 约 %v：%+v", tc.cooldown, cds)
			}
		})
	}
}

// 瞬时限流（不带恢复时间）：冷却从 RateLimitBase 起，同账号连续触发翻倍。
func TestTransientRateLimitBackoff(t *testing.T) {
	e := newEnv(t, "a")
	p := DefaultPolicy()
	p.RateLimitBase, p.RateLimitMax = 300*time.Millisecond, 2*time.Second
	p.CooldownWait = 0 // 冷却很短，开着等冷却就会等到再试
	e.relay.SetPolicy(p)
	e.up.script["a"] = []int{transient429, transient429}

	cooldownOfA := func() time.Duration {
		t.Helper()
		cds := e.relay.Cooldowns()[e.ids["a"]]
		if len(cds) != 1 {
			t.Fatalf("a 应有一条冷却：%+v", cds)
		}
		return time.Until(cds[0].Until)
	}
	_, _, _, err := e.generate("gemini-2.5-flash", false)
	if re := relayError(t, err); re.Status != http.StatusTooManyRequests {
		t.Fatalf("唯一的账号被限流时应返回 429，实际 %d", re.Status)
	}
	if d := cooldownOfA(); d > 300*time.Millisecond || d < 200*time.Millisecond {
		t.Fatalf("第一次应冷却约 300ms，实际 %v", d)
	}
	time.Sleep(350 * time.Millisecond)
	_, _, _, _ = e.generate("gemini-2.5-flash", false)
	if d := cooldownOfA(); d > 600*time.Millisecond || d < 450*time.Millisecond {
		t.Fatalf("连续第二次应冷却约 600ms，实际 %v", d)
	}
}

// refresh token 失效（invalid_grant）的账号被停用并记原因，本次请求换号成功。
func TestInvalidGrantDisablesAccount(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.up.revoked["a"] = true
	got := e.servedBy("gemini-2.5-flash", 3)
	if got["b"] != 3 {
		t.Fatalf("应全部由 b 服务，实际 %v", got)
	}
	acc, _ := e.store.GetAccount(context.Background(), e.enc, e.ids["a"])
	if acc.Status != store.StatusDisabled || acc.DisabledReason != ReasonInvalidGrant {
		t.Fatalf("a 应被停用并记原因，实际 %s %q", acc.Status, acc.DisabledReason)
	}
	if token, _ := e.up.calls("a"); token != 1 {
		t.Fatalf("停用后不应再刷新 a 的令牌，实际刷新 %d 次", token)
	}
}

// 响应关闭时归还并发名额。
func TestResponseCloseReleasesSlot(t *testing.T) {
	e := newEnv(t, "a")
	resp, err := e.relay.Generate(context.Background(), Request{Model: "gemini-2.5-flash", Body: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.relay.pool.InFlight(e.ids["a"]); n != 1 {
		t.Fatalf("读响应期间应占 1 个名额，实际 %d", n)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Close()
	if n := e.relay.pool.InFlight(e.ids["a"]); n != 0 {
		t.Fatalf("关闭后应归还名额，实际 %d", n)
	}
}

// refresh token 轮换后落库，并刷新号池，下次刷新用新 refresh token。
func TestRefreshTokenRotationPersisted(t *testing.T) {
	e := newEnv(t, "a")
	e.up.rotate["a"] = "a2"
	if status, _, _, err := e.generate("gemini-2.5-flash", false); err != nil || status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d %v", status, err)
	}
	acc, err := e.store.GetAccount(context.Background(), e.enc, e.ids["a"])
	if err != nil || acc.RefreshToken != "a2" {
		t.Fatalf("轮换后的 refresh token 应已落库，实际 %v %v", acc, err)
	}

	e.relay.InvalidateToken(e.ids["a"])
	if status, _, _, err := e.generate("gemini-2.5-flash", false); err != nil || status != http.StatusOK {
		t.Fatalf("期望 200，实际 %d %v", status, err)
	}
	if token, _ := e.up.calls("a2"); token != 1 {
		t.Fatalf("号池应已换上新 refresh token，用 a2 刷新 1 次，实际 %d", token)
	}
}

// 用量记录在 Close 时写入（失败的请求由 relay 直接记），带请求 ID、账号、状态码和 token 数；重复 Close 只记一次。
// 客户端名称过滤：替换模式让上游收到替换后的 system 提示词；拦截模式命中时返回 400、不发上游、记一条用量。
func TestClientFilterRewriteAndBlock(t *testing.T) {
	sysBody := func() map[string]any {
		return map[string]any{
			"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "You are Cline."}}},
		}
	}

	// 替换：上游收到的 systemInstruction 已把 Cline 换成 Antigravity。
	e := newEnv(t, "a")
	e.relay.SetClientFilter(antigravity.NewClientFilter(antigravity.FilterRewrite, nil, true))
	status, _, _, err := e.generateBody("gemini-2.5-flash", false, sysBody())
	if err != nil || status != http.StatusOK {
		t.Fatalf("替换模式请求应成功：status=%d err=%v", status, err)
	}
	req := e.up.last("a")["request"].(map[string]any)
	got := req["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
	if got != "You are Antigravity." {
		t.Fatalf("上游应收到替换后的提示词，实际 %q", got)
	}

	// 拦截：命中即返回 400，不调用上游。
	e2 := newEnv(t, "a")
	e2.relay.SetClientFilter(antigravity.NewClientFilter(antigravity.FilterBlock, nil, true))
	ctx := logx.WithRequestID(context.Background(), "req-block")
	_, blockErr := e2.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: sysBody(), APIKeyID: 7})
	var re *Error
	if !errors.As(blockErr, &re) || re.Status != http.StatusBadRequest {
		t.Fatalf("拦截应返回 400 *Error，实际 %v", blockErr)
	}
	if token, gen := e2.up.calls("a"); token != 0 || gen != 0 {
		t.Fatalf("拦截不应调用上游，实际 token=%d gen=%d", token, gen)
	}
	if err := e2.relay.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := e2.store.RecentUsage(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].StatusCode != http.StatusBadRequest {
		t.Fatalf("拦截应记一条 400 用量，实际 %+v", records)
	}
}

// 系统提示词伪装：as_user 模式下上游收到的 system 是注入的模板，客户端原 system 转成开头的 user 消息。
func TestSystemPromptInjection(t *testing.T) {
	e := newEnv(t, "a")
	e.relay.SetSystemPrompt(antigravity.SystemPromptAsUser, "OFFICIAL BASELINE")
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "You are Cline."}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
	}
	if status, _, _, err := e.generateBody("gemini-2.5-flash", false, body); err != nil || status != http.StatusOK {
		t.Fatalf("请求应成功：status=%d err=%v", status, err)
	}
	req := e.up.last("a")["request"].(map[string]any)
	sys := req["systemInstruction"].(map[string]any)["parts"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["text"] != "OFFICIAL BASELINE" {
		t.Fatalf("上游 system 应只有注入的模板，实际 %v", sys)
	}
	contents := req["contents"].([]any)
	if len(contents) != 2 || contents[0].(map[string]any)["role"] != "user" {
		t.Fatalf("客户端原 system 应转成开头的 user 消息，实际 %v", contents)
	}
	if contents[0].(map[string]any)["parts"].([]any)[1].(map[string]any)["text"] != "You are Cline." {
		t.Fatalf("开头 user 消息应带上客户端原 system")
	}
}

// 系统提示词伪装 as_user 空模板：不注入模板，只把客户端 system 降为开头的 user 消息，systemInstruction 清空。
func TestSystemPromptAsUserEmptyTemplate(t *testing.T) {
	e := newEnv(t, "a")
	e.relay.SetSystemPrompt(antigravity.SystemPromptAsUser, "")
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "You are Cline."}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
	}
	if status, _, _, err := e.generateBody("gemini-2.5-flash", false, body); err != nil || status != http.StatusOK {
		t.Fatalf("请求应成功：status=%d err=%v", status, err)
	}
	req := e.up.last("a")["request"].(map[string]any)
	if _, ok := req["systemInstruction"]; ok {
		t.Fatalf("上游 system 应清空，实际 %v", req["systemInstruction"])
	}
	contents := req["contents"].([]any)
	if len(contents) != 2 || contents[0].(map[string]any)["role"] != "user" {
		t.Fatalf("客户端原 system 应转成开头的 user 消息，实际 %v", contents)
	}
	if contents[0].(map[string]any)["parts"].([]any)[1].(map[string]any)["text"] != "You are Cline." {
		t.Fatalf("开头 user 消息应带上客户端原 system")
	}
}

// 返回思考内容开关：关闭后请求侧 thinkingConfig.includeThoughts 置 false。
func TestHideThoughtsRequestSide(t *testing.T) {
	e := newEnv(t, "a")
	e.relay.SetRequestHandling(false, false) // 不内联图片、不返回思考
	body := map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
		"generationConfig": map[string]any{"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingBudget": 512}},
	}
	if status, _, _, err := e.generateBody("gemini-2.5-flash", false, body); err != nil || status != http.StatusOK {
		t.Fatalf("请求应成功：status=%d err=%v", status, err)
	}
	req := e.up.last("a")["request"].(map[string]any)
	tc := req["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if tc["includeThoughts"] != false {
		t.Fatalf("上游应收到 includeThoughts=false，实际 %v", tc["includeThoughts"])
	}
}

func TestUsageRecordedOnClose(t *testing.T) {
	e := newEnv(t, "a")
	e.up.script["a"] = []int{200, 200, 400}
	cases := []struct {
		reqID         string
		stream        bool
		status        int
		output, total int64
	}{
		{"req-plain", false, 200, 2, 5},
		{"req-stream", true, 200, 7, 10},
		{"req-error", false, 400, 0, 0},
	}
	for _, tc := range cases {
		ctx := logx.WithRequestID(context.Background(), tc.reqID)
		resp, err := e.relay.Generate(ctx, Request{Model: "gemini-2.5-flash", Body: map[string]any{}, Stream: tc.stream, APIKeyID: 9})
		if tc.status != 200 {
			if err == nil {
				t.Fatalf("请求 %s 应失败", tc.reqID)
			}
			continue // 失败的请求由 relay 记用量
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Close()
		resp.Close()
	}

	// 关闭执行层会写完排队的记录，之后直接查库。
	if err := e.relay.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := e.store.RecentUsage(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(cases) {
		t.Fatalf("应有 %d 条用量记录（重复 Close 不重复记），实际 %d", len(cases), len(records))
	}
	byID := map[string]*store.UsageRecord{}
	for _, r := range records {
		byID[r.RequestID] = r
	}
	for _, tc := range cases {
		r := byID[tc.reqID]
		if r == nil {
			t.Fatalf("缺少请求 %s 的用量记录", tc.reqID)
		}
		if r.StatusCode != tc.status || r.Stream != tc.stream || r.OutputTokens != tc.output ||
			r.TotalTokens != tc.total || r.AccountID != e.ids["a"] || r.APIKeyID != 9 {
			t.Fatalf("请求 %s 的用量记录不对：%+v", tc.reqID, r)
		}
		if tc.status == 200 && r.UpstreamModel != "upstream-model" {
			t.Fatalf("请求 %s 应记录上游实际模型，实际 %q", tc.reqID, r.UpstreamModel)
		}
		if r.ResolvedModel != "gemini-2.5-flash" {
			t.Fatalf("请求 %s 应记录实际发出的模型，实际 %q", tc.reqID, r.ResolvedModel)
		}
		// 首字延迟只有流式有；缓存和推理数来自上游用量，非 2xx 没有用量。
		if (r.FirstTokenMs != nil) != tc.stream {
			t.Fatalf("请求 %s 的首字延迟不对：%v", tc.reqID, r.FirstTokenMs)
		}
		if tc.stream && (*r.CachedTokens != 2 || *r.ThoughtsTokens != 4 || *r.FirstTokenMs > r.LatencyMs) {
			t.Fatalf("流式请求的缓存、推理、首字延迟不对：%+v", r)
		}
		if tc.reqID == "req-plain" && (r.CachedTokens == nil || *r.CachedTokens != 0 || *r.ThoughtsTokens != 1) {
			t.Fatalf("非流式请求的缓存、推理数不对：%+v", r)
		}
		if tc.status != 200 && r.ThoughtsTokens != nil {
			t.Fatalf("没有用量时推理数应为空：%+v", r)
		}
	}
}

// 家族名按账号目录和思考参数解析成精确 ID 发给上游；分档模型不带客户端的思考强度。
func TestGenerateResolvesFamilyModel(t *testing.T) {
	e := newEnv(t, "a")
	e.setCatalog("a", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high")

	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"没有思考参数用默认档位", map[string]any{}, "gemini-3.8-flash-medium"},
		{"thinkingLevel 选档", map[string]any{"generationConfig": map[string]any{
			"thinkingConfig": map[string]any{"thinkingLevel": "HIGH", "includeThoughts": true}}}, "gemini-3.8-flash-high"},
		{"thinkingBudget 换算", map[string]any{"generationConfig": map[string]any{
			"thinkingConfig": map[string]any{"thinkingBudget": 512.0}}}, "gemini-3.8-flash-low"},
	}
	for _, tc := range cases {
		if status, body, _, err := e.generateBody("gemini-3.8-flash", false, tc.body); err != nil || status != http.StatusOK {
			t.Fatalf("%s：%d %v %s", tc.name, status, err, body)
		}
		env := e.up.last("a")
		if env["model"] != tc.want {
			t.Fatalf("%s：上游模型应为 %s，实际 %v", tc.name, tc.want, env["model"])
		}
		raw, _ := json.Marshal(env["request"])
		if strings.Contains(string(raw), "thinkingLevel") || strings.Contains(string(raw), "thinkingBudget") {
			t.Fatalf("%s：分档模型不应带客户端的思考强度：%s", tc.name, raw)
		}
	}
	// 客户端的请求体不应被改动（每次尝试都在副本上转换）。
	body := cases[1].body["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if body["thinkingLevel"] != "HIGH" {
		t.Fatalf("客户端请求体被改动了：%v", body)
	}

	_, _, _, err := e.generate("gemini-9-flash", false)
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("目录里没有的模型应返回 ErrModelNotFound，实际 %v", err)
	}
	if _, n := e.up.calls("a"); n != len(cases) {
		t.Fatalf("模型不存在时不应调用上游，实际生成调用 %d 次", n)
	}
}

// 按家族单独设置的档位优先于默认档位；请求带思考参数时以参数为准。
func TestConfiguredTierPerFamily(t *testing.T) {
	e := newEnv(t, "a")
	e.setCatalog("a", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high",
		"claude-opus-5-5-low", "claude-opus-5-5-medium", "claude-opus-5-5-high")
	e.relay.SetDefaultTier("low")
	e.relay.SetModelTiers(map[string]string{"gemini-3.8-flash": "high"})

	cases := []struct {
		model string
		body  map[string]any
		want  string
	}{
		{"gemini-3.8-flash", map[string]any{}, "gemini-3.8-flash-high"},
		{"claude-opus-5-5", map[string]any{}, "claude-opus-5-5-low"},
		{"gemini-3.8-flash", map[string]any{"generationConfig": map[string]any{
			"thinkingConfig": map[string]any{"thinkingLevel": "MEDIUM"}}}, "gemini-3.8-flash-medium"},
	}
	for _, tc := range cases {
		if status, body, _, err := e.generateBody(tc.model, false, tc.body); err != nil || status != http.StatusOK {
			t.Fatalf("%s：%d %v %s", tc.model, status, err, body)
		}
		if got := e.up.last("a")["model"]; got != tc.want {
			t.Fatalf("%s %v：上游模型应为 %s，实际 %v", tc.model, tc.body, tc.want, got)
		}
	}
	if m, ok := e.relay.LookupModel(context.Background(), "gemini-3.8-flash"); !ok || m.ID != "gemini-3.8-flash-high" {
		t.Fatalf("查模型也应按家族设置解析，实际 %+v %v", m, ok)
	}
	families := e.relay.Families()
	if len(families) != 2 || len(families["claude-opus-5-5"]) != 3 {
		t.Fatalf("家族列表不对：%v", families)
	}
}

// 没有账号能解析请求的模型时先刷新模型目录再选号；刷新限频，期间再遇到未知模型直接 404。
func TestUnknownModelRefreshesCatalog(t *testing.T) {
	e := newEnv(t, "a")
	e.setCatalog("a", "gemini-2.5-flash")
	e.up.catalogs["a"] = []string{"gemini-2.5-flash", "gemini-9-flash-low", "gemini-9-flash-high"}

	status, body, _, err := e.generate("gemini-9-flash", false)
	if err != nil || status != http.StatusOK {
		t.Fatalf("刷新目录后应能请求新模型：%d %v %s", status, err, body)
	}
	if got := e.up.last("a")["model"]; got != "gemini-9-flash-low" {
		t.Fatalf("应按刷新后的目录解析（默认 medium 缺失往低取 low），实际 %v", got)
	}
	acc, err := e.store.GetAccount(context.Background(), e.enc, e.ids["a"])
	if err != nil || !strings.Contains(acc.ModelsJSON, "gemini-9-flash-high") {
		t.Fatalf("刷新后的目录应落库：%v %v", acc, err)
	}

	// 限频期间再遇到未知模型不再刷新。
	if _, _, _, err := e.generate("gemini-10-pro", false); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("应返回 ErrModelNotFound，实际 %v", err)
	}
	if _, ok := e.relay.LookupModel(context.Background(), "gemini-10-pro"); ok {
		t.Fatal("不存在的模型不应解析成功")
	}
	e.up.mu.Lock()
	calls := e.up.fetchCalls
	e.up.mu.Unlock()
	if calls != 1 {
		t.Fatalf("限频期间只应刷新 1 次，实际 %d 次", calls)
	}

	// 过了限频间隔可以再刷新。
	e.relay.catalogMu.Lock()
	e.relay.lastCatalogRefresh = time.Now().Add(-catalogRefreshInterval)
	e.relay.catalogMu.Unlock()
	e.up.catalogs["a"] = append(e.up.catalogs["a"], "gemini-10-pro")
	if m, ok := e.relay.LookupModel(context.Background(), "gemini-10-pro"); !ok || m.ID != "gemini-10-pro" {
		t.Fatalf("间隔过后查模型应触发刷新并找到新模型，实际 %+v %v", m, ok)
	}
}

// 每个账号用自己的设备画像 UA：保存了的用保存的，没保存的按邮箱派生。
func TestGenerateUsesAccountUserAgent(t *testing.T) {
	e := newEnv(t, "a", "b", "c")
	const stored = "antigravity/hub/2.31.0 (aidev_client; os_type=linux; arch=amd64; cl=1017595969)"
	acc, err := e.store.GetAccount(context.Background(), e.enc, e.ids["c"])
	if err != nil {
		t.Fatal(err)
	}
	acc.UserAgent = stored
	if err := e.store.UpdateAccountSync(context.Background(), e.enc, acc); err != nil {
		t.Fatal(err)
	}
	if err := e.relay.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if status, body, _, err := e.generate("gemini-2.5-flash", false); err != nil || status != http.StatusOK {
			t.Fatalf("%d %v %s", status, err, body)
		}
	}
	want := map[string]string{
		"a": antigravity.DeviceUserAgent("a@example.com"),
		"b": antigravity.DeviceUserAgent("b@example.com"),
		"c": stored,
	}
	for rt, w := range want {
		e.up.mu.Lock()
		got := e.up.lastUA[rt]
		e.up.mu.Unlock()
		if got != w {
			t.Fatalf("账号 %s 应用 %q，实际 %q", rt, w, got)
		}
	}
}

// 目录里的模型缺枚举名（升级前存下的旧目录）时先刷新一次目录；补上之后不再刷新。
func TestMissingEnumRefreshesCatalog(t *testing.T) {
	e := newEnv(t, "a")
	e.setCatalog("a", "gemini-2.5-flash") // 旧目录，没有枚举名
	e.up.catalogs["a"] = []string{"gemini-2.5-flash"}

	fetches := func() int {
		e.up.mu.Lock()
		defer e.up.mu.Unlock()
		return e.up.fetchCalls
	}
	if status, body, _, err := e.generate("gemini-2.5-flash", false); err != nil || status != http.StatusOK {
		t.Fatalf("%d %v %s", status, err, body)
	}
	if fetches() != 1 {
		t.Fatalf("缺枚举名时应刷新一次目录，实际 %d 次", fetches())
	}
	acc, err := e.store.GetAccount(context.Background(), e.enc, e.ids["a"])
	if err != nil || !strings.Contains(acc.ModelsJSON, `"enum":"ENUM_gemini-2.5-flash"`) {
		t.Fatalf("刷新后的目录应带枚举名：%v %v", acc, err)
	}

	// 补上枚举名后，即使过了限频间隔也不再刷新。
	e.relay.catalogMu.Lock()
	e.relay.lastCatalogRefresh = time.Now().Add(-catalogRefreshInterval)
	e.relay.catalogMu.Unlock()
	if status, _, _, err := e.generate("gemini-2.5-flash", false); err != nil || status != http.StatusOK {
		t.Fatalf("%d %v", status, err)
	}
	if fetches() != 1 {
		t.Fatalf("目录完整时不应再刷新，实际 %d 次", fetches())
	}
}
