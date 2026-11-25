# 部署

agy2api 的数据（SQLite 库和凭据加密密钥）都在数据目录里。目前有两种部署方式：

- **Docker Compose**：仓库根目录的 `Dockerfile` 和 `compose.yaml`，数据在命名卷 `data` 里。
- **systemd**：交叉编译出 Linux 二进制，用 `deploy/agy2api.service` 跑成服务，数据在 `/var/lib/agy2api`。

两种方式默认都只在本机 8080 端口监听，由同一台机器上的反向代理对外提供 HTTPS。

## 上公网前

- 管理口令用足够长的随机串（如 `openssl rand -base64 24`），不要用默认值或短口令。口令少于 12 个字符或只有一类字符时，
  启动日志里会有"管理口令太弱"的警告。
- 只通过 HTTPS 访问：管理口令和 API Key 都放在请求头里，明文 HTTP 会被中途看到。
- 设 `AGY2API_ADMIN_ALLOW`，只让自己的 IP 访问管理端（见下文"客户端 IP 和管理端访问限制"）。
- 备份数据目录里的 `agy2api.db` 和 `agy2api.key`：凭据用这个密钥加密，丢了密钥库里的账号就用不了。

## Docker Compose

用发布的镜像（`ghcr.io/shiceai/agy2api`，linux/amd64、linux/arm64），只需要 `compose.yaml` 和 `.env`：

```bash
curl -fsSLO https://raw.githubusercontent.com/shiceai/agy2api/main/compose.yaml
curl -fsSL https://raw.githubusercontent.com/shiceai/agy2api/main/deploy/env.example -o .env   # 至少填上 AGY2API_ADMIN_PASSWORD
docker compose up -d
docker compose logs -f
```

- 默认用最新的正式版（`latest`）。要固定版本，在 `.env` 里写 `AGY2API_IMAGE=ghcr.io/shiceai/agy2api:0.1.0`。
- 从源码构建：在仓库根目录 `VERSION=$(git describe --tags --always --dirty) docker compose up -d --build`。
  构建出的镜像也叫 `ghcr.io/shiceai/agy2api:latest`，要换回发布的镜像时执行 `docker compose pull`。
- 核验镜像确实由本仓库的发布流程构建：`gh attestation verify oci://ghcr.io/shiceai/agy2api:<版本> -R shiceai/agy2api`。
- 镜像分三段构建：前端（Node + pnpm）→ Go 单二进制（无 CGO）→ Alpine 运行镜像，以非 root 用户（UID 10001）运行。
- 数据在命名卷 `agy2api_data`（项目名取目录名）里。改用宿主机目录挂载时，目录要归 UID 10001 所有：
  `chown 10001:10001 ./data`。
- 镜像带健康检查（请求 `/healthz`）。改了 `AGY2API_ADDR` 的端口时，健康检查也要跟着改。
- 关停时网关先等进行中的请求（最多 10 秒），再写完排队的用量记录（最多 5 秒），`compose.yaml` 已把
  `stop_grace_period` 设成 30 秒。

## systemd

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

## 反向代理

网关有流式响应（SSE），反代要关掉缓冲；非流式请求最长 10 分钟，读超时要比它长；入站请求体上限 16 MiB。

Caddy（自动申请 HTTPS 证书，SSE 会自动逐块转发）：

```
api.example.com {
	reverse_proxy 127.0.0.1:8080
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
        proxy_pass http://127.0.0.1:8080;
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

- 备份：停服务后复制整个数据目录（`agy2api.db` 和 `agy2api.key` 一起），或者用 `sqlite3 agy2api.db ".backup 备份路径"`
  在线备份数据库，密钥文件另外复制一份。Docker 卷可以这样打包：

  ```bash
  docker compose stop
  docker run --rm -v agy2api_data:/data -v "$PWD":/backup alpine tar czf /backup/agy2api-data.tgz -C /data .
  docker compose start
  ```

- 升级：Docker 执行 `docker compose pull && docker compose up -d`（从源码构建的 `git pull` 后加 `--build`）；systemd 换成
  新版本包里的 `agy2api` 后 `systemctl restart agy2api`。各版本的变化见 `CHANGELOG.md`。数据库迁移在启动时自动执行。升级前先备份，要回退时用旧版本加备份的数据。

## 环境变量

Docker Compose 写在 `.env` 里，systemd 写在 `/etc/agy2api/env` 里（模板是 `deploy/env.example`）。

| 变量 | 说明 | 默认 |
| --- | --- | --- |
| `AGY2API_ADMIN_PASSWORD` | 管理口令，必填 | — |
| `AGY2API_ADDR` | 监听地址 | `:8080` |
| `AGY2API_DATA_DIR` | 数据目录（SQLite 库和凭据加密密钥） | `./data` |
| `AGY2API_ADMIN_ALLOW` | 只允许这些客户端 IP 访问管理端（接口和页面），逗号分隔的 IP 或 CIDR；网关接口不受限 | 不限 |
| `AGY2API_TRUSTED_PROXIES` | 可信的反向代理（IP 或 CIDR）：只有直连的对端在其中时才按 `X-Forwarded-For`、`X-Real-IP` 取客户端 IP；`none` 都不信 | 回环和内网地址 |
| `AGY2API_ENCRYPTION_KEY` | 凭据加密密钥；留空时在数据目录生成 `agy2api.key` | 自动生成 |
| `AGY2API_LOG_LEVEL` | 日志级别：`debug`、`info`、`warn`、`error`（debug 会记每次选号和上游响应） | `info` |
| `AGY2API_PRICE_SYNC` | 每天从官方价格页同步模型价格，用来估算用量的费用；`off` 关闭（只用已同步的价格和手动设的价格） | 开启 |
| `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY` | 全局出口代理：没绑代理的账号和价格同步走它；账号单独绑的代理在管理端"代理"页配置 | 不设 |
| `AGY2API_UPSTREAM_BASES` | 上游基址（逗号分隔，按顺序回退），一般不用改 | 内置 |
| `AGY2API_USER_AGENT` | 上游请求的 User-Agent，一般不用改；留空时按账号自动设置 | 按账号设置 |
| `TZ` | 日志时区 | 系统时区（Docker 镜像里是 UTC） |
| `AGY2API_ALLOW_NO_ADMIN_AUTH` | 跳过管理端鉴权，只用于本机开发 | `false` |
| `AGY2API_EMBED_MOCK`、`AGY2API_MOCK_ADDR` | 启动内置的模拟上游及其监听地址，只用于开发 | `false`、`127.0.0.1:8099` |
