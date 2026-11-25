package upgrade

import "testing"

// 启动时选哪个程序：在线升级的程序比自己新、平台相同、没停用时换成它；自己不比它旧时清理掉它。
func TestDecide(t *testing.T) {
	st := func(f func(*State)) *State {
		s := &State{Version: "v0.2.0", OS: "linux", Arch: "amd64"}
		if f != nil {
			f(s)
		}
		return s
	}
	cases := []struct {
		name string
		st   *State
		own  string
		want bootAction
	}{
		{"没有在线升级的程序", nil, "v0.1.0", bootSelf},
		{"新程序更新", st(nil), "v0.1.0", bootOverlay},
		{"预发布升到正式版", st(nil), "v0.2.0-rc.2", bootOverlay},
		{"已确认能启动，不再计次", st(func(s *State) { s.Confirmed, s.Attempts = true, 9 }), "v0.1.0", bootOverlay},
		{"还有启动机会", st(func(s *State) { s.Attempts = maxAttempts - 1 }), "v0.1.0", bootOverlay},
		{"连续起不来", st(func(s *State) { s.Attempts = maxAttempts }), "v0.1.0", bootDisable},
		{"已停用", st(func(s *State) { s.Failed = true }), "v0.1.0", bootSelf},
		{"镜像换成同版本", st(nil), "v0.2.0", bootCleanup},
		{"镜像换成更新的", st(nil), "v0.3.0", bootCleanup},
		{"平台不同（数据目录搬过来的）", st(func(s *State) { s.Arch = "arm64" }), "v0.1.0", bootCleanup},
		{"记录的版本号不对", st(func(s *State) { s.Version = "latest" }), "v0.1.0", bootCleanup},
		{"开发构建不碰它", st(nil), "v0.1.0-3-gabc1234-dirty", bootSelf},
		{"dev 不碰它", st(nil), "dev", bootSelf},
	}
	for _, c := range cases {
		if got := decide(c.st, c.own, "linux", "amd64"); got != c.want {
			t.Errorf("%s：decide = %d，应为 %d", c.name, got, c.want)
		}
	}
}

func TestWithoutUpgradeEnv(t *testing.T) {
	got := withoutUpgradeEnv([]string{"PATH=/bin", envBase + "=v0.1.0", envBaseExe + "=/usr/local/bin/agy2api", "AGY2API_UPGRADE_BASE_EXTRA=x"})
	if len(got) != 2 || got[0] != "PATH=/bin" || got[1] != "AGY2API_UPGRADE_BASE_EXTRA=x" {
		t.Fatalf("withoutUpgradeEnv = %v", got)
	}
}
