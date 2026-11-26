# 部署

AGY2API 的数据（SQLite 库和凭据加密密钥）都在数据目录里。最简单的是用一键安装脚本（见下文），它装好的就是下面两种方式之一：

- **Docker Compose**：仓库根目录的 `Dockerfile` 和 `compose.yaml`，数据在命名卷 `data` 里。
- **systemd**：交叉编译出 Linux 二进制，用 `deploy/agy2api.service` 跑成服务，数据在 `/var/lib/agy2api`。

两种方式默认都只在本机 8249 端口监听（避开 8080 这类常用端口；249 是 AGY 在电话键盘上的位置），由同一台机器上的反向代理对外提供 HTTPS。

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
Linux 有 systemd 时装成服务（就是下文 systemd 一节的做法）：

- 程序在 `/usr/local/bin/agy2api`，配置在 `/etc/agy2api/env`（权限 600），数据在 `/var/lib/agy2api`。
- 首次安装自动生成 24 位随机管理口令写进配置，只在安装结束时显示一次，之后到配置文件里查看。默认只监听
  `127.0.0.1:8249`，对外请放 HTTPS 反向代理；远程服务器上可以先用 `ssh -L 8249:127.0.0.1:8249 <服务器>` 打开管理端。
- 再运行一次就是升级：只换程序和服务文件，配置和数据不动，旧程序备份成 `agy2api.old`，要回退时换回去重启即可。
- 不改系统内核参数。

没有 systemd 的系统（macOS、部分精简发行版）只装程序，并给出运行命令。选项放在 `bash -s --` 后面：

| 选项 | 作用 |
| --- | --- |
| `--version v0.2.0` | 安装指定版本，默认最新正式版 |
| `--listen 0.0.0.0:8249` | 不经反向代理直接对外（只在首次安装、生成配置时生效；会提示明文传输的风险） |
| `--docker` | 不装程序，在当前目录放 `compose.yaml` 和 `.env`（随机口令）后启动容器；再运行一次换成新版本 |
| `--uninstall` | 停止并删除服务和程序，配置和数据保留（会给出删除它们的命令） |

## Docker Compose

用发布的镜像（`ghcr.io/shiceai/agy2api`，linux/amd64、linux/arm64），只需要 `compose.yaml` 和 `.env`：

```bash
curl -fsSLO https://raw.githubusercontent.com/shiceai/agy2api/main/compose.yaml
curl -fsSL https://raw.githubusercontent.com/shiceai/agy2api/main/deploy/env.example -o .env   # 至少填上 AGY2API_ADMIN_PASSWORD
docker compose up -d
docker compose logs -f
```

- 默认用最新的正式版（`latest`）。要固定版本，在 `.env` 里写 `AGY2API_IMAGE=ghcr.io/shiceai/agy2api:1.4.0`。
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

- 升级：管理端可以在线升级（见下一节）；也可以手动升级：一键安装的再运行一次安装脚本（Docker 模式加 `--docker`）；手动部署的 Docker 执行 `docker compose pull && docker compose up -d`（从源码构建的 `git pull` 后加 `--build`）；systemd 换成
  新版本包里的 `agy2api` 后 `systemctl restart agy2api`。各版本的变化见 `CHANGELOG.md`。数据库迁移在启动时自动执行。升级前先备份，要回退时用旧版本加备份的数据。

## 在线升级

点管理端侧边栏的版本号（手机上是设置页底部的版本号），弹窗里显示 GitHub 上最新的正式版和更新内容，可以一键升级。

- 服务每天查一次 GitHub Releases（`api.github.com`），有新版本时侧边栏的版本号变成绿色并带向上箭头，手机顶栏出现升级图标。
  只看正式版，不看预发布版本。
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

## Anthropic 接口

除了 Gemini 原生接口，网关还提供 Anthropic Messages 接口，Claude Code 等客户端可以直接接入（用法见 README）：

- `POST /v1/messages`（流式和非流式）、`POST /v1/messages/count_tokens`；带 `anthropic-version` 请求头的 `GET /v1/models`
  返回 Anthropic 格式的模型列表。API Key 放在 `x-api-key` 或 `Authorization: Bearer` 里。
- 请求转成 Gemini 格式后走同一套号池：选号、冷却、换号、会话粘性、Key 的限额和允许的模型、客户端过滤、系统提示词伪装都一样。
  工具调用、图片和 PDF、思考参数（`thinking`、`output_config.effort`）都会转换；Claude Code 加在 system 里的计费信息块不会发给上游。
- **模型别名**：客户端的模型名不在号池目录里时，先去掉末尾的 `[1m]` 和 `-YYYYMMDD` 日期，还不在就按规则映射（自定义规则在前、
  预置规则在后）。预置规则把 Claude 的官方名映射到号池里的 `claude-opus-5-5`、`claude-sonnet-5-5` 家族，Haiku 映射到
  `claude-sonnet-5-5-low`。在管理端"设置 → 模型别名"里可以关掉预置规则、加自定义规则（每行 `别名 => 目标`，可用 `*` 通配），
  并试解析一个模型名。所有接口都生效。
- 不支持：Anthropic 的服务端工具（如 `web_search`，请求里有时去掉，其它工具照常）、文件 API（`file_id`）、批处理 API。
  Antigravity 上的 Claude 不返回思考内容。
- **客户端兼容**（默认开，作用于 Anthropic 和 OpenAI Responses 入站）：上游按系统提示里的客户端特征（Claude Code、Codex 等）拦截，返回
  伪装成额度耗尽的 429（实测与模型、入站协议无关，与措辞无关，删改单段也躲不掉；但上游只检查系统提示、不看对话内容）。
  开启后把客户端 system 整体降为开头的一条用户消息、系统提示留空即可放行，代价是客户端 system 从 system 级降到 user 级、
  可能略微改变模型遵循方式。在"设置 → 模型别名 → 客户端兼容"里开关。用别的中间服务以 Gemini 协议转发 Claude Code
  的请求时不走这个开关（它只认 Anthropic、Responses 入站），可开"系统提示词伪装"的"客户端转用户"模式达到同样效果，模板留空即可（只降级、不注入）。
- 用量页的"接口"一列区分 Gemini、Anthropic、OpenAI（Chat Completions）和 OpenAI Responses 的请求，请求模型记客户端原来的名字，"实际发出"是映射后的模型。

## OpenAI 接口

网关还提供 OpenAI Chat Completions 和 Responses 接口，OpenAI SDK、Cherry Studio、Codex 等客户端把 Base URL 设成
`http://<地址>/v1` 即可接入（用法见 README）：

- `POST /v1/chat/completions`（流式和非流式）；`GET /v1/models`、`GET /v1/models/{id}` 返回 OpenAI 格式。API Key 放在
  `Authorization: Bearer` 里。
- `/v1/models` 三种接口共用，按请求头区分格式：带 `anthropic-version` 的是 Anthropic 格式，带 `x-goog-api-key` 或 `?key=`
  （Gemini SDK 都带）的是 Gemini 格式，其余是 OpenAI 格式。`/v1beta/models` 始终是 Gemini 格式。
- 和 Anthropic 接口一样走同一套号池和设置（模型别名、客户端过滤、系统提示词伪装、请求处理）；"客户端兼容"开关作用于
  Responses 接口，不作用于 Chat Completions。开头的 `system`、`developer` 消息进系统提示，对话中间的转成用户消息里的 `<system-reminder>`。
- 转换：工具调用和 `tool_choice`；图片（data URL，http 地址要打开远程图片内联）、音频（`input_audio`）、文件（`file_data`）；
  `max_completion_tokens`（或 `max_tokens`）、`temperature`、`top_p`、`stop`；`reasoning_effort`（none、minimal 按 low）。
  `prompt_cache_key` 用来认对话（见"会话粘性"）。其它参数（如 `presence_penalty`、`seed`、`logprobs`）忽略。
- 思考内容在 `reasoning_content` 字段返回（非标准字段，DeepSeek 带起来的写法）。OpenAI 格式没有放思考签名的位置，下一轮
  历史里的工具调用由网关补占位签名，多轮工具调用照常可用。用量按 OpenAI 口径：`prompt_tokens` 含命中缓存，
  `completion_tokens` 含思考。
- 结构化输出：`response_format` 的 `json_schema` 转成上游认的 `responseSchema`（内联 `$ref`、去掉不支持的关键字），
  对 Gemini 模型生效；Claude 模型上游报 400。`json_object` 上游不强制，靠提示词。
- 不支持：`n` 大于 1、用 Files API 上传的文件（`file_id`）。

### Responses 接口

- `POST /v1/responses`（流式和非流式），Codex 等客户端用（配置见 README）。响应对象、流式事件按 OpenAI 的格式，错误格式同上。
- 不存响应：每轮要带上完整对话（Codex 就是这样，`store: false`）；`previous_response_id`、`conversation`、`background`
  返回 400，也没有 `GET`、`DELETE /v1/responses/{id}`。
- 转换：`instructions` 和开头的 `system`、`developer` 消息进系统提示（开"客户端兼容"时一起降为用户消息），对话中间的转成
  `<system-reminder>`；`input` 里的消息（文字、图片、文件）、工具调用和结果、reasoning 条目；函数工具、命名空间工具（展开成
  "命名空间__函数名"，回给客户端时拆回来）和自定义工具（Codex 的 `apply_patch` 等，转成只有一个字符串参数 `input` 的函数，
  回给客户端时还原成 `custom_tool_call`）；`max_output_tokens`、`temperature`、`top_p`、`tool_choice`、`reasoning.effort`
  （同 `reasoning_effort`）、`text.format`（同 `response_format`）。内置工具（`web_search`、`file_search` 等）上游没有对应，去掉。
- 思考放在 reasoning 条目的 `summary` 里。`include` 里有 `reasoning.encrypted_content` 时思考签名放进 `encrypted_content`：
  Claude 的签名跟着思考；Gemini 的签名挂在工具调用上，网关在工具调用前插一条摘要为空、只带签名的 reasoning 条目。客户端下一轮
  原样传回，网关再挂回原处；不带签名的 reasoning 条目不发给上游。
- 用量：`input_tokens` 含命中缓存，`output_tokens` 含思考。输出到上限时以 `response.incomplete` 结束，上游中途出错时是
  `response.failed`。

## 会话粘性

同一对话优先用上次的账号：上游的缓存按账号算，换号就命中不了、多花额度。在"设置 → 账号调度 → 会话粘性"里开关（默认开）
和设置闲置多久后失效。所有接口都生效，对话按"API Key + 模型 + 会话标识"认，会话标识按下面的顺序取：

1. `X-Claude-Code-Session-Id` 请求头：Claude Code 每个请求都带。子代理还带 `X-Claude-Code-Agent-Id`，单独认对话，
   不会影响主会话用的账号。
2. `X-Session-Id` 请求头：通用写法。多人共用一个 Key，或前面还有一层网关时，按人或按会话设置这个头，
   首条消息相同的不同对话就不会被认成一个。
3. OpenAI 接口请求体里的 `prompt_cache_key`（Codex 填的是会话 ID）。
4. 都没有时用第一条用户消息（客户端的 system 被转成用户消息时跳过那一条）。

用量详情的"会话"一行显示沿用账号、换了账号还是新对话，以及按什么认的对话。

## 环境变量

Docker Compose 写在 `.env` 里，systemd 写在 `/etc/agy2api/env` 里（模板是 `deploy/env.example`）。

| 变量 | 说明 | 默认 |
| --- | --- | --- |
| `AGY2API_ADMIN_PASSWORD` | 管理口令，必填 | — |
| `AGY2API_ADDR` | 监听地址 | `:8249` |
| `AGY2API_DATA_DIR` | 数据目录（SQLite 库和凭据加密密钥） | `./data` |
| `AGY2API_ADMIN_ALLOW` | 只允许这些客户端 IP 访问管理端（接口和页面），逗号分隔的 IP 或 CIDR；网关接口不受限 | 不限 |
| `AGY2API_TRUSTED_PROXIES` | 可信的反向代理（IP 或 CIDR）：只有直连的对端在其中时才按 `X-Forwarded-For`、`X-Real-IP` 取客户端 IP；`none` 都不信 | 回环和内网地址 |
| `AGY2API_ENCRYPTION_KEY` | 凭据加密密钥；留空时在数据目录生成 `agy2api.key` | 自动生成 |
| `AGY2API_LOG_LEVEL` | 日志级别：`debug`、`info`、`warn`、`error`（debug 会记每次选号和上游响应） | `info` |
| `AGY2API_UPDATE` | 在线升级：每天查一次新版本，管理端可以一键升级（见"在线升级"）；`off` 关闭 | 开启 |
| `AGY2API_PRICE_SYNC` | 每天从官方价格页同步模型价格，用来估算用量的费用；`off` 关闭（只用已同步的价格和手动设的价格） | 开启 |
| `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY` | 全局出口代理：没绑代理的账号和价格同步走它；账号单独绑的代理在管理端"代理"页配置 | 不设 |
| `AGY2API_UPSTREAM_BASES` | 上游基址（逗号分隔，按顺序回退），一般不用改 | 内置 |
| `AGY2API_USER_AGENT` | 上游请求的 User-Agent，一般不用改；留空时按账号自动设置 | 按账号设置 |
| `TZ` | 日志时区 | 系统时区（Docker 镜像里是 UTC） |
| `AGY2API_ALLOW_NO_ADMIN_AUTH` | 跳过管理端鉴权，只用于本机开发 | `false` |
| `AGY2API_UPDATE_API` | 查新版本用的 GitHub API 地址，只用于测试，只能是本机地址 | `https://api.github.com` |
| `AGY2API_EMBED_MOCK`、`AGY2API_MOCK_ADDR` | 启动内置的模拟上游及其监听地址，只用于开发 | `false`、`127.0.0.1:8099` |
