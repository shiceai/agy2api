#!/usr/bin/env bash
# 刷新 internal/antigravity/ua-builds.json：查官方 Antigravity Hub 最新版本，没收录过就下载 language_server，
# 静态提取构建戳里的 CL（grep，不执行二进制），把 (版本, CL) 加到表顶，只留最新 KEEP 条。
# 供 .github/workflows/ua-builds.yml 定时运行；本地也可直接跑（需要 curl、jq、grep）。从仓库根目录运行。
#
# CL 的来源：官方 language_server 二进制里嵌有明文构建戳「changelist <CL> with baseline <baseline>」，
# 这里只读这段静态字符串取 CL，不运行二进制、不保存、不转发，只把一个 CL 数字写进 JSON。
set -euo pipefail

MANIFEST="https://antigravity-hub-auto-updater-974169037036.us-central1.run.app/manifest/latest-arm64-mac.yml"
FILE="${UA_BUILDS_FILE:-internal/antigravity/ua-builds.json}" # 测试可用 UA_BUILDS_FILE 指向临时表
KEEP=12

# 1. 从清单取最新版本和下载前缀（形如 .../antigravity-hub/2.35.2-4585983447007232）。
prefix=$(curl -fsSL "$MANIFEST" |
	grep -oE 'https://storage\.googleapis\.com/antigravity-public/antigravity-hub/[0-9]+\.[0-9]+\.[0-9]+-[0-9]+' |
	head -1)
if [ -z "$prefix" ]; then
	echo "无法从清单取得下载前缀" >&2
	exit 1
fi
base=${prefix##*/} # 2.35.2-4585983447007232
version=${base%-*} # 2.35.2
if ! printf '%s' "$version" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "清单版本号异常: $version" >&2
	exit 1
fi

# 2. 已收录就无需更新。
if jq -e --arg v "$version" '.builds[] | select(.version == $v)' "$FILE" >/dev/null; then
	echo "版本 $version 已收录，无需更新"
	exit 0
fi

# 3. 不比表顶新就跳过（清单异常时的保险）。
top=$(jq -r '.builds[0].version // ""' "$FILE")
if [ -n "$top" ]; then
	newest=$(printf '%s\n%s\n' "$version" "$top" | sort -V | tail -1)
	if [ "$newest" != "$version" ]; then
		echo "清单版本 $version 不新于表顶 $top，跳过"
		exit 0
	fi
fi

# 4. 下载 language_server（CL 与平台无关，用 linux-x64），静态提取构建戳里的 CL（不执行二进制）。
url="$prefix/unsigned/language_server-linux-x64"
tmp=$(mktemp)
trap 'rm -f "$tmp" "$tmp.json"' EXIT
echo "下载 $url"
curl -fL --retry 3 --max-time 600 -o "$tmp" "$url"
cl=$(grep -aoE 'changelist [0-9]+ with baseline [0-9]+' "$tmp" | head -1 | grep -oE '[0-9]+' | head -1 || true)
if ! printf '%s' "$cl" | grep -qE '^[0-9]{6,12}$'; then
	echo "未能从二进制提取有效 CL（得到 '$cl'）" >&2
	exit 1
fi
echo "提取到 $version / $cl"

# 5. 加到表顶，只留最新 KEEP 条，写回。
jq --arg v "$version" --arg cl "$cl" --argjson keep "$KEEP" \
	'{builds: ([{version: $v, cl: $cl}] + .builds) | .[0:$keep]}' "$FILE" >"$tmp.json"
mv "$tmp.json" "$FILE"
echo "已更新 $FILE"
