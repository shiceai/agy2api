package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/shiceai/agy2api/internal/store"
)

// Router 构造完整的 HTTP 路由。
func (s *Service) Router() http.Handler {
	r := chi.NewRouter()
	// 客户端 IP 只认可信反代的转发头，见 clientip.go。
	r.Use(realIP(s.trusted))
	r.Use(requestLog)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	// 入站 Gemini：API Key 鉴权。/v1 是 /v1beta 的别名。模型名里可以带 /（如 假流式/gemini-2.5-flash），
	// 所以模型部分用通配，见 modelPath。
	r.Group(func(r chi.Router) {
		r.Use(s.apiKeyAuth)
		for _, prefix := range []string{"/v1beta", "/v1"} {
			r.Get(prefix+"/models", s.listModels)
			r.Get(prefix+"/models/*", s.getModel)
			r.Post(prefix+"/models/*", s.handleModelAction)
		}
	})

	// 管理端：先按 IP 白名单（AGY2API_ADMIN_ALLOW），再校验静态口令。
	r.Route("/api/admin", func(r chi.Router) {
		r.Use(s.adminAllowlist, s.adminAuth)

		r.Get("/session", s.adminSession)
		r.Get("/system", s.adminSystem)
		r.Post("/system/check", s.adminCheckUpdate)
		r.Post("/system/upgrade", s.adminUpgrade)
		r.Post("/system/revert", s.adminRevertUpgrade)
		r.Get("/overview", s.adminOverview)
		r.Get("/live", s.adminLive)

		r.Get("/accounts", s.adminListAccounts)
		r.Post("/accounts/import", s.adminImportAccount)
		r.Post("/accounts/oauth/start", s.adminOAuthStart)
		r.Post("/accounts/oauth/complete", s.adminOAuthComplete)
		r.Post("/accounts/batch", s.adminBatchAccounts)
		r.Post("/accounts/{id}/sync", s.adminSyncAccount)
		r.Get("/accounts/{id}/stats", s.adminAccountStats)
		r.Post("/accounts/{id}/test", s.adminTestAccount)
		r.Post("/accounts/{id}/enable", s.adminSetAccountStatus(store.StatusEnabled))
		r.Post("/accounts/{id}/disable", s.adminSetAccountStatus(store.StatusDisabled))
		r.Put("/accounts/{id}/settings", s.adminUpdateAccountSettings)
		r.Delete("/accounts/{id}", s.adminDeleteAccount)
		r.Put("/accounts/{id}/proxy", s.adminSetAccountProxy)
		r.Post("/accounts/assign-proxy", s.adminAssignProxies)

		r.Get("/proxies", s.adminListProxies)
		r.Post("/proxies", s.adminAddProxies)
		r.Post("/proxies/check", s.adminCheckProxies)
		r.Post("/proxies/enable", s.adminSetProxiesStatus(store.StatusEnabled))
		r.Post("/proxies/disable", s.adminSetProxiesStatus(store.StatusDisabled))
		r.Put("/proxies/{id}", s.adminUpdateProxy)
		r.Delete("/proxies/{id}", s.adminDeleteProxy)

		r.Get("/keys", s.adminListKeys)
		r.Post("/keys", s.adminCreateKey)
		r.Put("/keys/{id}", s.adminUpdateKey)
		r.Get("/keys/{id}/stats", s.adminKeyStats)
		r.Get("/models", s.adminModelNames)
		r.Post("/keys/{id}/enable", s.adminSetKeyStatus(store.StatusEnabled))
		r.Post("/keys/{id}/disable", s.adminSetKeyStatus(store.StatusDisabled))
		r.Delete("/keys/{id}", s.adminDeleteKey)

		r.Get("/settings", s.adminGetSettings)
		r.Put("/settings", s.adminUpdateSettings)
		r.Get("/settings/scheduling", s.adminGetScheduling)
		r.Put("/settings/scheduling", s.adminUpdateScheduling)
		r.Get("/settings/client-filter", s.adminGetClientFilter)
		r.Put("/settings/client-filter", s.adminUpdateClientFilter)
		r.Get("/settings/system-prompt", s.adminGetSystemPrompt)
		r.Put("/settings/system-prompt", s.adminUpdateSystemPrompt)
		r.Get("/settings/request-handling", s.adminGetRequestHandling)
		r.Put("/settings/request-handling", s.adminUpdateRequestHandling)
		r.Get("/prices", s.adminGetPrices)
		r.Post("/prices/sync", s.adminSyncPrices)
		r.Put("/prices/{model}", s.adminSetPrice)
		r.Delete("/prices/{model}", s.adminDeletePrice)

		r.Get("/usage/logs", s.adminUsageLogs)
		r.Get("/usage/logs/{id}", s.adminUsageLog)
		r.Get("/usage/retention", s.adminGetRetention)
		r.Put("/usage/retention", s.adminUpdateRetention)
		r.Post("/usage/retention/run", s.adminRunPurge)
	})

	// 前端静态资源（SPA 回退），和管理接口一样受 IP 白名单限制。
	r.With(s.adminAllowlist).Handle("/*", spaHandler())

	return r
}
