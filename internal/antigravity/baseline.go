package antigravity

import (
	_ "embed"
	"strings"
)

// 内置官方基础 system 提示词：启用系统提示词注入而没有填自定义模板时用它，让请求的 system 看起来像官方
// Antigravity 客户端发的。内容由官方客户端的 system 模板（identity、communication_style、guidelines）重构而来，
// 相邻版本间基本稳定，对所有账号统一使用，与账号 UA 版本无关。

//go:embed baseline.txt
var builtinSystemPromptRaw string

// BuiltinSystemPrompt 是内置基线提示词（去掉首尾空白）。
var BuiltinSystemPrompt = strings.TrimSpace(builtinSystemPromptRaw)
