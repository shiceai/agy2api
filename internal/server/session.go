package server

import "net/http"

// buildInfo 是管理端显示的版本信息。版本号由构建注入（git describe：正式版是 v1.3.0 这样的标签，之后的开发构建是
// v1.3.0-3-gabc1234，有未提交的改动时带 -dirty）。
type buildInfo struct {
	Version string `json:"version"`
}

// readBuildInfo 返回构建注入的版本号，为空（go run、go test）时是 dev。
func readBuildInfo(version string) buildInfo {
	if version == "" {
		version = "dev"
	}
	return buildInfo{Version: version}
}

// sessionView 是版本号加上查到的新版本（侧边栏提示用，没有时为空）。
type sessionView struct {
	buildInfo
	Update string `json:"update,omitempty"`
}

// adminSession 返回版本信息。走管理端鉴权，口令不对时已经返回 401：登录页用它当场校验口令，管理端用它显示版本。
func (s *Service) adminSession(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, sessionView{buildInfo: s.build, Update: s.upgrade.UpdateVersion()})
}
