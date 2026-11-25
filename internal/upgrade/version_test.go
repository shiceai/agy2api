package upgrade

import "testing"

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.1.0":                    true,
		"v1.12.3":                   true,
		"v0.2.0-rc.1":               true,
		"v0.2.0-beta.12":            true,
		"0.1.0":                     false, // 标签带 v
		"dev":                       false,
		"v0.1.0-3-gabc1234":         false, // git describe 的开发构建
		"v0.1.0-dirty":              false,
		"v0.1.0-3-gabc1234-dirty":   false,
		"v0.2.0-rc":                 false,
		"v0.2.0-preview.1":          false,
		"v0.1.0+build.1":            false,
		"v0.1":                      false,
		" v0.1.0":                   false,
		"v0.1.0\n":                  false,
		"v99999999999999999999.0.0": false,
	} {
		if got := IsRelease(v); got != want {
			t.Errorf("IsRelease(%q) = %v，应为 %v", v, got, want)
		}
	}
}

// 版本先后：预发布排在同号正式版之前，alpha < beta < rc，编号按数字比。
func TestNewer(t *testing.T) {
	order := []string{"v0.1.0-alpha.1", "v0.1.0-alpha.2", "v0.1.0-beta.1", "v0.1.0-rc.1", "v0.1.0-rc.2", "v0.1.0-rc.10", "v0.1.0", "v0.1.1", "v0.2.0", "v0.10.0", "v1.0.0"}
	for i := range order {
		for j := range order {
			if got, want := newer(order[i], order[j]), i > j; got != want {
				t.Errorf("newer(%s, %s) = %v，应为 %v", order[i], order[j], got, want)
			}
		}
	}
	if newer("v0.2.0", "dev") || newer("dev", "v0.1.0") {
		t.Error("开发构建的版本号不能参与比较")
	}
}
