package antigravity

import (
	"encoding/json"
	"regexp"
	"strconv"
	"testing"
)

var (
	uuidPattern      = `[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`
	requestIDPattern = regexp.MustCompile(`^agent/(` + uuidPattern + `)/(\d{13})/(` + uuidPattern + `)/(\d+)$`)
)

type builtEnvelope struct {
	RequestID string `json:"requestId"`
	Model     string `json:"model"`
	Request   struct {
		SessionID string            `json:"sessionId"`
		Labels    map[string]string `json:"labels"`
	} `json:"request"`
}

func buildEnvelopeFor(t *testing.T, p BuildParams, body string) builtEnvelope {
	t.Helper()
	if err := json.Unmarshal([]byte(body), &p.GeminiRequest); err != nil {
		t.Fatal(err)
	}
	raw, err := BuildEnvelope(p)
	if err != nil {
		t.Fatal(err)
	}
	var env builtEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

const (
	firstTurn  = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	secondTurn = `{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"hello"}]},{"role":"user","parts":[{"text":"more"}]}]}`
)

// requestId 和 labels 对齐官方格式：新对话 requestId 末段为 1、labels 步进为 0；
// 同一对话后续轮次 ID 不变、步数随模型回复数增长。
func TestEnvelopeRequestIDAndLabels(t *testing.T) {
	p := BuildParams{ProjectID: "p", WireModel: "gemini-3.8-flash-high", AccountID: 7, Caller: "key:1",
		Model: &ModelInfo{ID: "gemini-3.8-flash-high", Enum: "MODEL_PLACEHOLDER_M318"}}

	first := buildEnvelopeFor(t, p, firstTurn)
	m := requestIDPattern.FindStringSubmatch(first.RequestID)
	if m == nil {
		t.Fatalf("requestId 格式不对：%s", first.RequestID)
	}
	cascade, trajectory, step := m[1], m[3], m[4]
	if cascade == trajectory || step != "1" {
		t.Fatalf("新对话 requestId 应为 agent/<会话>/<毫秒>/<轨迹>/1：%s", first.RequestID)
	}
	wantLabels := map[string]string{
		"cascade_id":               cascade,
		"root_cascade_id":          cascade,
		"trajectory_id":            trajectory,
		"request_id":               trajectory + "-0",
		"last_step_index":          "0",
		"model_enum":               "MODEL_PLACEHOLDER_M318",
		"used_claude":              "false",
		"used_claude_conservative": "false",
		"used_non_gemini_model":    "false",
	}
	if len(first.Request.Labels) != len(wantLabels) {
		t.Fatalf("labels 应有 %d 个字段，实际 %v", len(wantLabels), first.Request.Labels)
	}
	for k, v := range wantLabels {
		if first.Request.Labels[k] != v {
			t.Fatalf("labels.%s 应为 %q，实际 %q", k, v, first.Request.Labels[k])
		}
	}
	if first.Request.SessionID == "" || first.Request.SessionID[0] != '-' {
		t.Fatalf("sessionId 应是账号稳定的负数：%q", first.Request.SessionID)
	}

	second := buildEnvelopeFor(t, p, secondTurn)
	m2 := requestIDPattern.FindStringSubmatch(second.RequestID)
	if m2 == nil || m2[1] != cascade || m2[3] != trajectory || m2[4] != "2" {
		t.Fatalf("同一对话第二轮应沿用 ID、步数为 2：%s", second.RequestID)
	}
	if second.Request.Labels["last_step_index"] != "1" || second.Request.Labels["request_id"] != trajectory+"-1" {
		t.Fatalf("第二轮 labels 步进应为 1：%v", second.Request.Labels)
	}
	if second.Request.SessionID != first.Request.SessionID {
		t.Fatal("同一账号的 sessionId 应不变")
	}
}

// 账号、调用方、首条用户消息任一不同都是不同的对话。
func TestConversationIdentity(t *testing.T) {
	base := BuildParams{ProjectID: "p", WireModel: "gemini-2.5-flash", AccountID: 7, Caller: "key:1"}
	cascadeOf := func(p BuildParams, body string) string {
		return requestIDPattern.FindStringSubmatch(buildEnvelopeFor(t, p, body).RequestID)[1]
	}
	ref := cascadeOf(base, firstTurn)
	if cascadeOf(base, firstTurn) != ref {
		t.Fatal("同样的输入应得到同一会话 ID")
	}
	otherAccount, otherCaller := base, base
	otherAccount.AccountID = 8
	otherCaller.Caller = "key:2"
	for name, id := range map[string]string{
		"不同账号":    cascadeOf(otherAccount, firstTurn),
		"不同调用方":   cascadeOf(otherCaller, firstTurn),
		"不同首条消息":  cascadeOf(base, `{"contents":[{"role":"user","parts":[{"text":"bye"}]}]}`),
		"只有图片的消息": cascadeOf(base, `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}]}`),
	} {
		if id == ref {
			t.Fatalf("%s 应是不同的对话", name)
		}
	}
}

func TestLabelsModelFlags(t *testing.T) {
	cases := []struct {
		model                      string
		info                       *ModelInfo
		claude, nonGemini, hasEnum bool
	}{
		{"claude-opus-5-5-high", &ModelInfo{Enum: "MODEL_PLACEHOLDER_M26"}, true, true, true},
		{"gpt-oss-120b-medium", nil, false, true, false},
		{"gemini-2.5-flash", &ModelInfo{}, false, false, false},
	}
	for _, tc := range cases {
		env := buildEnvelopeFor(t, BuildParams{ProjectID: "p", WireModel: tc.model, Model: tc.info}, firstTurn)
		l := env.Request.Labels
		if l["used_claude"] != strconv.FormatBool(tc.claude) || l["used_claude_conservative"] != strconv.FormatBool(tc.claude) ||
			l["used_non_gemini_model"] != strconv.FormatBool(tc.nonGemini) {
			t.Errorf("%s 的模型标记不对：%v", tc.model, l)
		}
		if _, ok := l["model_enum"]; ok != tc.hasEnum {
			t.Errorf("%s：目录有枚举名才填 model_enum，实际 %v", tc.model, l)
		}
	}
}

// countTokens 只要 request 对象，不带会话字段、labels 和 toolConfig。
func TestCountTokensBodyHasNoSessionFields(t *testing.T) {
	var req map[string]any
	if err := json.Unmarshal([]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}}}`), &req); err != nil {
		t.Fatal(err)
	}
	raw, err := BuildCountTokensBody(BuildParams{ProjectID: "p", WireModel: "gemini-2.5-flash", GeminiRequest: req})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["request"] == nil {
		t.Fatalf("应只有 request：%s", raw)
	}
	for _, k := range []string{"sessionId", "labels", "toolConfig"} {
		if _, ok := body["request"][k]; ok {
			t.Fatalf("countTokens 不应带 %s：%s", k, raw)
		}
	}
}

// 非 Gmail 账号用 jetski 标识，Gmail 个人号（含邮箱未知）用 antigravity。
func TestJetskiForNonGmail(t *testing.T) {
	for email, jetski := range map[string]bool{
		"someone@gmail.com":      false,
		"Someone@GoogleMail.com": false,
		"":                       false,
		"dev@company.com":        true,
		"student@school.edu":     true,
	} {
		var req map[string]any
		if err := json.Unmarshal([]byte(firstTurn), &req); err != nil {
			t.Fatal(err)
		}
		raw, err := BuildEnvelope(BuildParams{ProjectID: "p", WireModel: "gemini-2.5-flash", Email: email, GeminiRequest: req})
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			UserAgent string `json:"userAgent"`
			Request   struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"request"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if jetski {
			if env.UserAgent != "jetski" || env.Request.Metadata["ideType"] != "JETSKI" {
				t.Errorf("%s 应用 jetski 标识：%s", email, raw)
			}
		} else if env.UserAgent != "antigravity" || env.Request.Metadata != nil {
			t.Errorf("%q 应用 antigravity 且不带 metadata：%s", email, raw)
		}
	}
}

// maxOutputTokens 缺省时按目录补齐，客户端指定的不覆盖。
func TestMaxOutputTokensDefault(t *testing.T) {
	maxOut := 65536
	info := &ModelInfo{MaxOutputTokens: &maxOut}
	for body, want := range map[string]float64{
		`{"contents":[]}`: 65536,
		`{"contents":[],"generationConfig":{"maxOutputTokens":1024}}`:    1024,
		`{"contents":[],"generation_config":{"max_output_tokens":2048}}`: 2048,
	} {
		req := envelopeRequestWith(t, body, info)
		gen, _ := req["generationConfig"].(map[string]any)
		if gen["maxOutputTokens"] != want {
			t.Errorf("%s：maxOutputTokens 应为 %v，实际 %v", body, want, gen["maxOutputTokens"])
		}
	}
}

func envelopeRequestWith(t *testing.T, body string, info *ModelInfo) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	raw, err := BuildEnvelope(BuildParams{ProjectID: "p", WireModel: "gemini-2.5-flash", GeminiRequest: req, Model: info})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Request map[string]any `json:"request"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Request
}
