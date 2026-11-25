package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const errorBodyLimit = 1 << 20

// nonStreamBodyLimit 是非流式生成响应体的上限。生图模型（目录里有 gemini-3.1-flash-image）把图片 base64 内联在
// 响应里，多张或高分辨率时远超几 MiB；流式每一帧本来就不限大小，非流式不应更严。变量是为了测试时调小。
var nonStreamBodyLimit int64 = 64 << 20

// ErrResponseTooLarge 表示非流式生成响应体超过 nonStreamBodyLimit。不截断（截断的 JSON 客户端解析不了）；
// 生成已经完成、额度已经花掉，换号重试只会再超一次，调用方应直接报错并建议改用流式。
var ErrResponseTooLarge = errors.New("upstream non-streaming response exceeds the size limit")

// Usage 是上游返回的 token 用量。
type Usage struct {
	// PromptTokens 是输入 token 总数，含命中缓存的部分（CachedTokens）。
	PromptTokens int64
	// OutputTokens 是回复 token（candidatesTokenCount），不含推理部分。
	OutputTokens int64
	// CachedTokens 是命中缓存读取的输入 token（cachedContentTokenCount）。上游不报缓存写入。
	CachedTokens int64
	// ThoughtsTokens 是推理（思考）token（thoughtsTokenCount）。
	ThoughtsTokens int64
	TotalTokens    int64
	// UpstreamModel 是上游实际执行的模型（响应里的 modelVersion），可能与请求模型不同。
	UpstreamModel string
}

// Response 是一次上游生成调用的结果。
// 对 2xx：Body 已解包为标准 Gemini 形态（非流式为完整 JSON，流式为逐条解包的 SSE）。
// 对非 2xx：Body 为上游原始错误体，供调用方分类（如 429 冷却）。
type Response struct {
	Status int
	Proto  string // 上游响应的 HTTP 版本，如 HTTP/2.0
	Header http.Header
	Body   io.ReadCloser
	// Usage 仅非流式时在此直接可用；流式用量在流读完后由 StreamUsage 提供。
	Usage *Usage
	// HeaderAt 是收到上游响应头的时间。
	HeaderAt time.Time
	stream   *sseUnwrapper
	// 非流式在解包时读出；流式见 Marks。
	responseID string
	traceID    string
}

// traceHeader 是 Cloud Code 在响应头里给的 trace ID（出错时也有），向上游排查时用。
const traceHeader = "X-Cloudaicompanion-Trace-Id"

// Marks 是流式读取过程中记下的时间点（非流式为零值），以及上游的响应 ID 和 trace ID。
// 流式要在读完后调用才完整。
type Marks struct {
	FirstEventAt time.Time // 第一个上游帧
	FirstTextAt  time.Time // 第一个正文：非思考的文本或工具调用
	ResponseID   string
	TraceID      string
}

// ServerTiming 返回上游在响应头 Server-Timing 里自报的处理时长（Google 前端测得，形如 "gfet4t7; dur=8354"），
// 没有时 ok 为 false。2026-11-23 实测：非流式约等于整个生成耗时；流式响应头在第一段内容出来时发出，接近首字时间。
func (r *Response) ServerTiming() (time.Duration, bool) {
	for _, metric := range strings.Split(r.Header.Get("Server-Timing"), ",") {
		for _, param := range strings.Split(metric, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || k != "dur" {
				continue
			}
			if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
				return time.Duration(ms * float64(time.Millisecond)), true
			}
		}
	}
	return 0, false
}

// Marks 返回时间点和上游 ID。trace ID 优先取响应头，没有时取信封里的 traceId。
func (r *Response) Marks() Marks {
	m := Marks{ResponseID: r.responseID, TraceID: r.traceID}
	if r.stream != nil {
		var respID, traceID string
		m.FirstEventAt, m.FirstTextAt, respID, traceID = r.stream.marks()
		m.ResponseID = respID
		if m.TraceID == "" {
			m.TraceID = traceID
		}
	}
	return m
}

// FirstTokenAt 返回流式响应里第一个带内容的帧被读到的时间；非流式或还没有内容时为零值。
func (r *Response) FirstTokenAt() time.Time {
	if r.stream == nil {
		return time.Time{}
	}
	return r.stream.firstToken()
}

// LastRead 返回流式响应最近一次从上游读到数据的时间（含空事件、注释），非流式返回零值。
func (r *Response) LastRead() time.Time {
	if r.stream == nil {
		return time.Time{}
	}
	return time.Unix(0, r.stream.lastRead.Load())
}

// StreamError 返回流式响应中途上游推来的错误帧，没有时（或非流式）为 nil。流读到错误帧时 Body 的 Read 也返回它；
// 要在读取结束后、和读取同一个协程里调用。
func (r *Response) StreamError() *StreamError {
	if r.stream == nil {
		return nil
	}
	return r.stream.streamError()
}

// StreamUsage 在流式响应读取结束后返回累计用量（非流式返回 nil）。
func (r *Response) StreamUsage() *Usage {
	if r.stream == nil {
		return nil
	}
	return r.stream.usage()
}

// Generate 向 Cloud Code v1internal 发送生成请求，基址按数据面策略回退（见 basePolicy）。
// proxy 是账号绑定的代理（空为直连）；userAgent 是该账号实际要用的 User-Agent（见 Client.UserAgentFor），空时用默认值；
// payload 由 BuildEnvelope 生成。
// 拿到响应（含非 2xx）就返回，交给调用方分类；所有基址都连不上才返回 error。流式由 alt=sse 决定。
func (c *Client) Generate(ctx context.Context, proxy, accessToken, userAgent string, payload []byte, stream, hideThoughts bool) (*Response, error) {
	rt, err := c.route(proxy)
	if err != nil {
		return nil, err
	}
	if userAgent == "" {
		userAgent = c.UserAgentFor("", "")
	}
	method, query := "generateContent", ""
	if stream {
		method, query = "streamGenerateContent", "?alt=sse"
	}
	resp, err := c.postWithFallback(ctx, rt.data, dataPlane, method, query, cloudCodeHeaders(accessToken, userAgent), payload)
	if err != nil {
		return nil, err
	}
	headerAt := time.Now()
	out, err := buildResponse(resp, stream, hideThoughts)
	if out != nil {
		out.HeaderAt = headerAt
	}
	return out, err
}

// hideThoughts 为真时从响应里去掉思考 part（上游不认请求侧的 includeThoughts=false）。
func buildResponse(resp *http.Response, stream, hideThoughts bool) (*Response, error) {
	out := &Response{Status: resp.StatusCode, Proto: resp.Proto, Header: resp.Header, traceID: resp.Header.Get(traceHeader)}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		resp.Body.Close()
		out.Body = io.NopCloser(bytes.NewReader(body))
		return out, nil
	}

	if stream {
		u := newSSEUnwrapper(resp.Body, hideThoughts)
		out.Body = u
		out.stream = u
		return out, nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, nonStreamBodyLimit+1))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > nonStreamBodyLimit {
		return nil, ErrResponseTooLarge
	}
	// 2xx 但正文是错误：当成那个状态码的错误响应交给调用方分类，和真正的非 2xx 一样处理。
	if ue, ok := inBandError(raw, resp.Header, time.Now()); ok {
		out.Status = ue.Status
		out.Body = io.NopCloser(bytes.NewReader(raw))
		return out, nil
	}
	gemini, usage := unwrapGeminiJSON(raw)
	if hideThoughts {
		gemini = stripThoughtParts(gemini)
	}
	out.Body = io.NopCloser(bytes.NewReader(gemini))
	out.Usage = usage
	out.responseID = gjson.GetBytes(gemini, "responseId").String()
	if out.traceID == "" {
		out.traceID = gjson.GetBytes(raw, "traceId").String()
	}
	return out, nil
}

// unwrapGeminiJSON 把 Cloud Code 信封 {"response": {...}} 解包为标准 Gemini JSON 并解析用量。
// 直接截取上游原始字节，不整体解码再编码，保持上游的字段顺序和转义。
func unwrapGeminiJSON(raw []byte) ([]byte, *Usage) {
	inner := raw
	if r := gjson.GetBytes(raw, "response"); r.IsObject() {
		inner = []byte(r.Raw)
	}
	inner = restoreUsageMetadata(inner)
	return inner, parseUsage(inner)
}

// restoreUsageMetadata 把上游的 cpaUsageMetadata 还原为标准的 usageMetadata。
// 只在出现 cpaUsageMetadata 时改写，且只动顶层字段，嵌套内容保持原样。
func restoreUsageMetadata(inner []byte) []byte {
	cpa := gjson.GetBytes(inner, "cpaUsageMetadata")
	if !cpa.Exists() {
		return inner
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(inner, &fields) != nil {
		return inner
	}
	if _, ok := fields["usageMetadata"]; !ok {
		fields["usageMetadata"] = json.RawMessage(cpa.Raw)
	}
	delete(fields, "cpaUsageMetadata")
	out, err := json.Marshal(fields)
	if err != nil {
		return inner
	}
	return out
}

// parseUsage 读取 modelVersion 和 usageMetadata；两者都没有时返回 nil。
func parseUsage(inner []byte) *Usage {
	model := strings.TrimSpace(gjson.GetBytes(inner, "modelVersion").String())
	meta := gjson.GetBytes(inner, "usageMetadata")
	if !meta.IsObject() {
		if model == "" {
			return nil
		}
		return &Usage{UpstreamModel: model}
	}
	u := &Usage{
		UpstreamModel:  model,
		PromptTokens:   meta.Get("promptTokenCount").Int(),
		OutputTokens:   meta.Get("candidatesTokenCount").Int(),
		CachedTokens:   meta.Get("cachedContentTokenCount").Int(),
		ThoughtsTokens: meta.Get("thoughtsTokenCount").Int(),
		TotalTokens:    meta.Get("totalTokenCount").Int(),
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.OutputTokens + u.ThoughtsTokens
	}
	return u
}
