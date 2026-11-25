package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shiceai/agy2api/internal/logx"
	"github.com/shiceai/agy2api/internal/relay"
)

// 假流式：模型名带 假流式/ 前缀的流式请求，上游改用非流式（官方客户端也会发的普通 generateContent），
// 拿到完整结果后作为一个 SSE 事件返回（Gemini 流式的每一块和非流式响应结构相同，一块就是合法的流）。
// 流式不稳定时用。非流式、计数请求带这个前缀时只去掉前缀。模型列表里不列带前缀的模型。
const fakeStreamPrefix = "假流式/"

// fakeStreamWait 是先等结果的时长：这之内有结果就按正常结果返回，失败也能返回正确的状态码；
// 超过后先回 200，之后每隔这么久发一行 SSE 注释保活（SSE 规范里注释会被客户端忽略），失败时发错误事件。
var fakeStreamWait = 10 * time.Second

// stripFakeStream 去掉模型名的假流式前缀，返回去掉后的模型名和是否带前缀。
func stripFakeStream(model string) (string, bool) {
	base, ok := strings.CutPrefix(model, fakeStreamPrefix)
	return base, ok
}

type generateResult struct {
	resp *relay.Response
	err  error
}

// handleFakeStream 处理假流式请求，model 是去掉前缀并解析别名后的模型名，requested 是客户端原来的模型名。
func (s *Service) handleFakeStream(w http.ResponseWriter, r *http.Request, model, requested string, start time.Time) {
	req := inboundRequest(r, model, requested, false)
	req.FakeStream = true
	if s.quotaExceeded(w, r, req, start) {
		return
	}
	body, ok := readGeminiRequest(w, r)
	if !ok {
		return
	}
	req.Body = body

	done := make(chan generateResult, 1)
	go func() {
		resp, err := s.relay.Generate(r.Context(), req)
		done <- generateResult{resp, err}
	}()
	// 提前返回时（客户端断开）结果还会到，要关闭响应：归还并发名额、写用量。
	drain := func() {
		go func() {
			if res := <-done; res.resp != nil {
				res.resp.Close()
			}
		}()
	}

	timer := time.NewTimer(fakeStreamWait)
	defer timer.Stop()
	var res generateResult
	select {
	case res = <-done:
		if res.err != nil {
			writeRelayError(w, r, res.err, model, true)
			return
		}
		startSSE(w)
	case <-timer.C:
		startSSE(w)
		flush(w)
		ticker := time.NewTicker(fakeStreamWait)
		defer ticker.Stop()
	wait:
		for {
			select {
			case res = <-done:
				break wait
			case <-ticker.C:
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					drain()
					return
				}
				flush(w)
			case <-r.Context().Done():
				drain()
				return
			}
		}
		if res.err != nil {
			writeStreamRelayError(w, r, res.err, model)
			flush(w)
			return
		}
	}
	defer res.resp.Close()

	// 非流式结果已经整个在内存里（大小上限在 antigravity）。SSE 的 data 不能跨行，上游的 JSON 可能带换行，压成一行。
	var line bytes.Buffer
	raw, err := io.ReadAll(res.resp.Body)
	if err == nil {
		err = json.Compact(&line, raw)
	}
	if err != nil {
		slog.WarnContext(r.Context(), "假流式：上游响应不是合法 JSON", "err", err)
		writeSSEError(w, http.StatusBadGateway, "UNAVAILABLE", "upstream returned an invalid response")
		flush(w)
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", line.Bytes())
	flush(w)
}

// writeStreamRelayError 在已经开始的 SSE 流里报告生成失败（状态码已经是 200），内容和 writeRelayError 一样。
func writeStreamRelayError(w http.ResponseWriter, r *http.Request, err error, model string) {
	var re *relay.Error
	if !errors.As(err, &re) {
		re = &relay.Error{Status: http.StatusServiceUnavailable, Code: "UNAVAILABLE", Message: "upstream is temporarily unavailable, retry later", Err: err}
	}
	slog.WarnContext(r.Context(), "生成请求失败", "model", model, "stream", true, "fake_stream", true,
		"status", re.Status, "err", re.Err)
	msg := re.Message
	if re.Status >= 500 {
		msg += " (request id: " + logx.RequestID(r.Context()) + ")"
	}
	writeSSEError(w, re.Status, re.Code, msg)
}
