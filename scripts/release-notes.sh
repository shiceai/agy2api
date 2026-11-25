#!/usr/bin/env bash
# 输出某个版本的发布说明：CHANGELOG.md 里 "## <版本> - <日期>" 那一节的正文，末尾加上和上一个版本的比较链接。
# 预发布标签（如 v0.2.0-rc.1）没有自己的一节时用正式版本（v0.2.0）那一节。找不到时报错，发布中止。
#
# 用法：scripts/release-notes.sh v0.2.0
set -euo pipefail
cd "$(dirname "$0")/.."

tag="${1:?用法：scripts/release-notes.sh <标签>}"
repo="${GITHUB_REPOSITORY:-shiceai/agy2api}"

# section 输出标题第二个字段等于 $1 的那一节（不含标题），到下一个 "## " 为止。
section() {
  awk -v v="$1" '
    /^## / { if (found) exit; split($0, a, " "); if (a[2] == v) { found = 1; next } }
    found { print }
  ' CHANGELOG.md
}

notes="$(section "$tag")"
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ] && [[ "$tag" == *-* ]]; then
  notes="$(section "${tag%%-*}")"
fi
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG.md 里没有 $tag 这一节：先写好发布说明再打标签" >&2
  exit 1
fi

# 上一个版本：正式版只和上一个正式版比（跳过预发布），预发布版和上一个任意版本比。
prev="$(git tag --merged "$tag^" --list 'v*' --sort=-v:refname 2>/dev/null |
  { if [[ "$tag" == *-* ]]; then cat; else grep -v -- '-' || true; fi; } | head -n 1 || true)"

printf '%s\n\n' "$(printf '%s\n' "$notes" | sed -e '/./,$!d')" # 去掉开头的空行（结尾的由 $(...) 去掉）
if [ -n "$prev" ]; then
  printf '**完整变更**：https://github.com/%s/compare/%s...%s\n' "$repo" "$prev" "$tag"
else
  printf '**完整变更**：https://github.com/%s/commits/%s\n' "$repo" "$tag"
fi
