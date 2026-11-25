#!/usr/bin/env bash
# 提交前检查：gofmt、go.mod 整洁、go vet、staticcheck、go test（有 C 编译器时带 -race）、前端类型检查，
# 任一步失败即退出。
#
# staticcheck 用 go run 按下面固定的版本拉取运行，不进 go.mod；首次运行需要联网下载。
set -euo pipefail
cd "$(dirname "$0")/.."

STATICCHECK_VERSION=v0.8.2

echo "==> gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "以下文件未格式化，执行 gofmt -w 修正：" >&2
  echo "$unformatted" >&2
  exit 1
fi

echo "==> go mod tidy"
if ! go mod tidy -diff; then
  echo "go.mod/go.sum 不整洁，执行 go mod tidy 修正" >&2
  exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> staticcheck $STATICCHECK_VERSION"
go run "honnef.co/go/tools/cmd/staticcheck@$STATICCHECK_VERSION" ./...

# 有 C 编译器时加 -race 检测数据竞争（-race 需要 cgo，这里显式打开；正式构建仍是 CGO_ENABLED=0，见 build.sh），
# 没有时跑普通测试。Windows 装 MinGW-w64 的 gcc 即可，如 winget install BrechtSanders.WinLibs.POSIX.UCRT。
if command -v "$(go env CC)" >/dev/null 2>&1; then
  echo "==> go test -race"
  CGO_ENABLED=1 go test -race ./...
else
  echo "==> go test（没有 C 编译器，跳过 -race）"
  go test ./...
fi

echo "==> 前端不引用外部资源"
# 管理端要能离线使用：字体、脚本、样式都必须本地打包。shadcn-vue add 会往 style.css 里加 Google Fonts，要删掉。
if grep -rnE "https?://(fonts\.googleapis|fonts\.gstatic|cdn\.|unpkg\.com|cdnjs\.)" web/src web/index.html; then
  echo "前端引用了外部字体或 CDN，改为本地打包" >&2
  exit 1
fi

echo "==> 前端字重只用 400 和 600"
# Windows 的微软雅黑没有 500，font-medium 在中文上退成 400，和同一行英文粗细不一致。shadcn-vue add 生成的组件会带回来。
if grep -rn "font-medium" web/src --include=*.vue --include=*.ts; then
  echo "不用 font-medium：普通文字和控件用 font-normal（默认），标题用 font-semibold" >&2
  exit 1
fi

echo "==> 前端类型检查"
(
  cd web
  [ -d node_modules ] || pnpm install --frozen-lockfile
  pnpm exec vue-tsc --noEmit
)

echo "==> 全部通过"
