#!/usr/bin/env bash
# 构建前端和单二进制（无 CGO）。
#
# 用法：
#   scripts/build.sh                            本机平台，输出 bin/agy2api[.exe]
#   scripts/build.sh linux/amd64 darwin/arm64   交叉编译，输出 bin/agy2api-<os>-<arch>[.exe]
#
# 版本号默认取 git describe（没有标签时是提交号，有未提交改动时带 -dirty），可用 VERSION 环境变量覆盖。
# 版本号通过 -ldflags 注入 main.version，运行 agy2api -version 查看。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

# 先校验目标格式，避免构建完前端才发现参数写错。
for target in "$@"; do
  if [[ ! "$target" =~ ^[a-z0-9]+/[a-z0-9]+$ ]]; then
    echo "目标格式应为 os/arch，例如 linux/amd64：$target" >&2
    exit 1
  fi
done

echo "==> 构建前端"
( cd web && pnpm install --frozen-lockfile && pnpm build )

build() { # build <goos> <goarch> <输出路径>
  echo "==> 构建 $1/$2（版本 $VERSION）→ $3"
  CGO_ENABLED=0 GOOS="$1" GOARCH="$2" \
    go build -trimpath -ldflags "-X main.version=$VERSION" -o "$3" .
}

if [ $# -eq 0 ]; then
  build "$(go env GOOS)" "$(go env GOARCH)" "bin/agy2api$(go env GOEXE)"
else
  for target in "$@"; do
    os="${target%/*}"
    arch="${target#*/}"
    ext=""
    if [ "$os" = windows ]; then
      ext=".exe"
    fi
    build "$os" "$arch" "bin/agy2api-$os-$arch$ext"
  done
fi

echo "==> 完成"
