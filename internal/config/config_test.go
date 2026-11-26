package config

import (
	"strings"
	"testing"
)

func TestAdminPasswordWeakness(t *testing.T) {
	cases := []struct {
		pw   string
		weak bool
	}{
		{"agy-dev", true},                     // 太短
		{"Ab1!Ab1!Ab1", true},                 // 11 个字符
		{"123456789012", true},                // 只有数字
		{"abcdefghijklmnop", true},            // 只有小写字母
		{"口令口令口令口令口令口令", true},                // 只有一类（非字母数字）
		{"abcdefgh1234", false},               // 12 个字符、两类
		{"Zx8kP2vQ9mR4tW7y", false},           // 随机串
		{"Kq3n+V9xQwE8rT2yUi7oP0aS==", false}, // openssl rand -base64 的样子
		{"ABCDEFGHIJKLmnopqrstuvwxyz", false}, // 大小写两类
		{"短短短短短短短短短短短1", false},               // 按字符数算，不按字节
	}
	for _, c := range cases {
		if got := AdminPasswordWeakness(c.pw) != ""; got != c.weak {
			t.Errorf("AdminPasswordWeakness(%q) 弱=%v，应为 %v（%q）", c.pw, got, c.weak, AdminPasswordWeakness(c.pw))
		}
	}
}

// AGY2API_ADDR 可写多个地址：逗号分隔，去掉空格、空项和重复项；没有地址时用默认值。
func TestListenAddrs(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", ":8249"},
		{" , ,", ":8249"},
		{"127.0.0.1:8249", "127.0.0.1:8249"},
		{" 127.0.0.1:8249 , 10.8.0.1:8249,,127.0.0.1:8249 ", "127.0.0.1:8249|10.8.0.1:8249"},
		{"[::1]:8249,:9000", "[::1]:8249|:9000"},
	} {
		if got := strings.Join(listenAddrs(tc.raw), "|"); got != tc.want {
			t.Errorf("listenAddrs(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
