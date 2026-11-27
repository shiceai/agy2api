package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/shiceai/agy2api/internal/relay"
	"github.com/shiceai/agy2api/internal/store"
)

// 失败请求的正文："设置 → 数据"里开关（默认关）。开着时上游返回了错误的请求把发出的请求体存下（见 relay/capture.go），
// 用量详情里下载。含用户的对话内容，所以默认关，可以随时清空。

// requestCaptureSettings 是保存失败请求正文的开关。
type requestCaptureSettings struct {
	Enabled bool `json:"enabled"`
}

// requestCaptureSetting 存在 settings 的 request_capture 键，默认关。
var requestCaptureSetting = jsonSetting[requestCaptureSettings]{
	key:      "request_capture",
	name:     "保存失败请求正文的设置",
	limit:    1 << 10,
	defaults: func() requestCaptureSettings { return requestCaptureSettings{} },
}

// requestCaptureView 是设置页"数据"的失败请求正文卡片：开关、已存的条数和占用、上限。
type requestCaptureView struct {
	Settings  requestCaptureSettings `json:"settings"`
	Count     int64                  `json:"count"`
	Bytes     int64                  `json:"bytes"`
	MaxCount  int                    `json:"max_count"`
	MaxBytes  int64                  `json:"max_bytes"`
	PerMinute int                    `json:"per_minute"`
}

// loadRequestCapture 启动时从库加载开关并生效。
func (s *Service) loadRequestCapture(ctx context.Context) error {
	v, err := requestCaptureSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	s.relay.SetRequestCapture(v.Enabled)
	return nil
}

func (s *Service) requestCaptureView(ctx context.Context) (*requestCaptureView, error) {
	v, err := requestCaptureSetting.load(ctx, s.store)
	if err != nil {
		return nil, err
	}
	st, err := s.store.RequestBodyStats(ctx)
	if err != nil {
		return nil, err
	}
	return &requestCaptureView{Settings: v, Count: st.Count, Bytes: st.Bytes,
		MaxCount: relay.CaptureMaxCount, MaxBytes: relay.CaptureMaxBytes, PerMinute: relay.CapturePerMinute}, nil
}

func (s *Service) writeRequestCapture(w http.ResponseWriter, r *http.Request) {
	v, err := s.requestCaptureView(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Service) adminGetRequestCapture(w http.ResponseWriter, r *http.Request) {
	s.writeRequestCapture(w, r)
}

func (s *Service) adminUpdateRequestCapture(w http.ResponseWriter, r *http.Request) {
	v, ok := requestCaptureSetting.update(w, r, s.store, requestCaptureSetting.defaults())
	if !ok {
		return
	}
	s.relay.SetRequestCapture(v.Enabled)
	slog.InfoContext(r.Context(), "已修改保存失败请求正文的设置", "enabled", v.Enabled)
	s.writeRequestCapture(w, r)
}

// adminClearRequestBodies 删除全部已保存的失败请求正文（开关不变）。
func (s *Service) adminClearRequestBodies(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.ClearRequestBodies(r.Context())
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.InfoContext(r.Context(), "已清空失败请求的正文", "deleted", n)
	s.writeRequestCapture(w, r)
}

// adminUsageRequestBody 下载一条用量记录对应的失败请求正文（解压后的 JSON）。没存、已被清理时 404。
func (s *Service) adminUsageRequestBody(w http.ResponseWriter, r *http.Request) {
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
	gz, _, err := s.store.RequestBody(r.Context(), l.RequestID)
	if errors.Is(err, store.ErrNotFound) || l.RequestID == "" {
		adminError(w, http.StatusNotFound, "request body not saved or already removed")
		return
	}
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer zr.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="request-%d.json"`, id))
	if _, err := io.Copy(w, zr); err != nil {
		slog.WarnContext(r.Context(), "下载失败请求的正文中断", "usage_id", id, "err", err)
	}
}
