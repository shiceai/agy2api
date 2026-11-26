# 部署

AGY2API 的数据（SQLite 库和凭据加密密钥）都在数据目录里。最简单的是用一键安装脚本（见下文），它把程序、配置和数据放在
一个目录里，装成下面两种方式之一：

- **systemd**：Linux 二进制跑成系统服务。
- **Docker Compose**：用发布的镜像，`compose.yaml`、`.env` 和数据目录 `data/` 在一起。

两种方式默认都只在本机 8249 端口监听（避开 8080 这类常用端口；249 是 AGY 在电话键盘上的位置），由同一台机器上的反向代理对外提供 HTTPS。
各接口的细节（转换规则、限制、会话粘性）见[接口说明](api.md)。

## 上公网前

- 管理口令用足够长的随机串（如 `openssl rand -base64 24`），不要用默认值或短口令。口令少于 12 个字符或只有一类字符时，
  启动日志里会有"管理口令太弱"的警告。
- 只通过 HTTPS 访问：管理口令和 API Key 都放在请求头里，明文 HTTP 会被中途看到。
- 设 `AGY2API_ADMIN_ALLOW`，只让自己的 IP 访问管理端（见下文"客户端 IP 和管理端访问限制"）。
- 备份数据目录里的 `agy2api.db` 和 `agy2api.key`：凭据用这个密钥加密，丢了密钥库里的账号就用不了。

## 一键安装

```bash
curl -fsSL https://github.com/shiceai/agy2api/releases/latest/download/install.sh | sudo bash
```

脚本从 Releases 下载对应平台（Linux、macOS，amd64、arm64）的压缩包，按 `SHA256SUMS.txt` 校验，不一致就停下。
所有文件放在一个目录里，默认是当前目录下新建的 `agy2api`，用 `--dir` 指定别处：

```text
agy2api/
  agy2api        程序
  agy2api.old    升级前的旧程序
  .env           配置（权限 600）
  data/          数据：库、加密密钥，以及在线升级下载的新程序和备份
```

- Linux 有 systemd 时装成服务 `agy2api`，以安装目录所有者的身份运行：用 sudo 的普通用户装就是这个用户，直接用 root
  装就是 root。服务没有任何特权，系统和家目录只读，只能写 `data/`。服务文件按安装目录生成在
  `/etc/systemd/system/agy2api.service`。
- 安装目录不能在 `/tmp`、`/var/tmp` 下（会被清理，服务也看不到），服务的用户要进得去（普通用户装到 `/root` 下会被拒）；
  已经存在、里面有别的文件的目录也不用。
- 没有 systemd 的系统（macOS、部分精简发行版）不装成服务，目录里多一个 `run.sh`：读 `.env` 后在前台运行。
- 首次安装自动生成 24 位随机管理口令写进 `.env`，只在安装结束时显示一次，之后到 `.env` 里查看。默认只监听
  `127.0.0.1:8249`，对外请放 HTTPS 反向代理；远程服务器上可以先用 `ssh -L 8249:127.0.0.1:8249 <服务器>` 打开管理端。
- 再运行一次就是升级：脚本会找到原来的安装（systemd 服务记的目录，或者当前目录就是安装目录），只换程序，配置和数据
  不动，旧程序备份成 `agy2api.old`，要回退时换回去重启即可。没有 systemd 时到安装目录或它的上级目录里运行，或用 `--dir`。
- 1.4.0 及以前的一键安装把文件放在系统目录（程序 `/usr/local/bin/agy2api`、数据 `/var/lib/private/agy2api`），新脚本
  不会原地升级它：先 `--uninstall`，再重新安装，要沿用旧数据就把旧数据目录里的 `agy2api.db`、`agy2api.key` 复制到新的
  `data/` 里，改成安装目录的所有者后重启服务。
- 不改系统内核参数。

选项放在 `bash -s --` 后面：

| 选项 | 作用 |
| --- | --- |
| `--dir /opt/agy2api` | 安装目录，默认当前目录下的 `agy2api`；升级、卸载时会自己找到原来的目录 |
| `--version v1.7.0` | 安装指定版本，默认最新正式版 |
| `--listen 0.0.0.0:8249` | 不经反向代理直接对外（只在首次安装、生成配置时生效；会提示明文传输的风险） |
| `--docker` | 不装程序，在安装目录放 `compose.yaml` 和 `.env`（随机口令），数据在 `data/`，然后启动容器；不需要 sudo；再运行一次换成新版本 |
| `--uninstall` | 卸载，见下文"卸载" |

## Docker Compose

用发布的镜像（`ghcr.io/shiceai/agy2api`，linux/amd64、linux/arm64），只需要 `compose.yaml` 和 `.env`：

```bash
mkdir agy2api && cd agy2api
curl -fsSLO https://raw.githubusercontent.com/shiceai/agy2api/main/compose.yaml
curl -fsSL https://raw.githubusercontent.com/shiceai/agy2api/main/deploy/env.example -o .env   # 至少填上 AGY2API_ADMIN_PASSWORD
mkdir -p data && sudo chown 10001:10001 data   # 容器以 UID 10001 运行，数据目录要归它
docker compose up -d
docker compose logs -f
```

- 默认用最新的正式版（`latest`）。要固定版本，在 `.env` 里写 `AGY2API_IMAGE=ghcr.io/shiceai/agy2api:1.7.0`。
- 从源码构建：在仓库根目录 `VERSION=$(git describe --tags --always --dirty) docker compose up -d --build`。
  构建出的镜像也叫 `ghcr.io/shiceai/agy2api:latest`，要换回发布的镜像时执行 `docker compose pull`。挂的是仓库里的 `data/`，
  和本地直接运行的默认数据目录是同一个，不要同时运行两份。
- 核验镜像确实由本仓库的发布流程构建：`gh attestation verify oci://ghcr.io/shiceai/agy2api:<版本> -R shiceai/agy2api`。
- 镜像分三段构建：前端（Node + pnpm）→ Go 单二进制（无 CGO）→ Alpine 运行镜像，以非 root 用户（UID 10001）运行。
- 数据在 `compose.yaml` 同目录的 `data/`，要归 UID 10001 所有（一键安装用临时容器改，不需要 sudo）。备份时连 `.env` 一起。
  1.4.0 及以前的 `compose.yaml` 把数据放在命名卷 `<目录名>_data` 里，换成新的 `compose.yaml` 前先把卷里的数据复制出来：
  `docker compose down && docker run --rm -v <目录名>_data:/from -v "$PWD/data":/to alpine sh -c 'cp -a /from/. /to/ && chown -R 10001:10001 /to'`。
- 镜像带健康检查（请求 `/healthz`）。改了 `AGY2API_ADDR` 的端口时，健康检查也要跟着改。
- 关停时网关先等进行中的请求（最多 10 秒），再写完排队的用量记录（最多 5 秒），`compose.yaml` 已把
  `stop_grace_period` 设成 30 秒。

## systemd

不用一键安装时，可以手动装成服务，程序、配置和数据放在系统目录：

```bash
# 从 Releases 下载对应平台的压缩包，解压后在包目录里执行（包里有二进制、env.example 和 agy2api.service）。
# 也可以在开发机上 bash scripts/build.sh linux/amd64 自己编译，再把 bin/agy2api-linux-amd64 当作 agy2api 拷过去。
install -m 755 agy2api /usr/local/bin/agy2api
install -D -m 600 env.example /etc/agy2api/env        # 填上 AGY2API_ADMIN_PASSWORD 等
install -m 644 agy2api.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now agy2api
journalctl -u agy2api -f
```

- 服务用 `DynamicUser` 跑在临时的非 root 用户下，只能写自己的数据目录 `/var/lib/agy2api`
  （实际在 `/var/lib/private/agy2api`），其余文件系统只读。
- `/etc/agy2api/env` 里的变量优先于服务文件里的默认值（监听地址、数据目录）。
- 连接数：服务文件把文件描述符上限定成 524288（Go 启动时会把软限制提到这个值），够网关用；不需要改内核参数。
  客户端离服务器很远、流式响应慢时，可以自己评估是否开启 BBR 拥塞控制，这属于整台机器的设置，安装脚本不会动。

## 反向代理

网关有流式响应（SSE），反代要关掉缓冲；非流式请求最长 10 分钟，读超时要比它长；入站请求体上限 16 MiB。

Caddy（自动申请 HTTPS 证书，SSE 会自动逐块转发）：

```
api.example.com {
	reverse_proxy 127.0.0.1:8249
}
```

nginx：

```nginx
server {
    listen 443 ssl;
    server_name api.example.com;
    # ssl_certificate、ssl_certificate_key 省略

    client_max_body_size 16m;

    location / {
        proxy_pass http://127.0.0.1:8249;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;          # 流式响应逐块转发
        proxy_read_timeout 660s;      # 非流式最长 10 分钟
    }
}
```

- 客户端用 `?key=` 传 API Key 时，反代的访问日志默认会记下带 Key 的完整地址。让客户端改用请求头
  （`x-goog-api-key` 或 `Authorization: Bearer`），或者把反代的日志格式改成不记查询串（nginx 用 `$uri` 代替 `$request`）。

## 内网访问（如 WireGuard）

机器之间组了内网（WireGuard、Tailscale 等）时，可以让网关同时在本机和内网地址上监听：本机的反向代理照常对外，内网里的
机器直接访问内网地址。`AGY2API_ADDR` 写多个地址，用逗号分隔：

```bash
AGY2API_ADDR=127.0.0.1:8249,10.8.0.1:8249   # 10.8.0.1 换成本机的内网地址
```

- 每个地址各开一个监听，共用同一个服务；有一个地址监听不了，服务就不启动，日志里写明是哪个地址。
- 只要内网访问的话，只写内网地址也行：本机的反向代理连这个地址照样通（本机进程能访问本机任意网卡上的地址）。
- 开机时内网网卡要先起来，否则监听失败、服务反复重启；在线升级的新程序连续 3 次没能开始监听还会被当成起不来、
  退回原来的版本。systemd 里加上启动顺序（WireGuard 的服务名按实际的写）：

  ```bash
  sudo systemctl edit agy2api
  # 写入：
  # [Unit]
  # After=wg-quick@wg0.service
  # Wants=wg-quick@wg0.service
  ```

- Docker 方式的 `.env` 里 `AGY2API_BIND` 是端口映射，只能写一个地址；要多监听一个，在 `compose.yaml` 旁边建
  `compose.override.yaml`（`docker compose` 会自动合并，再次运行安装脚本也不会动它），然后 `docker compose up -d`：

  ```yaml
  services:
    agy2api:
      ports:
        - "10.8.0.1:8249:8249"
  ```

  Docker 先于内网网卡启动时容器会起不来，`restart: unless-stopped` 会自动重试，网卡起来后就正常了。
- 不建议改成监听所有地址（`0.0.0.0`）再靠防火墙挡公网：防火墙配错时 8249 就以明文对公网开放，而且 Docker 的端口映射
  会绕过 ufw 的规则。
- 管理端可以用 `AGY2API_ADMIN_ALLOW` 只允许内网网段，如 `10.8.0.0/24`（见下一节）。

## 客户端 IP 和管理端访问限制

客户端 IP 用于访问日志、用量记录和管理端白名单。

- `AGY2API_TRUSTED_PROXIES`：可信的反向代理。只有直连的对端在其中时才看 `X-Forwarded-For`（从右往左跳过可信反代，
  第一个不可信的地址就是客户端），否则转发头可被客户端伪造。默认是回环和内网地址：反代和网关在同一台机器，
  或者在同一个 Docker 网络里时不用改。网关直接对公网、前面没有反代时，设成 `none`。
- `AGY2API_ADMIN_ALLOW`：只允许这些 IP 访问管理端的接口和页面，其他地址返回 403，连口令都试不了；网关接口
  （`/v1beta`、`/v1`）和 `/healthz` 不受限。写法如 `203.0.113.7,198.51.100.0/24`。默认不限。

两个变量写错时服务拒绝启动，日志里会说明是哪一个。

管理口令输错有次数限制：同一个地址 15 分钟内试了 10 个不同的错误口令，这个地址的管理端锁定 15 分钟（再犯翻倍，最长
24 小时），锁定期间口令对了也进不去，登录页会提示还要等多久；网关接口不受影响。这个限制按客户端 IP 计数，所以反代一定要
在 `AGY2API_TRUSTED_PROXIES` 里：否则所有请求看起来都来自反代的地址，一个人猜错就会把所有人锁在外面（反代在另一台公网机器上时
要把它的地址加进去）。被误锁时重启服务即可解除（计数只在内存里）。

## 备份和升级

- 备份：停服务后复制整个数据目录（`agy2api.db` 和 `agy2api.key` 一起）和配置，或者用 `sqlite3 agy2api.db ".backup 备份路径"`
  在线备份数据库，密钥文件另外复制一份。一键安装的直接打包安装目录里的 `.env` 和 `data/`：

  ```bash
  cd agy2api
  sudo systemctl stop agy2api        # Docker 方式：docker compose stop
  sudo tar czf ../agy2api-backup.tgz .env data
  sudo systemctl start agy2api       # Docker 方式：docker compose start
  ```

- 升级：管理端可以在线升级（见下一节）；也可以手动升级：一键安装的再运行一次安装脚本（Docker 模式加 `--docker`）；手动部署的 Docker 执行 `docker compose pull && docker compose up -d`（从源码构建的 `git pull` 后加 `--build`）；systemd 换成
  新版本包里的 `agy2api` 后 `systemctl restart agy2api`。各版本的变化见 `CHANGELOG.md`。数据库迁移在启动时自动执行。升级前先备份，要回退时用旧版本加备份的数据。

## 在线升级

点管理端侧边栏的版本号（手机上是设置页底部的版本号），弹窗里显示 GitHub 上最新的正式版和更新内容，可以一键升级。

- 服务每天查一次 GitHub Releases（`api.github.com`），打开管理端或从后台切回管理端时也会查（10 分钟内最多一次）；
  有新版本时侧边栏的版本号变成绿色并带向上箭头，手机顶栏出现升级图标。只看正式版，不看预发布版本。
- 升级时下载当前平台的压缩包，按 `SHA256SUMS.txt` 校验，再运行一次新程序核对版本号；把数据库备份到数据目录的
  `backups/agy2api-before-<版本>.db`（只留最近一份），然后把新程序放进数据目录的 `upgrade/`，服务原地重启换成它：
  进程号不变，中断几秒，进行中的请求先完成。
- 新程序放在数据目录，不替换镜像或安装的程序，启动时在两者中选版本较新的运行。所以 Docker 容器重建（改了 `.env` 后
  `docker compose up -d`、`down` 再 `up`）、systemd 重启之后都还是新版本，`.env` 里固定的镜像版本不用跟着改。
- 之后换上同版本或更新的镜像、安装包（改 `AGY2API_IMAGE` 后 `docker compose pull && docker compose up -d`，或重新运行安装
  脚本），数据目录里的新程序会自动清理。
- 新程序连续 3 次没能启动（开始监听之前就退出）时自动停用，退回原来的程序，管理端会提示。
- "撤销在线升级"删掉新程序，重启回到镜像或安装的程序的版本。新版本改过数据库结构时，旧版本可能不兼容；要完全回退，
  停服务后用 `backups/` 里的备份替换 `agy2api.db`（同时删掉 `agy2api.db-wal`、`agy2api.db-shm`）。
- 支持 Linux、macOS 的 amd64、arm64。Windows 和自己编译的开发构建不支持，管理端会说明原因；Windows 上有新版本时给出下载链接。
- `AGY2API_UPDATE=off` 关闭：不连 GitHub，也不运行数据目录里在线升级的程序。连 GitHub 走 `HTTPS_PROXY`。

## 卸载

一键安装的，在任意目录运行（Docker 方式在安装目录或它的上级目录里运行，不需要 sudo；也可以加 `--dir` 指定）：

```bash
curl -fsSL https://github.com/shiceai/agy2api/releases/latest/download/install.sh | sudo bash -s -- --uninstall
```

- systemd：停掉并删除服务，安装目录里的程序、配置和数据都保留。
- Docker 方式：删除容器（`docker compose down`），安装目录保留。
- 没有 systemd 的：先 Ctrl+C 停掉 `run.sh`，脚本只给出删除命令。
- 确认不要了再删除安装目录：`rm -rf <安装目录>`（Docker 方式的 `data/` 归 UID 10001，要 sudo）。想留数据先备份 `.env` 和 `data/`。
- 1.4.0 及以前的一键安装也用这条命令卸载：删除服务和 `/usr/local/bin/agy2api`，配置和数据留在 `/etc/agy2api`、
  `/var/lib/private/agy2api`，确认不要了再删。

手动部署的：

- Docker Compose：在 `compose.yaml` 所在目录执行 `docker compose down`，再删除这个目录（`data/` 要 sudo）。1.4.0 及以前的
  `compose.yaml` 用命名卷，`docker compose down -v` 会连数据卷一起删掉，确认不要数据才加 `-v`。
- systemd：

  ```bash
  sudo systemctl disable --now agy2api
  sudo rm /etc/systemd/system/agy2api.service /usr/local/bin/agy2api
  sudo systemctl daemon-reload
  sudo rm -rf /etc/agy2api /var/lib/private/agy2api   # 确认不要配置和数据了再删
  ```

## 环境变量

一键安装的写在安装目录的 `.env` 里（Docker 方式也是），手动部署的 Docker 写在 `compose.yaml` 同目录的 `.env` 里，手动装的
systemd 写在 `/etc/agy2api/env` 里（模板是 `deploy/env.example`）。改完重启生效：systemd 是 `systemctl restart agy2api`，
Docker 是 `docker compose up -d`，`run.sh` 重新运行。

| 变量 | 说明 | 默认 |
| --- | --- | --- |
| `AGY2API_ADMIN_PASSWORD` | 管理口令，必填 | — |
| `AGY2API_ADDR` | 监听地址，多个用逗号分隔（如 `127.0.0.1:8249,10.8.0.1:8249`，见"内网访问"） | `:8249` |
| `AGY2API_DATA_DIR` | 数据目录（SQLite 库和凭据加密密钥） | `./data` |
| `AGY2API_ADMIN_ALLOW` | 只允许这些客户端 IP 访问管理端（接口和页面），逗号分隔的 IP 或 CIDR；网关接口不受限 | 不限 |
| `AGY2API_TRUSTED_PROXIES` | 可信的反向代理（IP 或 CIDR）：只有直连的对端在其中时才按 `X-Forwarded-For`、`X-Real-IP` 取客户端 IP；`none` 都不信 | 回环和内网地址 |
| `AGY2API_ENCRYPTION_KEY` | 凭据加密密钥；留空时在数据目录生成 `agy2api.key` | 自动生成 |
| `AGY2API_LOG_LEVEL` | 日志级别：`debug`、`info`、`warn`、`error`（debug 会记每次选号和上游响应） | `info` |
| `AGY2API_UPDATE` | 在线升级：每天和打开管理端时查新版本，管理端可以一键升级（见"在线升级"）；`off` 关闭 | 开启 |
| `AGY2API_PRICE_SYNC` | 每天从官方价格页同步模型价格，用来估算用量的费用；`off` 关闭（只用已同步的价格和手动设的价格） | 开启 |
| `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY` | 全局出口代理：没绑代理的账号和价格同步走它；账号单独绑的代理在管理端"代理"页配置 | 不设 |
| `AGY2API_UPSTREAM_BASES` | 上游基址（逗号分隔，按顺序回退），一般不用改 | 内置 |
| `AGY2API_USER_AGENT` | 上游请求的 User-Agent，一般不用改；留空时按账号自动设置 | 按账号设置 |
| `TZ` | 日志时区 | 系统时区（Docker 镜像里是 UTC） |
| `AGY2API_ALLOW_NO_ADMIN_AUTH` | 跳过管理端鉴权，只用于本机开发 | `false` |
| `AGY2API_UPDATE_API` | 查新版本用的 GitHub API 地址，只用于测试，只能是本机地址 | `https://api.github.com` |
| `AGY2API_EMBED_MOCK`、`AGY2API_MOCK_ADDR` | 启动内置的模拟上游及其监听地址，只用于开发 | `false`、`127.0.0.1:8099` |
