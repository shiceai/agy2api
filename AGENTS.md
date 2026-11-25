# AGENTS.md

## 工作原则

- 先分析，再修改；修改前给出简短计划。
- 优先最小、局部且可验证的改动，不做猜测性预留。
- 遵循现有目录职责和代码风格，避免破坏性变更。
- Go `import` 非重名时不用别名。

## 项目概况

agy2api 把多个 Antigravity 账号组成号池，对外提供 Gemini 原生 `v1beta` 接口，自带管理端。上游是 Antigravity 使用的 Google Cloud Code 内部接口。

技术栈：Go（chi、`modernc.org/sqlite` 纯 Go 无 CGO），管理端 Vue 3 + shadcn-vue（reka-ui，Tailwind 4），构建产物 `go:embed` 进二进制。

部署说明见 `docs/deploy.md`。设计说明和路线图由维护者另行维护，不在本仓库。

## 目录职责

| 目录 | 职责 |
| --- | --- |
| `main.go`（根目录） | 入口：读配置、打开存储、启动 HTTP 服务 |
| `internal/config` | 环境变量配置（前缀 `AGY2API_`） |
| `internal/cryptox` | 凭据加解密（AES-256-GCM） |
| `internal/logx` | slog 初始化，按 ctx 自动附加请求 ID |
| `internal/store` | SQLite 打开、迁移执行、DAO；数据库访问只在这里 |
| `internal/antigravity` | Antigravity 上游：OAuth 刷新与控制面（loadCodeAssist、onboardUser、fetchAvailableModels），数据面（信封构造、生成与计数调用、SSE 解包、上游错误分类）、基址回退、按账号绑定的代理出网 |
| `internal/relay` | 与入站协议无关的执行层：选号、会话粘性、取令牌、上游错误处理决定（冷却、换号、停用）、调度设置、类型化错误、用量记录、后台额度刷新、账号检测 |
| `internal/pricing` | 模型价格：同步并解析官方价格页（Google AI Studio、Anthropic）、手动价、内存价格表、算用量费用、补算 |
| `internal/pool` | 内存号池：启用账号快照、选号（含会话粘性的优先账号、额度避让、只用绑定代理的账号）、账号×模型冷却（落库，额度恢复后提前解除）、代理故障时暂停账号、每账号并发名额 |
| `internal/server` | HTTP：路由、中间件（客户端 IP 只认可信反代、管理端 IP 白名单、管理口令防爆破）、API Key 鉴权缓存（含到期、允许的模型、Token 上限检查）、Gemini 入站、管理接口（含代理管理和测试）、用量记录清理、前端嵌入；账号变更后通知 relay 重新加载号池 |
| `internal/mockupstream` | 开发与测试用的模拟上游 |
| `ddl` | SQLite 迁移脚本，规则见 `ddl/README.md` |
| `web` | 管理端前端，构建输出到 `internal/server/webdist`；`web/src/demo` 是演示站的模拟后端 |
| `docs` | 部署说明 |
| `deploy` | 部署示例：systemd 服务、环境变量模板（Dockerfile、compose.yaml 在仓库根目录） |
| `scripts` | 构建、提交前检查、一键安装（`install.sh`，随 Release 发布）、从 CHANGELOG 取发布说明、更新演示站的目录快照 |
| `.github/workflows` | `ci.yml`：推送到 main 和 PR 时跑 `check.sh` 和前端构建；`release.yml`：推送 `v*` 标签时检查（调用 `ci.yml`）、推送镜像、构建二进制包、创建 Release；`demo.yml`：部署演示站 |
| `.github` | `dependabot.yml`：每周检查 Go、前端、Actions、镜像依赖的更新；`ISSUE_TEMPLATE`：问题反馈和功能建议表单（醒目提醒不要贴凭据）；安全策略在根目录 `SECURITY.md` |

## 开发流程

- Git 提交使用 Conventional Commits，**全部用英文**，例如 `fix(pool): restore accounts after cooldown expires`：标题用祈使句、小写开头、不加句号，正文说明改了什么、为什么。不写 `Co-Authored-By` 等合作者脚注。文档和代码注释仍用简体中文。
- 一个独立功能或修复验证通过后单独提交，不把无关改动放进同一个提交。
- 涉及表结构时先加迁移，规则见 `ddl/README.md`：文件名 `YYYYMMDD_NNN_description.sql`，已提交的迁移不得改名、修改或删除，只能追加。
- 改动影响使用方式（配置、部署、功能）时，在同一次工作中更新 README 和 `docs/deploy.md` 的对应内容。
- README 有两份：`README.md`（简体中文，默认）和 `README_EN.md`（英文），内容保持一致，改一份时另一份一起改。
- 前端构建产物不入库：`internal/server/webdist` 只提交 `.gitkeep`，Vite 构建后会自动补回。
- 完整构建：`bash scripts/build.sh`；交叉编译传 `os/arch`，如 `bash scripts/build.sh linux/amd64`。版本号默认取 `git describe --tags --always --dirty`，注入 `main.version`。
- 发版按语义化版本打附注标签，如 `git tag -a v0.2.0`。1.0 之前用 `v0.x.y`：新功能升 MINOR，只修 bug 升 PATCH；预发布用 `v0.2.0-rc.1`。
- 打标签前先在 `CHANGELOG.md` 加 `## vX.Y.Z - YYYY-MM-DD` 一节：简体中文，写给使用者看，分"功能""修复""升级说明"等小节，每条先用一句加粗的话说清变化，再补一两句细节；`bash scripts/release-notes.sh vX.Y.Z` 可预览发布说明。推送 `v*` 标签后 `.github/workflows/release.yml` 依次做完整检查、推送 ghcr.io 镜像（多架构）、构建 5 个平台的二进制包，并用这一节创建 Release；没有这一节时发布中止。
- 演示站：`cd web && pnpm build:demo`（`--mode demo`，输出 `web/dist-demo`）构建，管理接口由 `web/src/demo/server.ts` 在浏览器里模拟，数据只在内存里；正式构建不含模拟后端。**新增或修改管理接口时同步改模拟后端**，否则演示站对应页面会报"演示站没有模拟这个接口"；模型目录、价格等快照用 `node scripts/demo-catalog.mjs` 从运行中的服务更新。推送正式版标签时 `.github/workflows/demo.yml` 部署到 Cloudflare Pages（要配 `CLOUDFLARE_API_TOKEN`、`CLOUDFLARE_ACCOUNT_ID`）。
- 工作流里的第三方 Action 按提交 SHA 固定（注释写版本），升级时连 SHA 一起换；每个任务只给需要的权限，不引入 `GITHUB_TOKEN` 以外的密钥。

## 验证

- 提交前：`bash scripts/check.sh` 通过（gofmt、go.mod 整洁、go vet、staticcheck、go test、前端 `vue-tsc` 类型检查）。staticcheck 用 `go run` 按脚本里固定的版本运行，不进 go.mod；升级版本时改脚本里的 `STATICCHECK_VERSION`。
- 有 C 编译器时 `check.sh` 用 `-race` 跑测试（数据竞争检测需要 cgo，脚本里显式 `CGO_ENABLED=1`；正式构建仍无 CGO），没有时跑普通测试并提示跳过。Windows 可装 MinGW-w64 的 gcc；PATH 里有 gcc 后 Go 默认会开 cgo，建议 `go env -w CGO_ENABLED=0` 固定成关闭，只让 `-race` 打开。
- 前端改动：另外 `cd web && pnpm build`，确认能构建；界面改动截桌面和手机的图看效果。
- 涉及上游请求形态、号池或错误处理：用真实账号冒烟，至少覆盖非流式和流式各一次。

## 前端约定

- 基础组件用 shadcn-vue 生成到 `web/src/components/ui/<组件>/`，源码归项目所有，可以直接改。加组件：`cd web && npx shadcn-vue@2.9.0 add <组件>`。
- `shadcn-vue add` 每次都会往 `style.css` 加 Google Fonts 远程引入，要删掉（`check.sh` 会拦）：字体用本地打包的 `@fontsource-variable/inter`，管理端要能离线使用。
- vega 风格组件里的 `data-open`、`data-checked`、`data-active`、`data-horizontal` 等简写变体靠 `style.css` 里的 `@custom-variant` 映射到 reka-ui 的 `data-state`、`data-orientation`（摘自 `shadcn-vue/dist/tailwind.css`）。新组件用到没定义的简写变体时在那里补，否则选中、展开等样式会静默失效。
- 字重只用 400 和 600：Windows 的微软雅黑没有 500，`font-medium` 在中文上退成 400，和同一行英文粗细不一致。新生成的组件里的 `font-medium` 要改掉（`check.sh` 会拦）：控件和正文用默认字重，标题用 `font-semibold`，表头靠灰色区分。
- 等宽字体只用于请求 ID、Key 前缀、凭据这类标识，模型名、项目、版本号用普通字体。
- 亮暗模式：浅色、深色、跟随系统（默认），`lib/theme.ts` 的 `useTheme` 和 `ThemeToggle`（桌面在侧边栏底部，手机在顶栏）；改主题走 `setTheme`，新主题从点击处圆形展开（View Transition）。颜色用主题变量（`bg-background`、`text-muted-foreground` 等），不写死浅色值；界面改动的截图浅色、深色各看一遍。
- 界面文案短：名称说"是什么"，说明只说"效果"、最多一行（约 25 字内），和名称意思重复的不写；背景、原理、取值依据不写在页面上，放进 ⓘ（Popover，手机上也能点开）。风险提示（如"会改变模型行为"）不藏，留在对应控件的一行说明里。
- 依赖固定精确版本（不用 `^`）；pnpm 12 会拦截发布不满一天的版本，选版本时避开刚发布的。
- 布局：桌面端用 shadcn-vue `Sidebar`（inset 样式，可收起成图标栏，不放 `SidebarRail`；顶部是标志、名称和下面一行版本号，底部是主题、退出两个图标按钮，收起时竖排；收起按钮在顶栏）；手机端用吸顶顶栏加底部标签栏，不用抽屉式侧边栏。
- 页名只在顶栏出现一次（取自导航），内容区不放大标题；页面主操作（如"导入账号"）用 `PageActions` 放到顶栏右侧，用 `size="sm"` 按钮。有用的说明放到对应位置（卡片说明、按钮旁），复述页名的说明不写。
- 页面结构：所有页面铺满内容区，响应式排版按内容区宽度（`@container/main` 的容器查询，如 `@6xl/main:`），不按视口，侧边栏收起展开都准；设置页用 `SettingCard`（卡片头是标题、最多一句话的说明和分区操作，背景原理放 `info` 插槽、标题旁 ⓘ 点开看）装 `SettingRow`（shadcn Field，名称和一行说明在左、控件在右，放不下时控件折到下一行），卡片放进 `SettingGrid`（够宽时两列瀑布流），大块内容（模板文本框、价格表）的卡片单独占满整行；宽屏表格每条记录一行，主列加 `w-full` 吃掉多余宽度，其他列按内容收紧，操作列表头和按钮居中，手机上用卡片列表；表格正文 13px；分页栏在表格下方（左侧只放条数，右侧每页条数、页码、翻页按钮），每页默认 10 条；可点击筛选的值用 `FilterButton`（悬停出下划线和漏斗图标，不在页面上写操作提示）；互斥筛选用 `SegmentedControl`；行内图标按钮用 `IconAction`（带悬停说明，按危险程度给颜色：普通操作灰色；可恢复的如停用 `tone="warning"` 悬停变橙；不可恢复的如删除 `tone="danger"` 常驻淡红）；状态类标签用 `DotBadge`（`StatusBadge`、`HttpStatus` 基于它）；删除等不可恢复操作用 `useConfirm` 弹确认框；操作结果（同步、启停、删除、批量操作等）用 Toast（`lib/toast.ts`、vue-sonner，桌面右下、手机顶部），标题带上操作对象，失败的说明写可读原因，成功但列表上看不出变化的才提示成功；页面数据加载失败用内容区顶部的 `LoadError`（Alert），不在内容区放红字；批量操作用首列勾选框（表头全选，部分勾选显示横杠）加 `SelectionBar`：有勾选时顶栏进入选择模式（浅灰底，页名和页面操作收起，换成取消选择、已选数量和操作按钮），手机上操作按钮只显示图标。
## 数据与安全

- 真实凭据（refresh token、导出的凭据 JSON）、`data/` 目录、密钥文件不得入库；日志不输出 token 和 API Key 明文，不记 URL 查询串。
- 日志用 `log/slog`，请求链路上用带 ctx 的方法（`slog.InfoContext` 等），请求 ID 会自动带上。
- 所有 Cloud Code（`v1internal`）请求（控制面和数据面）必须带真实 Antigravity User-Agent，否则上游返回 `403 SUBSCRIPTION_REQUIRED`，看起来像账号没订阅。OAuth 令牌刷新按官方客户端实抓用 Go 默认 UA。
- 请求形态（请求头、传输层、信封字段）只照官方客户端实抓改，没有实抓依据的写法不引入。
- 时间存 Unix 毫秒；防重复靠唯一约束加 `ON CONFLICT`，不"先查再插"。
- 热路径不读库：选号、列模型走内存号池，API Key 鉴权走内存缓存；账号或 Key 变更落库后要重新加载对应快照。
- Windows 命令行直接传中文请求体会按本地代码页编码，测试中文内容时用 UTF-8 文件作为请求体。
