# 更新日志

每个版本的说明也会作为 GitHub Release 的正文发布。版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。

## 未发布

### 功能

- **Anthropic Messages 接口。** 提供 `/v1/messages`（流式和非流式）和 `/v1/messages/count_tokens`，带 `anthropic-version` 头的 `/v1/models` 返回 Anthropic 格式。Claude Code 设好 `ANTHROPIC_BASE_URL` 和 `ANTHROPIC_AUTH_TOKEN` 就能用。工具调用、图片和文档、思考参数都会转换，Gemini 模型的思考签名能在多轮对话里来回传递；选号、换号、用量记录等和 Gemini 接口一样。
- **模型别名。** 客户端发的模型名不在号池里时，自动去掉 `[1m]` 和日期后缀，再按规则映射：预置规则把 Claude 的官方名映射到号池里的 Claude 家族，Haiku 映射到 `claude-sonnet-5-5-low`。可以在"设置 → 模型别名"里加自定义规则并试解析，两个接口都生效。
- **用量区分接口。** 用量页新增"接口"列和筛选，详情里写明调用的接口；请求模型记客户端原来的名字。
- **Claude Code 兼容（默认开）。** 上游会按系统提示里的客户端特征拦截 Claude Code 等，返回伪装成额度耗尽的 429。网关默认把 Anthropic 入站客户端的 system 降为开头的一条用户消息（上游只检查系统提示）即可放行，可在"设置 → 模型别名"里关掉。

## v1.0.0 - 2026-11-25

首个公开版本。

### 功能

- **Gemini 原生接口。** 提供 `generateContent`、`streamGenerateContent`、`countTokens` 和模型列表，`/v1` 是 `/v1beta` 的别名，可以直接用 `google-genai` 等 SDK。模型名可以写目录里的精确 ID，也可以写家族名，按思考参数自动选档位。
- **号池调度。** 冷却按"账号 × 模型"计算：429 按上游给的重置时间，5xx 短暂休息。请求失败自动换号，流式在第一帧之前出错也会换号；选不到账号时短暂等待冷却恢复。剩余额度低的账号会被避让，同一对话优先使用上次的账号，每个账号有并发上限。
- **账号管理。** 管理端用 Google 登录添加账号，服务部署在远端也能用；也可以批量导入 refresh token。令牌提前刷新，失效后自动停用账号，单个账号可以手动检测。
- **出口代理。** 每个账号绑定固定的出口代理（http、https、socks5），代理故障时暂停该账号，不回落直连。可以检测代理的出口 IP、地区和延迟。
- **API Key。** 支持到期时间、Token 上限（每天、每月、总量）和允许的模型。
- **用量和费用。** 记录每次调用的耗时、Token 和失败原因，按官方 API 价格估算费用（只显示，不扣费），显示实时并发，按保留期自动清理旧记录。
- **安全。** 管理口令防爆破：同一地址错误口令太多时暂时锁定。支持管理端 IP 白名单，客户端 IP 只认可信反向代理转发的地址，凭据加密存储。
- **管理端。** 桌面和手机都能用，支持浅色和深色。
- **在线升级。** 有新版本时侧边栏的版本号变成绿色，点开一键升级到新的正式版：下载后按 SHA256SUMS.txt 校验，先备份数据库，服务原地重启换成新程序。新程序放在数据目录里，Docker 容器重建、systemd 重启后都还是新版本；连续起不来时自动退回原来的版本，也可以手动撤销。`AGY2API_UPDATE=off` 关闭。

### 部署

- **一键安装**：`curl -fsSL https://github.com/shiceai/agy2api/releases/latest/download/install.sh | sudo bash`。Linux 上装成 systemd 服务，首次安装自动生成管理口令，默认只在本机 8249 端口监听；再运行一次就是升级，加 `--docker` 用 Docker 部署，`--uninstall` 卸载。
- **二进制包**：从本页下载对应平台的压缩包，用随附的 `SHA256SUMS.txt` 核对；也可以运行 `gh attestation verify <压缩包> -R shiceai/agy2api`，核验压缩包确实由本仓库的发布流程构建。
- **容器镜像**：`ghcr.io/shiceai/agy2api:1.0.0`，支持 linux/amd64 和 linux/arm64。
- 部署步骤见包里的 `deploy.md`。使用前请先阅读 README 里的风险提示。
