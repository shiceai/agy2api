// Package ddl 存放 SQLite 迁移脚本。脚本编译时嵌入二进制，
// 由 internal/store 在启动时按文件名顺序执行尚未执行过的文件。规则见 README.md。
package ddl

import "embed"

// FS 是全部迁移脚本（*.sql）。
//
//go:embed *.sql
var FS embed.FS
