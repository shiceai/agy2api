package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shiceai/agy2api/internal/store"
)

// usageRange 解析 since、until（Unix 毫秒）；since 缺省为 24 小时前，until 缺省不设上限。
func usageRange(q url.Values) (since, until int64, err error) {
	since = time.Now().Add(-24 * time.Hour).UnixMilli()
	if raw := q.Get("since"); raw != "" {
		if since, err = strconv.ParseInt(raw, 10, 64); err != nil {
			return 0, 0, fmt.Errorf("invalid since")
		}
	}
	if raw := q.Get("until"); raw != "" {
		if until, err = strconv.ParseInt(raw, 10, 64); err != nil {
			return 0, 0, fmt.Errorf("invalid until")
		}
	}
	return since, until, nil
}

// adminUsageLogs 按条件分页返回用量记录：status=success|error，stream=stream|sync，
// model、account_id、api_key_id、request_id 精确匹配，limit 最大 200。
func (s *Service) adminUsageLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, until, err := usageRange(q)
	if err != nil {
		adminError(w, http.StatusBadRequest, err.Error())
		return
	}
	f := store.UsageFilter{
		Since:     since,
		Until:     until,
		Status:    q.Get("status"),
		Model:     strings.TrimSpace(q.Get("model")),
		Stream:    q.Get("stream"),
		RequestID: strings.TrimSpace(q.Get("request_id")),
		Protocol:  q.Get("protocol"),
	}
	if f.Status != "" && f.Status != "success" && f.Status != "error" {
		adminError(w, http.StatusBadRequest, "status must be success or error")
		return
	}
	if f.Protocol != "" && f.Protocol != store.ProtocolGemini && f.Protocol != store.ProtocolAnthropic && f.Protocol != store.ProtocolOpenAI {
		adminError(w, http.StatusBadRequest, "protocol must be gemini, anthropic or openai")
		return
	}
	if f.Stream != "" && f.Stream != "stream" && f.Stream != "sync" {
		adminError(w, http.StatusBadRequest, "stream must be stream or sync")
		return
	}
	if raw := q.Get("account_id"); raw != "" {
		if f.AccountID, err = strconv.ParseInt(raw, 10, 64); err != nil {
			adminError(w, http.StatusBadRequest, "invalid account_id")
			return
		}
	}
	// api_key_id=0 筛选管理端的账号检测，所以不能用 0 表示不筛选。
	if raw := q.Get("api_key_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			adminError(w, http.StatusBadRequest, "invalid api_key_id")
			return
		}
		f.APIKeyID = &id
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	logs, total, err := s.store.UsageLogs(r.Context(), f, limit, offset)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": logs, "total": total})
}

// adminUsageLog 返回一条用量记录的完整信息（详情弹窗用）。
func (s *Service) adminUsageLog(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	l, err := s.store.UsageLogByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		adminError(w, http.StatusNotFound, "usage record not found")
		return
	}
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": l})
}
