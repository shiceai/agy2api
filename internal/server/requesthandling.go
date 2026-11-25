package server

import (
	"context"
	"log/slog"
	"net/http"
)

// requestHandlingView 是请求处理设置的读写形态。
type requestHandlingView struct {
	// RemoteImagesEnabled 为真时把客户端请求里的 http(s) 图片 URL 下载后内联成 inlineData。默认关闭。
	RemoteImagesEnabled bool `json:"remote_images_enabled"`
	// ReturnThoughts 为真（默认）时把模型的思考内容返回给客户端；关闭后请求侧设 includeThoughts=false。
	ReturnThoughts bool `json:"return_thoughts"`
}

// requestHandlingSetting 存在 settings 的 request_handling 键。默认远程图片内联关、返回思考内容开（保持原有行为）。
var requestHandlingSetting = jsonSetting[requestHandlingView]{
	key:   "request_handling",
	name:  "请求处理设置",
	limit: 1 << 12,
	defaults: func() requestHandlingView {
		return requestHandlingView{RemoteImagesEnabled: false, ReturnThoughts: true}
	},
}

func (v requestHandlingView) apply(s *Service) {
	s.relay.SetRequestHandling(v.RemoteImagesEnabled, v.ReturnThoughts)
}

// loadRequestHandling 启动时从库加载请求处理设置并生效。
func (s *Service) loadRequestHandling(ctx context.Context) error {
	v, err := requestHandlingSetting.load(ctx, s.store)
	if err != nil {
		return err
	}
	v.apply(s)
	return nil
}

func (s *Service) adminGetRequestHandling(w http.ResponseWriter, r *http.Request) {
	v, err := requestHandlingSetting.load(r.Context(), s.store)
	if err != nil {
		adminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": v})
}

func (s *Service) adminUpdateRequestHandling(w http.ResponseWriter, r *http.Request) {
	v, ok := requestHandlingSetting.update(w, r, s.store, requestHandlingSetting.defaults())
	if !ok {
		return
	}
	v.apply(s)
	slog.InfoContext(r.Context(), "已修改请求处理设置", "remote_images", v.RemoteImagesEnabled, "return_thoughts", v.ReturnThoughts)
	s.adminGetRequestHandling(w, r)
}
