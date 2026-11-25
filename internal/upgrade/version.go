package upgrade

import (
	"regexp"
	"strconv"
)

// releasePattern 是发布流程打的标签：v主.次.修订，预发布带 -alpha.N、-beta.N、-rc.N。
// git describe 出来的开发构建（v0.1.0-3-gabc1234、带 -dirty 的）和 dev 都不算。
var releasePattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-(alpha|beta|rc)\.(\d+))?$`)

// release 是解析后的正式版本号。
type release struct {
	nums [3]int
	pre  string // 空表示正式版；alpha、beta、rc 按字母顺序正好是先后顺序
	preN int
}

// parseRelease 解析正式发布的版本号，不是时 ok 为假。
func parseRelease(v string) (r release, ok bool) {
	m := releasePattern.FindStringSubmatch(v)
	if m == nil {
		return release{}, false
	}
	for i := range r.nums {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return release{}, false
		}
		r.nums[i] = n
	}
	if m[4] != "" {
		n, err := strconv.Atoi(m[5])
		if err != nil {
			return release{}, false
		}
		r.pre, r.preN = m[4], n
	}
	return r, true
}

// IsRelease 报告版本号是不是正式发布的标签（在线升级只对这样的构建开放）。
func IsRelease(v string) bool {
	_, ok := parseRelease(v)
	return ok
}

// compare 按语义化版本比较先后：a 在前返回负数，相同返回 0，a 在后返回正数。预发布排在同号正式版之前。
func compare(a, b release) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			return a.nums[i] - b.nums[i]
		}
	}
	switch {
	case a.pre == b.pre:
		return a.preN - b.preN
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	case a.pre < b.pre:
		return -1
	default:
		return 1
	}
}

// newer 报告 a 是否比 b 新，两者都得是正式发布的版本号。
func newer(a, b string) bool {
	ra, ok1 := parseRelease(a)
	rb, ok2 := parseRelease(b)
	return ok1 && ok2 && compare(ra, rb) > 0
}
