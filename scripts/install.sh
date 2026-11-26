#!/usr/bin/env bash
# agy2api 一键安装（Linux、macOS）：从 GitHub Releases 下载对应平台的二进制包，校验 SHA256 后安装。
# 所有文件放在一个目录里，默认是当前目录下新建的 agy2api（--dir 指定别处）：程序 agy2api、配置 .env、数据 data/；
# Docker 方式是 compose.yaml、.env 和 data/。Linux 有 systemd 时装成系统服务，以安装目录所有者的身份运行（加固：
# 没有特权，只有 data/ 可写）；没有 systemd 时生成 run.sh。首次安装自动生成管理口令。再运行一次就是升级：在哪个
# 目录运行都能找到原来的安装，配置和数据保留，旧程序备份成 agy2api.old。不改系统内核参数。
#
#   curl -fsSL https://github.com/shiceai/agy2api/releases/latest/download/install.sh | sudo bash
#   ... | sudo bash -s -- --dir /opt/agy2api      装到指定目录
#   ... | sudo bash -s -- --version v1.7.0        安装指定版本
#   ... | sudo bash -s -- --listen 0.0.0.0:8249   不经反向代理直接对外（只在首次安装、生成配置时生效）
#   ... | sudo bash -s -- --listen 127.0.0.1:8249,10.8.0.1:8249   同时在本机和内网（如 WireGuard）地址上监听
#   ... | bash -s -- --docker                     用 Docker Compose 部署，不装程序
#   ... | sudo bash -s -- --uninstall             停掉并删除服务（Docker 方式是删除容器），目录里的配置和数据保留
#
# 测试用的环境变量：AGY2API_DOWNLOAD_BASE 换下载地址，AGY2API_LATEST_URL 换查最新版本的接口，
# AGY2API_IMAGE 换 Docker 方式用的镜像。
set -euo pipefail

REPO=shiceai/agy2api
DOWNLOAD_BASE="${AGY2API_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download}"
LATEST_URL="${AGY2API_LATEST_URL:-https://api.github.com/repos/$REPO/releases/latest}"
SERVICE=/etc/systemd/system/agy2api.service
# 1.4.0 及以前的安装把程序放在这里、数据在 /var/lib/agy2api，只用于认出和卸载旧安装。
LEGACY_BIN=/usr/local/bin/agy2api
DEFAULT_LISTEN=127.0.0.1:8249
# 容器里运行 agy2api 的用户（见 Dockerfile），Docker 方式的 data/ 要归它所有。
CONTAINER_UID=10001

version=""
listen=""
dir=""
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

默认装在当前目录下新建的 agy2api 目录里（程序、配置 .env、数据 data/ 都在里面）。再运行一次就是升级。

选项（放在 bash -s -- 后面）：
  --dir 路径          安装目录，默认当前目录下的 agy2api；升级、卸载时会自己找到原来的目录
  --version vX.Y.Z   安装指定版本，默认最新正式版
  --listen 地址       监听地址，默认 127.0.0.1:8249（只在本机，由反向代理对外提供 HTTPS）；
                     多个用逗号分隔，如 127.0.0.1:8249,10.8.0.1:8249（Docker 方式只能一个）
  --docker           用 Docker Compose 部署（不装程序）
  --uninstall        卸载：停掉并删除服务或容器，目录里的配置和数据保留
  -h, --help         显示这段说明
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dir)
      [ $# -ge 2 ] || die "--dir 后面要写目录，如 /opt/agy2api"
      dir="$2"
      shift 2
      ;;
    --version)
      [ $# -ge 2 ] || die "--version 后面要写版本，如 v1.7.0"
      version="$2"
      shift 2
      ;;
    --listen)
      [ $# -ge 2 ] || die "--listen 后面要写地址，如 0.0.0.0:8249"
      listen="$2"
      shift 2
      ;;
    --docker)
      [ "$mode" = uninstall ] || mode=docker
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
  die "版本号格式不对：$version（应为 v1.7.0 这样）"
fi
if [ -n "$listen" ]; then
  listen="${listen// /}"
  IFS=, read -ra listen_list <<<"$listen"
  [ "${#listen_list[@]}" -gt 0 ] || die "--listen 后面要写地址，如 0.0.0.0:8249"
  for a in "${listen_list[@]}"; do
    if [[ ! "$a" =~ ^(\[[0-9A-Fa-f:]+\]|[0-9A-Za-z.-]*):[0-9]{1,5}$ ]]; then
      die "监听地址格式不对：$a（应为 0.0.0.0:8249、127.0.0.1:8249 或 :8249，多个用逗号分隔）"
    fi
  done
  if [ "$mode" = docker ] && [ "${#listen_list[@]}" -gt 1 ]; then
    die "Docker 方式只能写一个监听地址；其他地址在 compose.yaml 旁边的 compose.override.yaml 里加端口映射，见 deploy.md"
  fi
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

# first_addr 取监听地址列表（逗号分隔）里的第一个，去掉空格。
first_addr() {
  local a="${1// /}"
  echo "${a%%,*}"
}

# wait_healthy 等服务的 /healthz 通过，最多 30 秒。$1 是监听地址（多个时用第一个），0.0.0.0 和空主机按本机访问。
wait_healthy() {
  local addr
  addr=$(first_addr "$1")
  local host="${addr%:*}" port="${addr##*:}"
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

# service_dir 输出 systemd 服务记的安装目录；1.4.0 及以前的安装输出 legacy；没有服务时什么也不输出。
service_dir() {
  [ -f "$SERVICE" ] || return 0
  local d
  d=$(sed -n 's/^WorkingDirectory=//p' "$SERVICE" | head -n 1)
  if [ -n "$d" ]; then
    echo "$d"
  elif grep -q "^ExecStart=$LEGACY_BIN" "$SERVICE"; then
    echo legacy
  fi
}

# kind_of 报告目录 $1 里装的是哪种：docker、binary（systemd 或 run.sh），不是安装目录时为空。
# 源码目录（有 go.mod）不算。
kind_of() {
  [ -f "$1/.env" ] && [ ! -e "$1/go.mod" ] || return 0
  if [ -f "$1/compose.yaml" ] && grep -q "$REPO" "$1/compose.yaml"; then
    echo docker
  elif [ -f "$1/agy2api" ]; then
    echo binary
  fi
}

# absolute_dir 把 --dir 给的目录换成绝对路径（相对路径按当前目录），去掉末尾的 /，检查字符。
absolute_dir() {
  case "$dir" in /*) ;; *) dir="$PWD/$dir" ;; esac
  while [ "${#dir}" -gt 1 ] && [ "${dir%/}" != "$dir" ]; do dir="${dir%/}"; done
  [[ "$dir" =~ ^/[A-Za-z0-9._/-]*$ ]] || die "安装目录只能用字母、数字和 . _ - /：$dir"
  [ "$dir" != / ] || die "不能装在根目录，换个目录（--dir）"
}

# resolve_dir 定安装目录：--dir 给了就用；否则 systemd 服务记的目录（Docker 方式不看）；否则当前目录本身是安装目录
# 就用当前目录（种类不对时提示）；都没有时是当前目录下的 agy2api（已有安装就是升级，没有就新建）。
# $1 是安装种类（docker、binary）。
# 已经存在、里面有别的文件的目录不用，免得改了它的权限或者混进别的东西。
resolve_dir() {
  local want="$1"
  if [ -z "$dir" ]; then
    local svc=""
    [ "$want" = docker ] || svc=$(service_dir)
    if [ -n "$svc" ] && [ "$svc" != legacy ]; then
      dir="$svc"
    elif [ -n "$(kind_of "$PWD")" ]; then
      dir="$PWD"
    else
      dir="$PWD/agy2api"
    fi
  fi
  absolute_dir
  local found
  found=$(kind_of "$dir")
  if [ -z "$found" ] && [ -d "$dir" ] && [ -n "$(ls -A "$dir")" ]; then
    die "$dir 已经存在，里面有别的文件，不是 agy2api 的安装目录；换个目录（--dir）"
  fi
  if [ -n "$found" ] && [ "$found" != "$want" ]; then
    if [ "$found" = docker ]; then
      die "$dir 是 Docker 方式的安装：升级加 --docker，或者用 --dir 换个目录"
    fi
    die "$dir 是装成程序的安装（systemd 或 run.sh）：去掉 --docker 升级，或者用 --dir 换个目录"
  fi
}

# write_env 首次安装时生成配置 $1（随机口令、监听地址），$2 是监听地址的变量名，$3 是第一行的说明，
# 之后的参数是额外写进去的行。已经有配置时不动，--listen 不生效。
write_env() {
  local file="$1" key="$2" note="$3"
  shift 3
  if [ -f "$file" ]; then
    [ -z "$listen" ] || warn "$file 已存在，--listen 没有生效；要改监听地址，编辑这个文件里的 $key"
    return
  fi
  password=$(gen_password)
  (
    umask 077
    {
      echo "# $note 全部变量见 deploy.md 的\"环境变量\"。"
      echo "AGY2API_ADMIN_PASSWORD=$password"
      echo "$key=${listen:-$DEFAULT_LISTEN}"
      for line in "$@"; do echo "$line"; done
    } >"$file"
  )
}

# show_access 打印管理端地址（每个监听地址一行）和首次生成的口令。$1 是监听地址（逗号分隔），$2 是配置文件。
show_access() {
  local a host
  local -a list
  IFS=, read -ra list <<<"${1// /}"
  for a in "${list[@]}"; do
    host="${a%:*}"
    case "$host" in "" | 0.0.0.0 | "[::]") host=服务器IP ;; esac
    echo "  管理端：http://$host:${a##*:}"
  done
  [ -z "$password" ] || echo "  管理口令：$password（只显示这一次，之后可以在 $2 里查看）"
}

# addr_kind 判断一个监听地址：loopback（只在本机）、private（内网，含 WireGuard、Tailscale 常用的网段）、
# public（所有网卡或公网地址）。
addr_kind() {
  local host="${1%:*}"
  host="${host#[}"
  host="${host%]}"
  case "$host" in
    127.* | localhost | ::1) echo loopback ;;
    "" | 0.0.0.0 | ::) echo public ;;
    10.* | 192.168.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].*) echo private ;;
    100.6[4-9].* | 100.[7-9][0-9].* | 100.1[01][0-9].* | 100.12[0-7].*) echo private ;;
    fc* | fd* | FC* | FD*) echo private ;;
    *) echo public ;;
  esac
}

# show_listen_hint 按监听地址给提示：对外的警告明文传输，内网的说明谁能访问，只在本机的提示反向代理和 SSH 隧道。
show_listen_hint() {
  local a loopback="" private="" public=""
  local -a list
  IFS=, read -ra list <<<"${1// /}"
  for a in "${list[@]}"; do
    case "$(addr_kind "$a")" in
      loopback) loopback="$a" ;;
      private) private="${private:+$private, }$a" ;;
      *) public="${public:+$public, }$a" ;;
    esac
  done
  if [ -n "$public" ]; then
    warn "现在直接对外监听（$public），管理口令和 API Key 会以明文在网络上传输；正式使用请改回本机或内网监听，前面放 HTTPS 反向代理"
    return
  fi
  if [ -n "$private" ]; then
    echo "在内网地址上监听（$private），同一内网（如 WireGuard）里的机器可以直接访问；"
    echo "管理端可以用 AGY2API_ADMIN_ALLOW 只允许内网网段，开机时要等内网网卡起来再启动，见 deploy.md。"
  fi
  if [ -n "$loopback" ]; then
    [ -n "$private" ] || echo -n "默认只在本机监听。"
    echo "对外提供服务请在前面放 HTTPS 反向代理（Caddy、nginx），见 deploy.md；"
    echo "远程服务器上可以先用 SSH 隧道打开管理端：ssh -L 8249:127.0.0.1:${loopback##*:} <服务器>"
  fi
}

# install_program 把程序装进安装目录，原来的程序备份成 agy2api.old。$1、$2 是所有者和组（为空时不改）。
install_program() {
  local owner="$1" group="$2" old_version=""
  if [ -f "$dir/agy2api" ]; then
    old_version=$("$dir/agy2api" -version 2>/dev/null || echo 未知)
    cp -p "$dir/agy2api" "$dir/agy2api.old"
  fi
  if [ -n "$owner" ]; then
    install -m 755 -o "$owner" -g "$group" "$pkg/agy2api" "$dir/agy2api"
  else
    install -m 755 "$pkg/agy2api" "$dir/agy2api"
  fi
  echo "$old_version"
}

# install_systemd 装成 systemd 服务：服务文件按安装目录生成，以目录所有者的身份运行（首次安装时是 sudo 前的
# 用户，直接用 root 运行就是 root），只有 data/ 可写。
install_systemd() {
  [ "$(id -u)" -eq 0 ] || die "装成系统服务需要 root，请用 sudo 运行"
  if [ "$(service_dir)" = legacy ]; then
    die "这台机器上有 1.4.0 及以前装的 agy2api（程序在 $LEGACY_BIN）。新版改成装在一个目录里：
  先运行 --uninstall 卸载旧版（数据保留在 /var/lib/private/agy2api），再重新安装；
  要沿用旧数据，把那里的 agy2api.db、agy2api.key 复制到新目录的 data/ 里，改成目录所有者后重启服务"
  fi
  local svc
  svc=$(service_dir)
  if [ -n "$svc" ] && [ "$svc" != "$dir" ]; then
    die "agy2api 已经装在 $svc：升级不用加 --dir；要换目录，先运行 --uninstall"
  fi
  case "$dir" in
    /tmp | /tmp/* | /var/tmp | /var/tmp/*)
      die "不能装在 $dir：临时目录会被清理，服务也看不到它（服务用自己的临时目录）；换个目录（--dir）"
      ;;
  esac

  local owner group
  if [ -d "$dir" ]; then
    owner=$(stat -c %U "$dir")
  else
    owner="${SUDO_USER:-root}"
    id "$owner" >/dev/null 2>&1 || owner=root
  fi
  group=$(id -gn "$owner")
  [ -d "$dir" ] || install -d -m 750 -o "$owner" -g "$group" "$dir"
  install -d -m 700 -o "$owner" -g "$group" "$dir/data"
  if [ "$owner" != root ] && have runuser && ! runuser -u "$owner" -- test -x "$dir/data"; then
    die "用户 $owner 进不了 $dir（上级目录不让进），服务没法运行；换个目录（--dir），如 /opt/agy2api"
  fi

  local old_version
  old_version=$(install_program "$owner" "$group")
  write_env "$dir/.env" AGY2API_ADDR "agy2api 的环境变量，改完执行 systemctl restart agy2api 生效。"
  chown "$owner:$group" "$dir/.env"
  write_unit "$owner" "$group"

  systemctl daemon-reload
  systemctl enable agy2api >/dev/null 2>&1
  systemctl restart agy2api
  local addr
  addr=$(env_value AGY2API_ADDR "$dir/.env")
  addr=${addr:-$DEFAULT_LISTEN}
  if ! wait_healthy "$addr"; then
    die "服务没有正常启动，查看日志：journalctl -u agy2api -n 50 --no-pager"
  fi

  echo
  if [ -n "$old_version" ]; then
    say "已从 $old_version 升级到 $version，旧程序备份在 $dir/agy2api.old"
  else
    say "agy2api $version 已安装并启动（目录 $dir，以用户 $owner 运行）"
  fi
  show_access "$addr" "$dir/.env"
  echo "  配置：$dir/.env    数据：$dir/data（备份时连 .env 一起）"
  echo "  状态：systemctl status agy2api    日志：journalctl -u agy2api -f"
  echo "  升级：再运行一次安装命令    卸载：加 --uninstall"
  echo
  show_listen_hint "$addr"
}

# write_unit 生成 systemd 服务文件。$1、$2 是运行服务的用户和组。
write_unit() {
  cat >"$SERVICE" <<EOF
# agy2api 的 systemd 服务，由安装脚本生成（再运行安装脚本时会覆盖）。安装目录 $dir：
# 程序 agy2api、配置 .env、数据 data/（库和加密密钥，要连 .env 一起备份）。
[Unit]
Description=agy2api
Wants=network-online.target
After=network-online.target

[Service]
User=$1
Group=$2
WorkingDirectory=$dir
ExecStart=$dir/agy2api
# 默认只在本机监听，由反向代理对外提供 HTTPS；.env 里的同名变量优先。
Environment=AGY2API_ADDR=$DEFAULT_LISTEN
Environment=AGY2API_DATA_DIR=$dir/data
EnvironmentFile=$dir/.env
Restart=on-failure
RestartSec=5
# 关停时先等进行中的请求（最多 10 秒），再写完排队的用量记录（最多 5 秒）。
TimeoutStopSec=30
# 文件描述符上限：Go 启动时会把软限制提到硬限制，这里把硬限制统一定成 524288（新版 systemd 的默认值），
# 老系统（systemd 240 以前默认 4096）上也一样。只影响本服务，不改内核参数。
LimitNOFILE=524288

# 加固：没有任何特权（root 运行时也一样），系统和家目录只读、只有 data/ 可写，网络只用 IPv4、IPv6 和本地套接字，
# 系统调用限于普通服务需要的（其余返回 EPERM，不杀进程）。
CapabilityBoundingSet=
UMask=0077
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
ProtectProc=invisible
ProcSubset=pid
ProtectKernelLogs=yes
PrivateUsers=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=$dir/data
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
EOF
  chmod 644 "$SERVICE"
}

# install_run_script 用在没有 systemd 的系统（macOS、部分容器和精简发行版）：程序装进安装目录，生成 run.sh
# （读 .env 后在前台运行），告诉用户怎么启动。
install_run_script() {
  mkdir -p "$dir/data"
  local old_version
  old_version=$(install_program "" "")
  write_env "$dir/.env" AGY2API_ADDR "agy2api 的环境变量，改完重新运行 run.sh 生效。"
  cat >"$dir/run.sh" <<'EOF'
#!/bin/sh
# 在前台运行 agy2api（Ctrl+C 停止）：读同目录的 .env，数据在同目录的 data/。由安装脚本生成。
cd "$(dirname "$0")" || exit 1
set -a
. ./.env
set +a
exec ./agy2api
EOF
  chmod 755 "$dir/run.sh"
  echo
  if [ -n "$old_version" ]; then
    say "已从 $old_version 升级到 $version，旧程序备份在 $dir/agy2api.old（重新运行 run.sh 换成新版本）"
  else
    say "agy2api $version 已安装到 $dir（这个系统没有 systemd，没有装成服务）"
  fi
  local addr
  addr=$(env_value AGY2API_ADDR "$dir/.env")
  addr=${addr:-$DEFAULT_LISTEN}
  echo "  运行：$dir/run.sh"
  show_access "$addr" "$dir/.env"
  echo "  配置：$dir/.env    数据：$dir/data（备份时连 .env 一起）"
}

# install_docker 在安装目录放 compose.yaml 和 .env（首次生成随机口令），拉取这个版本的镜像并启动，数据挂在 data/。
# 再运行一次就是升级：换成新版本的 compose.yaml 和镜像，.env 里的其它设置保留。
install_docker() {
  have docker || die "没有找到 docker"
  docker compose version >/dev/null 2>&1 || die "需要 Docker Compose 插件（docker compose）"
  local image="${AGY2API_IMAGE:-ghcr.io/$REPO:${version#v}}"
  if [ -f "$dir/compose.yaml" ] && grep -qE '^[[:space:]]*- data:/data' "$dir/compose.yaml"; then
    local volume
    volume="$(basename "$dir")_data"
    die "$dir 是 1.4.0 及以前装的，数据在 Docker 卷 $volume 里，新版改放在 data/。先把数据复制出来再运行：
  cd $dir && docker compose down && mkdir -p data && docker run --rm -v $volume:/from -v $dir/data:/to alpine sh -c 'cp -a /from/. /to/ && chown -R $CONTAINER_UID:$CONTAINER_UID /to'
  （项目名改过的，卷名用 docker volume ls 查）"
  fi
  mkdir -p "$dir/data"
  cd "$dir"
  say "下载 $version 的 compose.yaml 到 $dir"
  fetch "$DOWNLOAD_BASE/$version/compose.yaml" compose.yaml
  if [ -f .env ]; then
    write_env .env AGY2API_BIND ""
    awk -v img="$image" 'BEGIN { done = 0 } /^AGY2API_IMAGE=/ { print "AGY2API_IMAGE=" img; done = 1; next } { print } END { if (!done) print "AGY2API_IMAGE=" img }' .env >.env.new
    mv .env.new .env
  else
    write_env .env AGY2API_BIND "agy2api 的环境变量，改完执行 docker compose up -d 生效。" "AGY2API_IMAGE=$image"
  fi
  say "拉取镜像并启动"
  docker compose pull --quiet 2>/dev/null || warn "拉取镜像失败，改用本地已有的镜像"
  # data/ 要归容器里的用户（UID 10001）：用这个镜像起一个临时容器改，不需要 sudo。
  docker run --rm --user 0 --entrypoint chown -v "$dir/data:/data" "$image" -R "$CONTAINER_UID:$CONTAINER_UID" /data ||
    warn "没能把 $dir/data 改成 UID $CONTAINER_UID 所有，容器可能写不了数据；可以手动执行 sudo chown -R $CONTAINER_UID:$CONTAINER_UID $dir/data"
  docker compose up -d
  local addr
  addr=$(env_value AGY2API_BIND .env)
  addr=${addr:-$DEFAULT_LISTEN}
  wait_healthy "$addr" || die "容器没有正常启动，查看日志：cd $dir && docker compose logs --tail 50"
  echo
  say "agy2api $version 已在 Docker 里启动（目录 $dir）"
  show_access "$addr" "$dir/.env"
  echo "  配置：$dir/.env    数据：$dir/data（备份时连 .env 一起）"
  echo "  日志：cd $dir && docker compose logs -f    升级：再运行一次安装命令（加 --docker）"
  echo
  show_listen_hint "$addr"
}

# uninstall 卸载：systemd 服务停掉并删除服务文件，Docker 方式删除容器，目录里的程序、配置和数据都保留，给出彻底
# 删除的命令。找安装的顺序：--dir；当前目录是安装目录；systemd 服务记的目录；当前目录下的 agy2api。
# 1.4.0 及以前的安装还会删掉 /usr/local/bin 里的程序。
uninstall() {
  local svc
  svc=$(service_dir)
  if [ -n "$dir" ]; then
    absolute_dir
  elif [ -n "$(kind_of "$PWD")" ]; then
    dir="$PWD"
  elif [ -n "$svc" ]; then
    [ "$svc" = legacy ] || dir="$svc"
  elif [ -n "$(kind_of "$PWD/agy2api")" ]; then
    dir="$PWD/agy2api"
  else
    die "没找到 agy2api 的安装：到安装目录里运行，或者用 --dir 指定"
  fi

  if [ -n "$dir" ] && [ "$(kind_of "$dir")" = docker ]; then
    have docker || die "没有找到 docker"
    (cd "$dir" && docker compose down)
    say "已删除 agy2api 的容器"
    echo "  配置和数据保留在 $dir（data/ 归 UID $CONTAINER_UID）"
    echo "  确认不要了再删：sudo rm -rf $dir"
    return
  fi
  if [ -n "$svc" ] && { [ -z "$dir" ] || [ "$svc" = "$dir" ]; }; then
    [ "$(id -u)" -eq 0 ] || die "卸载系统服务需要 root，请用 sudo 运行"
    systemctl disable --now agy2api >/dev/null 2>&1 || true
    rm -f "$SERVICE"
    systemctl daemon-reload
    if [ "$svc" = legacy ]; then
      rm -f "$LEGACY_BIN" "$LEGACY_BIN.old"
      say "已卸载 agy2api 的服务和程序"
      echo "  配置和数据保留着：/etc/agy2api、/var/lib/private/agy2api"
      echo "  确认不要了再删：rm -rf /etc/agy2api /var/lib/private/agy2api"
      return
    fi
    say "已停止并删除 agy2api 服务"
  elif [ -n "$(kind_of "$dir")" ]; then
    say "$dir 没有装成服务，不用停止（run.sh 在前台运行，Ctrl+C 即停）"
  else
    die "$dir 不是 agy2api 的安装目录"
  fi
  echo "  程序、配置和数据保留在 $dir"
  echo "  确认不要了再删：rm -rf $dir"
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

case "$mode" in
  uninstall) uninstall ;;
  docker)
    resolve_dir docker
    resolve_version
    install_docker
    ;;
  install)
    detect_platform
    resolve_dir binary
    resolve_version
    download_package
    if [ "$os" = linux ] && have systemctl && [ -d /run/systemd/system ]; then
      install_systemd
    else
      install_run_script
    fi
    ;;
esac
