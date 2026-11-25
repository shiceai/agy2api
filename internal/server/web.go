package server

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// webdist 由前端构建写入；仓库里只有 .gitkeep，保证未构建前端时也能编译。
//
//go:embed all:webdist
var webDist embed.FS

const frontendMissingHint = "管理端前端未构建：在 web 目录执行 pnpm install && pnpm build 后重新编译二进制。\n"

// apiNotFound 处理没有匹配到路由的接口路径：返回 JSON 404，不回退到前端页面，免得写错路径的客户端拿到一页 HTML。
// 管理接口用管理端的错误格式，其余（v1beta、v1）用 Gemini 的错误格式。没有匹配的前缀时返回 false。
func apiNotFound(w http.ResponseWriter, r *http.Request) bool {
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/api/"):
		adminError(w, http.StatusNotFound, "not found")
	case strings.HasPrefix(p, "/v1beta/"), strings.HasPrefix(p, "/v1/"):
		geminiError(w, http.StatusNotFound, "NOT_FOUND", "unknown path: "+p)
	default:
		return false
	}
	return true
}

// spaHandler 提供嵌入的前端静态文件，未命中的路径回退到 index.html 交给前端路由（History 模式）。
// 接口路径不回退（见 apiNotFound）。未构建前端时返回提示，后端接口不受影响。
func spaHandler() http.Handler {
	dist, err := fs.Sub(webDist, "webdist")
	if err == nil {
		_, err = fs.Stat(dist, "index.html")
	}
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiNotFound(w, r) {
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, frontendMissingHint)
		})
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiNotFound(w, r) {
			return
		}
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(dist, clean); err != nil {
			// 未命中静态文件：交给前端路由（返回 index.html）。
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
