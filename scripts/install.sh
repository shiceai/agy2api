#!/usr/bin/env bash
# agy2api 一键安装（Linux、macOS）：从 GitHub Releases 下载对应平台的二进制包，校验 SHA256 后安装。
# Linux 有 systemd 时装成系统服务（DynamicUser 加固），首次安装自动生成管理口令；再运行一次就是升级，
# 配置和数据保留，旧版本备份成 agy2api.old。不改系统内核参数。
#
#   curl -fsSL https://github.com/shiceai/agy2api/releases/latest/download/install.sh | sudo bash
#   ... | sudo bash -s -- --version v0.2.0        安装指定版本
#   ... | sudo bash -s -- --listen 0.0.0.0:8249   不经反向代理直接对外（只在首次安装、生成配置时生效）
#   ... | bash -s -- --docker                     在当前目录用 Docker Compose 部署，不装二进制
#   ... | sudo bash -s -- --uninstall             卸载，保留配置和数据
#
# 测试用的环境变量：AGY2API_DOWNLOAD_BASE 换下载地址，AGY2API_LATEST_URL 换查最新版本的接口，
# AGY2API_IMAGE 换 Docker 模式用的镜像。
set -euo pipefail

REPO=shiceai/agy2api
DOWNLOAD_BASE="${AGY2API_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download}"
LATEST_URL="${AGY2API_LATEST_URL:-https://api.github.com/repos/$REPO/releases/latest}"
BIN=/usr/local/bin/agy2api
SERVICE=/etc/systemd/system/agy2api.service
ENV_DIR=/etc/agy2api
ENV_FILE=$ENV_DIR/env
DATA_DIR=/var/lib/agy2api
DEFAULT_LISTEN=127.0.0.1:8249

version=""
listen=""
mode=install
os=""
arch=""
pkg=""
password=""

if [ -t 1 ]; then
  G=$'\033[32m' Y=$'\033[33m' R=$'\033[31m' N=$'\033[0m'
else
  G='' Y='' R='' N=''
fi
say() { printf '%s==>%s %s\n' "$G" "$N" "$*"; }
warn() { printf '%s注意：%s%s\n' "$Y" "$*" "$N" >&2; }
die() {
  printf '%s错误：%s%s\n' "$R" "$*" "$N" >&2
  exit 1
}

usage() {
  cat <<'EOF'
agy2api 一键安装

  curl -fsSL https://github.com/shiceai/agy2api/releases/latest/download/install.sh | sudo bash

选项（放在 bash -s -- 后面）：
  --version vX.Y.Z   安装指定版本，默认最新正式版
  --listen 地址       监听地址，默认 127.0.0.1:8249（只在本机，由反向代理对外提供 HTTPS）
  --docker           在当前目录用 Docker Compose 部署
  --uninstall        卸载，保留配置和数据
  -h, --help         显示这段说明
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version)
      [ $# -ge 2 ] || die "--version 后面要写版本，如 v0.2.0"
      version="$2"
      shift 2
      ;;
    --listen)
      [ $# -ge 2 ] || die "--listen 后面要写地址，如 0.0.0.0:8249"
      listen="$2"
      shift 2
      ;;
    --docker)
      mode=docker
      shift
      ;;
    --uninstall)
      mode=uninstall
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *) die "不认识的参数：$1（--help 查看用法）" ;;
  esac
done

if [ -n "$version" ] && [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
  die "版本号格式不对：$version（应为 v0.2.0 这样）"
fi
if [ -n "$listen" ] && [[ ! "$listen" =~ ^(\[[0-9A-Fa-f:]+\]|[0-9A-Za-z.-]*):[0-9]{1,5}$ ]]; then
  die "监听地址格式不对：$listen（应为 0.0.0.0:8249、127.0.0.1:8249 或 :8249）"
fi

have() { command -v "$1" >/dev/null 2>&1; }

# fetch 下载 $1 到文件 $2，失败时重试几次。
fetch() {
  if have curl; then
    curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1" || die "下载失败：$1"
  elif have wget; then
    wget -q -O "$2" "$1" || die "下载失败：$1"
  else
    die "需要 curl 或 wget"
  fi
}

sha256() {
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "需要 sha256sum 或 shasum 来校验下载的文件"
  fi
}

# gen_password 生成 24 位随机口令（字母、数字、- 和 _）。
gen_password() {
  head -c 18 /dev/urandom | base64 | tr '+/' '-_'
}

# http_ok 报告 $1 是否返回 2xx。
http_ok() {
  if have curl; then
    curl -fsS --max-time 3 -o /dev/null "$1" 2>/dev/null
  else
    wget -q -T 3 -O /dev/null "$1" 2>/dev/null
  fi
}

# wait_healthy 等服务的 /healthz 通过，最多 30 秒。$1 是监听地址，0.0.0.0 和空主机按本机访问。
wait_healthy() {
  local host="${1%:*}" port="${1##*:}"
  case "$host" in "" | 0.0.0.0 | "[::]") host=127.0.0.1 ;; esac
  local url="http://$host:$port/healthz"
  for _ in $(seq 1 30); do
    if http_ok "$url"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

resolve_version() {
  [ -n "$version" ] && return
  say "查询最新版本"
  fetch "$LATEST_URL" "$work/latest.json"
  version=$(grep -m1 '"tag_name"' "$work/latest.json" | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
  [ -n "$version" ] || die "查不到最新版本，可以用 --version 指定"
}

detect_platform() {
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "不支持的系统：$(uname -s)。Windows 请从 Releases 下载 zip 包，或者加 --docker 用 Docker 部署" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "不支持的架构：$(uname -m)，目前只有 amd64 和 arm64 的包" ;;
  esac
}

# download_package 下载这个版本对应平台的压缩包，按 SHA256SUMS.txt 校验后解压，包目录记在 pkg。
download_package() {
  local name="agy2api_${version#v}_${os}_${arch}"
  say "下载 agy2api $version（$os/$arch）"
  fetch "$DOWNLOAD_BASE/$version/$name.tar.gz" "$work/$name.tar.gz"
  fetch "$DOWNLOAD_BASE/$version/SHA256SUMS.txt" "$work/SHA256SUMS.txt"
  local want got
  want=$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f {print $1}' "$work/SHA256SUMS.txt")
  [ -n "$want" ] || die "SHA256SUMS.txt 里没有 $name.tar.gz"
  got=$(sha256 "$work/$name.tar.gz")
  [ "$want" = "$got" ] || die "校验和不对，下载的文件可能不完整或被篡改：$name.tar.gz"
  say "校验通过"
  tar -xzf "$work/$name.tar.gz" -C "$work"
  pkg="$work/$name"
  [ -f "$pkg/agy2api" ] || die "压缩包里没有 agy2api 程序" # 可执行权限由下面的 install -m 755 设置
}

# env_value 读环境变量文件里某个键最后一次出现的值。
env_value() {
  [ -f "$2" ] || return 0
  sed -n "s/^$1=//p" "$2" | tail -n 1
}

install_systemd() {
  [ "$(id -u)" -eq 0 ] || die "装成系统服务需要 root，请用 sudo 运行"
  local upgrade=0 old=""
  if [ -x "$BIN" ]; then
    upgrade=1
    old=$("$BIN" -version 2>/dev/null || echo 未知)
    cp -p "$BIN" "$BIN.old"
  fi
  install -m 755 "$pkg/agy2api" "$BIN"
  install -m 644 "$pkg/agy2api.service" "$SERVICE"

  if [ ! -f "$ENV_FILE" ]; then
    password=$(gen_password)
    install -d -m 700 "$ENV_DIR"
    (
      umask 077
      {
        echo "# agy2api 的环境变量，改完执行 systemctl restart agy2api 生效。全部变量见 deploy.md 的\"环境变量\"。"
        echo "AGY2API_ADMIN_PASSWORD=$password"
        echo "AGY2API_ADDR=${listen:-$DEFAULT_LISTEN}"
      } >"$ENV_FILE"
    )
  elif [ -n "$listen" ]; then
    warn "$ENV_FILE 已存在，--listen 没有生效；要改监听地址，编辑这个文件里的 AGY2API_ADDR 后重启服务"
  fi

  systemctl daemon-reload
  systemctl enable agy2api >/dev/null 2>&1
  systemctl restart agy2api
  local addr
  addr=$(env_value AGY2API_ADDR "$ENV_FILE")
  addr=${addr:-$DEFAULT_LISTEN}
  if ! wait_healthy "$addr"; then
    die "服务没有正常启动，查看日志：journalctl -u agy2api -n 50 --no-pager"
  fi

  echo
  if [ $upgrade = 1 ]; then
    say "已从 $old 升级到 $version，旧版本备份在 $BIN.old"
  else
    say "agy2api $version 已安装并启动"
  fi
  local host="${addr%:*}"
  case "$host" in "" | 0.0.0.0 | "[::]") host=服务器IP ;; esac
  echo "  管理端：http://$host:${addr##*:}"
  if [ -n "$password" ]; then
    echo "  管理口令：$password"
    echo "            （只显示这一次，之后可以在 $ENV_FILE 里查看）"
  fi
  echo "  配置：$ENV_FILE    数据：$DATA_DIR（要和配置一起备份）"
  echo "  状态：systemctl status agy2api    日志：journalctl -u agy2api -f"
  echo
  case "${addr%:*}" in
    127.0.0.1 | localhost | "[::1]")
      echo "默认只在本机监听。对外提供服务请在前面放 HTTPS 反向代理（Caddy、nginx），见 deploy.md；"
      echo "远程服务器上可以先用 SSH 隧道打开管理端：ssh -L 8249:127.0.0.1:${addr##*:} <服务器>"
      ;;
    *) warn "现在直接对外监听，管理口令和 API Key 会以明文在网络上传输；正式使用请改回本机监听，前面放 HTTPS 反向代理" ;;
  esac
}

# install_binary_only 用在没有 systemd 的系统（macOS、部分容器和精简发行版）：只装二进制，告诉用户怎么运行。
install_binary_only() {
  local dir=/usr/local/bin
  if [ ! -w "$dir" ]; then
    dir="$HOME/.local/bin"
    mkdir -p "$dir"
  fi
  install -m 755 "$pkg/agy2api" "$dir/agy2api"
  password=$(gen_password)
  echo
  say "agy2api $version 已安装到 $dir/agy2api（这个系统没有 systemd，没有装成服务）"
  echo "  运行（数据放在 ~/.agy2api，口令是刚随机生成的，可以换成自己的）："
  echo "    AGY2API_ADMIN_PASSWORD=$password AGY2API_ADDR=${listen:-$DEFAULT_LISTEN} AGY2API_DATA_DIR=\$HOME/.agy2api $dir/agy2api"
  case ":$PATH:" in
    *":$dir:"*) ;;
    *) warn "$dir 不在 PATH 里，运行时请写完整路径，或者把它加进 PATH" ;;
  esac
}

# install_docker 在当前目录放 compose.yaml 和 .env（首次生成随机口令），拉取这个版本的镜像并启动；
# 再运行一次就是升级：换成新版本的 compose.yaml 和镜像，.env 里的其它设置保留。
install_docker() {
  have docker || die "没有找到 docker"
  docker compose version >/dev/null 2>&1 || die "需要 Docker Compose 插件（docker compose）"
  local image="${AGY2API_IMAGE:-ghcr.io/$REPO:${version#v}}"
  say "下载 $version 的 compose.yaml 到 $(pwd)"
  fetch "$DOWNLOAD_BASE/$version/compose.yaml" compose.yaml
  if [ ! -f .env ]; then
    password=$(gen_password)
    (
      umask 077
      {
        echo "# agy2api 的环境变量，改完执行 docker compose up -d 生效。全部变量见 deploy.md 的\"环境变量\"。"
        echo "AGY2API_ADMIN_PASSWORD=$password"
        echo "AGY2API_BIND=${listen:-$DEFAULT_LISTEN}"
        echo "AGY2API_IMAGE=$image"
      } >.env
    )
  else
    [ -z "$listen" ] || warn ".env 已存在，--listen 没有生效；要改监听地址，编辑 .env 里的 AGY2API_BIND"
    awk -v img="$image" 'BEGIN { done = 0 } /^AGY2API_IMAGE=/ { print "AGY2API_IMAGE=" img; done = 1; next } { print } END { if (!done) print "AGY2API_IMAGE=" img }' .env >.env.new
    mv .env.new .env
  fi
  say "拉取镜像并启动"
  docker compose pull --quiet 2>/dev/null || warn "拉取镜像失败，改用本地已有的镜像"
  docker compose up -d
  local addr
  addr=$(env_value AGY2API_BIND .env)
  addr=${addr:-$DEFAULT_LISTEN}
  wait_healthy "$addr" || die "容器没有正常启动，查看日志：docker compose logs --tail 50"
  echo
  say "agy2api $version 已在 Docker 里启动（目录 $(pwd)）"
  echo "  管理端：http://${addr%:*}:${addr##*:}"
  [ -z "$password" ] || echo "  管理口令：$password（只显示这一次，之后可以在 .env 里查看）"
  echo "  日志：docker compose logs -f    停止：docker compose down（数据在命名卷里，不会删）"
}

uninstall() {
  [ "$(id -u)" -eq 0 ] || die "卸载需要 root，请用 sudo 运行"
  if have systemctl && [ -f "$SERVICE" ]; then
    systemctl disable --now agy2api >/dev/null 2>&1 || true
    rm -f "$SERVICE"
    systemctl daemon-reload
  fi
  rm -f "$BIN" "$BIN.old"
  say "已卸载 agy2api 程序和服务"
  echo "  配置和数据保留着：$ENV_DIR、$DATA_DIR（实际在 /var/lib/private/agy2api）"
  echo "  确认不要了再删：rm -rf $ENV_DIR /var/lib/private/agy2api"
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

case "$mode" in
  uninstall) uninstall ;;
  docker)
    resolve_version
    install_docker
    ;;
  install)
    detect_platform
    resolve_version
    download_package
    if [ "$os" = linux ] && have systemctl && [ -d /run/systemd/system ]; then
      install_systemd
    else
      install_binary_only
    fi
    ;;
esac
