// 极简 API 客户端：带管理口令，统一错误处理。
const PW_KEY = 'agy2api_admin_pw'

export function getAdminPassword(): string {
  return localStorage.getItem(PW_KEY) ?? ''
}
export function setAdminPassword(pw: string) {
  localStorage.setItem(PW_KEY, pw)
}
export function clearAdminPassword() {
  localStorage.removeItem(PW_KEY)
}

export class ApiError extends Error {
  status: number
  // 部分接口在错误里带机器可读的原因，如同步时 refresh token 已失效为 invalid_grant。
  code?: string
  constructor(status: number, message: string, code?: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

// 口令失效（401）统一在这里处理：清掉保存的口令，交给 main.ts 注册的处理函数跳到登录页（带上当前地址，登录后回来）。
// 页面里不用各自判断 401：toastError 会跳过它，加载失败的说明用 errorMessage（401 时为空串），不会在跳走前闪一下报错；
// 循环里的批量操作遇到 401 要停下时用 isUnauthorized。
let unauthorizedHandler: (() => void) | null = null
export function onUnauthorized(fn: () => void) {
  unauthorizedHandler = fn
}

export function isUnauthorized(e: unknown): boolean {
  return e instanceof ApiError && e.status === 401
}

// errorMessage 是给页面显示的错误说明；口令失效时为空串（已经在跳登录页）。
export function errorMessage(e: unknown): string {
  return isUnauthorized(e) ? '' : (e as Error).message
}

// waitText 把 Retry-After（秒）换成"N 分钟"或"N 小时"，向上取整。
function waitText(retryAfter: string | null): string {
  const minutes = Math.max(1, Math.ceil(Number(retryAfter) / 60) || 1)
  return minutes < 60 ? `${minutes} 分钟` : `${Math.ceil(minutes / 60)} 小时`
}

// isDemo：演示站构建（vite build --mode demo）。正式构建里它是常量 false，演示相关的分支和模块都不会打包进去。
export const isDemo = import.meta.env.MODE === 'demo'

// password 不传时用保存的口令；传了（登录页校验口令）时口令不对只抛 401，不清口令、不跳转。
async function request<T>(method: string, path: string, body?: unknown, password?: string): Promise<T> {
  const pw = password ?? getAdminPassword()
  let status: number
  let data: unknown = undefined
  let retryAfter: string | null = null
  if (isDemo) {
    // 演示站没有服务器：请求交给浏览器里的模拟后端，返回的状态码和数据照常走下面的处理。
    const { demoRequest } = await import('@/demo/server')
    ;({ status, data } = await demoRequest(method, path, body, pw))
  } else {
    const headers: Record<string, string> = {}
    if (pw) headers['X-Admin-Password'] = pw
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    const res = await fetch(path, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
    status = res.status
    retryAfter = res.headers.get('Retry-After')
    const text = await res.text()
    if (text) {
      try {
        data = JSON.parse(text)
      } catch {
        data = { error: text }
      }
    }
  }
  if (status < 200 || status >= 300) {
    if (status === 401 && password === undefined) {
      clearAdminPassword()
      unauthorizedHandler?.()
    }
    const d = data as { error?: string; code?: string } | undefined
    // 错误口令太多，这个地址的管理端被暂时锁定（429）：登录页和各页面的报错都显示中文说明和大约要等多久。
    if (status === 429 && d?.code === 'admin_locked') {
      throw new ApiError(429, `口令错误太多，已暂时锁定，约 ${waitText(retryAfter)}后再试`, d.code)
    }
    throw new ApiError(status, d?.error ?? `HTTP ${status}`, d?.code)
  }
  return data as T
}

// Session 是版本信息：version 是构建注入的版本号（正式版如 v0.1.0，开发构建如 v0.1.0-3-gabc1234-dirty，
// 本地 go run 为 dev）。
export interface Session {
  version: string
  // update 是查到的更新的版本（侧边栏提示），没有时不出现。
  update?: string
}

// --- 模型别名（设置页"模型别名"） ---
export interface ModelAliasSettings {
  use_presets: boolean
  // rules 是自定义规则：每行一条 别名 => 目标，别名可以用 * 通配，# 开头是注释。
  rules: string
}
export interface AliasRule {
  pattern: string
  target: string
}
export interface ModelAliasResponse {
  settings: ModelAliasSettings
  presets: AliasRule[]
}
// AliasTestResult 是试解析的结果：target 是交给号池的名字，rule 是命中的规则（没走规则时为 null），
// resolved 是按家族和档位最终用的模型（号池里没有时不出现）。
export interface AliasTestResult {
  model: string
  target: string
  rule: AliasRule | null
  resolved?: string
}

// AnthropicCompat 是客户端兼容设置（作用于 Anthropic、OpenAI Responses 入站）：system_as_user 为真时把客户端 system 降为开头的用户消息。
export interface AnthropicCompat {
  system_as_user: boolean
}
export interface AnthropicCompatResponse {
  settings: AnthropicCompat
}

// --- 版本与在线升级（版本弹窗） ---
export interface ReleaseInfo {
  version: string
  notes: string
  url?: string
  published_at?: number
}
// 升级进度：state 为空表示没有在升级。
export type UpgradeState = 'downloading' | 'verifying' | 'backing_up' | 'restarting' | 'failed'
export interface UpgradeProgress {
  state?: UpgradeState
  target?: string
  received?: number
  total?: number
  error?: string
}
export interface SystemView {
  // version 是运行中的版本；运行的是在线升级的程序时，base_version 是镜像、安装包自带的版本，installed_at 是在线升级的时间。
  version: string
  base_version?: string
  installed_at?: number
  deployment: 'docker' | 'systemd' | 'binary'
  can_check: boolean
  can_upgrade: boolean
  unsupported_reason?: string
  latest?: ReleaseInfo
  update_available: boolean
  checked_at?: number
  check_error?: string
  // failed 是停用的在线升级程序（连续起不来、文件和记录不符）。
  failed?: { version: string; reason: string }
  progress: UpgradeProgress
  // new_migrations 是在线升级后新版本执行过的迁移数；unknown_migrations 是当前程序不认识的迁移（库被更新的版本改过）。
  new_migrations?: number
  unknown_migrations?: string[]
}

// fetchSession 读版本信息；传 password 时用它校验口令（登录页），口令不对抛 401，不触发跳转。
export function fetchSession(password?: string): Promise<Session> {
  return request<Session>('GET', '/api/admin/session', undefined, password)
}

export const api = {
  get: <T>(p: string) => request<T>('GET', p),
  post: <T>(p: string, body?: unknown) => request<T>('POST', p, body),
  put: <T>(p: string, body?: unknown) => request<T>('PUT', p, body),
  del: <T>(p: string) => request<T>('DELETE', p),
}

// --- 类型 ---
export interface ModelInfo {
  id: string
  display_name: string
  supports_thinking: boolean
  remaining_fraction?: number
  reset_time?: string
  // listed：在对外的模型列表里（官方客户端的模型选择器或辅助用途）；family、family_display_name：官方分组的家族。
  listed?: boolean
  family?: string
  family_display_name?: string
}
// QuotaSummary 是开了每周额度的账号拉到的分组额度：每组共享额度的模型（如 Gemini、Claude 和 GPT），
// 组里每个窗口（window 是 5h、weekly 等）一个桶。remaining_fraction 缺省表示上游没给（多半是用完了）。
export interface QuotaSummary {
  groups: QuotaSummaryGroup[]
}
export interface QuotaSummaryGroup {
  display_name?: string
  description?: string
  buckets: QuotaBucket[]
}
export interface QuotaBucket {
  bucket_id?: string
  window?: string
  display_name?: string
  description?: string
  remaining_fraction?: number
  reset_time?: string
}
export interface Account {
  id: number
  email: string
  name: string
  project_id: string
  user_project_id: string
  user_agent: string
  status: string
  disabled_reason?: string
  // 单独设置的并发上限，0 表示跟随全局默认值（列表接口的 default_concurrency）。
  max_concurrency: number
  // 取视图时该账号进行中的生成请求数（实时并发占用）；要实时刷新用 /api/admin/live（见 lib/live）。
  in_flight: number
  cooldowns: Cooldown[]
  model_count: number
  models: ModelInfo[]
  // 模型目录（含额度）上次刷新的 Unix 毫秒。
  models_refreshed_at: number
  // proxy 是绑定的代理，直连为 null；proxy_paused_until 是代理连不上后暂停调度的截止（Unix 毫秒），0 为没暂停。
  proxy: AccountProxy | null
  proxy_paused_until: number
  // 订阅档位，还没检查过的账号为 null。
  subscription: Subscription | null
  synced_at: number
  created_at: number
  // weekly_quota 是账号自己的每周额度开关（'' 跟随全局，'on'、'off'），weekly_quota_on 是实际是否拉取；
  // quota_summary 是上次拉到的分组额度（没拉过为 null），quota_summary_at 是拉到的 Unix 毫秒。
  weekly_quota: '' | 'on' | 'off'
  weekly_quota_on: boolean
  quota_summary: QuotaSummary | null
  quota_summary_at: number
}
// Subscription 是 loadCodeAssist 返回的订阅档位。downgraded 表示付费档位降到了 Antigravity Starter Quota。
export interface Subscription {
  current_tier?: string
  current_tier_id?: string
  paid_tier?: string
  paid_tier_id?: string
  ineligible?: string[]
  // 付费档位上的 AI 积分，上游没给时缺省。
  credits?: number
  downgraded: boolean
  checked_at: number
}
// Cooldown 是账号某个模型生效中的冷却；until 为 Unix 毫秒，reason 是分类（rate_limit、quota_exhausted 等）。
export interface Cooldown {
  model: string
  until: number
  reason: string
}
export interface AccountList {
  accounts: Account[]
  default_concurrency: number
  // 避让阈值（百分比）：剩余额度低于它的模型选号时先避开。
  low_quota: number
  // 后台刷新额度的间隔（秒，0 为不刷新），用来提示额度可能已过期；weekly_quota 是每周额度的全局开关。
  quota_refresh: number
  weekly_quota: boolean
}
// ImportResult 是导入或 Google 登录添加账号的结果：created 为 false 表示邮箱已存在、换上了新凭据；
// reenabled 表示账号之前因 refresh token 失效被停用，这次重新启用了。
export interface ImportResult {
  account: Account
  created: boolean
  reenabled: boolean
  warning?: string
}
// AccountStats 是账号详情的统计，按每次上游尝试算：各模型的尝试次数、成功次数、成功尝试的平均耗时，
// 以及最近的失败尝试（usage_id 是所在的用量记录，可打开调用详情）。
export interface ModelAttemptStat {
  model: string
  attempts: number
  success: number
  avg_ms: number
}
export interface AttemptError {
  usage_id: number
  created_at: number
  model: string
  status: number
  result: string
  error: string
  cooldown_ms: number
}
export interface AccountStats {
  models: ModelAttemptStat[]
  recent_errors: AttemptError[]
  // cost_nanos 是最终由这个账号完成的请求的费用合计（十亿分之一美元），一条都没算过时为 null。
  cost_nanos: number | null
}
// ProbeResult 是一次账号检测的结果：kind 是失败分类，cooldown_ms、disabled 是这次检测引起的冷却和停用原因。
export interface ProbeResult {
  ok: boolean
  model: string
  status?: number
  kind?: string
  message?: string
  reply?: string
  latency_ms: number
  cooldown_ms?: number
  disabled?: string
}
// OAuthStart 是一次 Google 登录的授权链接，expires_at 是过期时间（Unix 毫秒）。
export interface OAuthStart {
  session_id: string
  auth_url: string
  expires_at: number
}
// TokenPeriod 是 Token 上限的周期，按服务器时区划分。
export type TokenPeriod = 'day' | 'month' | 'total'
// ApiKeySettings 是 Key 可修改的名称和限制：expires_at 为 0 表示永不过期，token_limit 为 0 表示不限，
// allowed_models 为空表示不限制（支持 * 通配）。
export interface ApiKeySettings {
  name: string
  expires_at: number
  token_limit: number
  token_period: TokenPeriod | ''
  allowed_models: string[]
}
// ApiKey 是管理端的 Key：period_used 是本周期已用 Token（设了上限时），period_reset_at 是下次重置时间
// （每天、每月；总量为 0），recent 是近 30 天的请求数和 Token 数。
export interface ApiKey extends ApiKeySettings {
  id: number
  prefix: string
  status: string
  created_at: number
  last_used_at: number
  expired: boolean
  period_used: number
  period_reset_at: number
  recent: { requests: number; tokens: number; cost_nanos: number | null }
}
// KeyModelStat 是 Key 在某个请求模型上的用量（按最终请求）。
export interface KeyModelStat {
  model: string
  requests: number
  success: number
  tokens: number
  avg_ms: number
  cost_nanos: number | null
}
// 统计里的 cost_nanos 是算过费用的记录的费用合计（十亿分之一美元），一条都没算过时为 null。
export interface ModelStat {
  model: string
  requests: number
  prompt_tokens: number
  output_tokens: number
  total_tokens: number
  cost_nanos: number | null
}
// UsageRecord 是一次请求的用量记录；null 表示没有这项数据（老记录，或非流式没有首字延迟）。
export interface UsageRecord {
  id: number
  request_id: string
  api_key_id: number
  account_id: number
  request_model: string
  resolved_model: string
  upstream_model: string
  // protocol 是入站接口：gemini（Gemini 原生接口）、anthropic（Anthropic Messages 接口）、openai（OpenAI Chat Completions 接口）
  // 或 openai_responses（OpenAI Responses 接口）。
  protocol: 'gemini' | 'anthropic' | 'openai' | 'openai_responses'
  reasoning: string
  prompt_tokens: number
  output_tokens: number
  cached_tokens: number | null
  thoughts_tokens: number | null
  total_tokens: number
  status_code: number
  stream: boolean
  first_token_ms: number | null
  latency_ms: number
  created_at: number
  client_ip: string
  user_agent: string
  // 老记录没有详情。
  detail?: UsageDetail
  // cost_nanos 是费用（十亿分之一美元）；null 表示没有算：没有 token，或还没有这个模型的价格。
  cost_nanos: number | null
}
// UsageTimings 是最终那次尝试的耗时明细（毫秒）：cooldown_wait_ms（整个请求累计等冷却）、acquire_ms、token_ms、upstream_ms
// 是时长，其余是从收到请求起算的时间点。
export interface UsageTimings {
  cooldown_wait_ms?: number
  acquire_ms?: number
  token_ms?: number
  header_ms?: number
  first_event_ms?: number
  first_text_ms?: number
  upstream_ms?: number
}
export interface UsageAttempt {
  try: number
  account_id: number
  account: string
  model: string
  status?: number
  result: string // success、上游错误分类，或 transport
  error?: string
  cooldown_ms?: number
  duration_ms: number
}
export interface UsageDetail {
  timings?: UsageTimings
  slot?: { in_use: number; limit: number }
  attempts?: UsageAttempt[]
  error?: { kind: string; reason?: string; message?: string }
  response_id?: string
  trace_id?: string
  // fake_stream 表示客户端请求的是假流式：上游用非流式请求（stream 为 false），结果按流式返回。
  fake_stream?: boolean
  cost?: UsageCost
  affinity?: UsageAffinity
}
// UsageAffinity 是会话粘性的结果：hit 沿用绑定的账号，diverted 原账号暂时接不了、临时借了别的账号（绑定不变），
// switched 原账号恢复不了、换号并改绑（reason 是原账号这次不能接的原因，from_account 是原账号），new 新对话。source 是按什么认的对话（Claude Code 会话头、X-Session-Id、prompt_cache_key、
// 首条消息），老记录没有。
export interface UsageAffinity {
  result: 'hit' | 'diverted' | 'switched' | 'new'
  reason?: string
  from_account_id?: number
  from_account?: string
  source?: 'claude_code' | 'claude_code_agent' | 'header' | 'prompt_cache_key' | 'first_message'
}
// UsageCost 是一条用量的费用和算法：nanos 是费用（十亿分之一美元）；input、cache_read、output 是用的单价
// （百万分之一美元每百万 token），model、source 是价格对应的型号和来源；tier、promo 表示用了长提示分档价、限时价；
// backfilled 表示记录时还没有价格，之后补算的。
export interface UsageCost {
  nanos: number
  model: string
  source: PriceSource
  input: number
  cache_read: number
  output: number
  tier?: boolean
  promo?: boolean
  backfilled?: boolean
}
// UsageLog 是带账号邮箱和 API Key 名称的用量记录；账号或 Key 已删除时为空串。
export interface UsageLog extends UsageRecord {
  account_email: string
  api_key_name: string
  api_key_prefix: string
}
// HourlyUsage 是一个小时的用量；start 是这一小时的起点（UTC 整点，Unix 毫秒），errors 是非 2xx 请求数。
export interface HourlyUsage {
  start: number
  requests: number
  errors: number
  prompt_tokens: number
  output_tokens: number
  total_tokens: number
}
// Overview 的"近 24 小时"是当前小时加前 23 个小时；hourly 固定 24 项，按时间升序。
export interface Overview {
  accounts_total: number
  accounts_ready: number
  api_keys_total: number
  api_keys_enabled: number
  requests_24h: number
  // 取概览时进行中的生成请求总数（实时并发），要实时刷新用 /api/admin/live（见 lib/live）。
  in_flight: number
  usage_by_model: ModelStat[]
  hourly: HourlyUsage[]
}
// Live 是 /api/admin/live 的实时并发：in_flight 是账号 ID（字符串键）→ 进行中请求数，total 是总数，
// now 是服务器时间（Unix 毫秒）。
export interface Live {
  in_flight: Record<string, number>
  total: number
  now: number
}
export interface Settings {
  default_tier: string
  model_tiers: Record<string, string>
}
export interface TierFamily {
  family: string
  tiers: string[]
}
export interface SettingsResponse {
  settings: Settings
  tiers: string[]
  families: TierFamily[]
}
// Scheduling 是调度与容错设置，时长都以秒为单位；quota_refresh 为 0 表示关闭后台刷新，low_quota 是百分比，stream_idle（流式空闲超时）为 0 表示不断开。
export interface Scheduling {
  token_refresh_early: boolean
  token_refresh_min_lead: number
  token_refresh_max_lead: number
  concurrency: number
  slot_wait: number
  max_attempts: number
  cooldown_wait: number
  rate_limit_base: number
  rate_limit_max: number
  server_error_cooldown: number
  validation_cooldown: number
  model_forbidden_cooldown: number
  quota_refresh: number
  low_quota: number
  stream_idle: number
  require_proxy: boolean
  // sticky_mode 是会话粘性的方式：off 关闭、preferred 优先、strict 严格；sticky 是旧的开关（不是关闭时为 true），
  // 服务端照它写，撤销升级后的旧版本照样能读。sticky_ttl 是对话闲置多久后不再粘（秒）。
  sticky: boolean
  sticky_mode: StickyMode
  sticky_ttl: number
  // weekly_quota 是每周额度的全局开关：开启后后台刷新额度时顺带拉每周额度，账号可单独开关。
  weekly_quota: boolean
}
export type StickyMode = 'off' | 'preferred' | 'strict'
export interface SchedulingResponse {
  settings: Scheduling
  defaults: Scheduling
}
// ClientFilter 是客户端名称过滤设置：mode 为 rewrite（替换）或 block（拦截），
// use_default_keywords 为真时在自定义规则外加上预置名称，rules 是自定义规则文本。
export interface ClientFilter {
  enabled: boolean
  mode: 'rewrite' | 'block'
  use_default_keywords: boolean
  rules: string
}
export interface ClientFilterResponse {
  settings: ClientFilter
  // default_keywords 是预置的客户端名称清单，供界面展示。
  default_keywords: string[]
}
// SystemPrompt 是系统提示词伪装设置：mode 为 prepend（前置）、replace（替换）或 as_user（客户端转用户），
// template 是注入的 system 提示词。
export interface SystemPrompt {
  enabled: boolean
  mode: 'prepend' | 'replace' | 'as_user'
  template: string
}
export interface SystemPromptResponse {
  settings: SystemPrompt
}
// RequestHandling 是请求处理开关：是否内联远程图片 URL、是否把思考内容返回给客户端。
export interface RequestHandling {
  remote_images_enabled: boolean
  return_thoughts: boolean
}
export interface RequestHandlingResponse {
  settings: RequestHandling
}

// --- 价格 ---

export type PriceSource = 'official' | 'manual'
// PriceRates 是一组单价，百万分之一美元每百万 token；cache_read 缺省时缓存命中按输入价算。
export interface PriceRates {
  input: number
  output: number
  cache_read?: number
}
// Price 是一个模型的价格：tier 是提示超过 threshold 个 token 时的价格，promo 是 end（Unix 毫秒）之前的限时价。
export interface Price extends PriceRates {
  tier?: PriceRates & { threshold: number }
  promo?: PriceRates & { end: number }
}
export interface PriceEntry {
  model: string
  source: PriceSource
  vendor?: string
  name?: string
  price: Price
  updated_at: number
}
// PriceRow 是价格页的一行：号池里的一个模型家族（ids 是各档位的模型 ID），或只剩手动价的模型。
export interface PriceRow {
  model: string
  ids: string[]
  price: PriceEntry | null
  official: PriceEntry | null
}
export interface PriceSourceStatus {
  vendor: string
  url: string
  ok: boolean
  error?: string
  prices: number
  skipped?: { model: string; reason: string }[]
  success_at: number
}
export interface PricesView {
  status: { attempt_at: number; sources?: PriceSourceStatus[] | null }
  auto_sync: boolean
  models: PriceRow[]
}

// --- 代理 ---

export type ProxyScheme = 'http' | 'https' | 'socks5' | 'socks5h'
// AccountProxy 是账号绑定的代理（不含地址和密码）。
export interface AccountProxy {
  id: number
  name: string
  egress_ip: string
  egress_country: string
  check_error?: string
  // status 为 disabled 时这个账号暂停调度。
  status: 'enabled' | 'disabled'
}
// Proxy 是管理端看到的代理，不含密码。egress_* 是最近一次测试拿到的出口（国家为两位代码，地区、城市、运营商
// 来自 ipinfo，退回 Cloudflare 时为空）；latency_ms 是经代理请求 Google 的往返耗时，0 表示没测出来；
// check_error 是最近一次测试的失败原因；prev_egress_ip、egress_changed_at 是出口变化前的 IP 和变化时间。
// 停用（status 为 disabled）的代理不参与自动分配，绑定它的账号暂停调度。
export interface Proxy {
  id: number
  name: string
  scheme: ProxyScheme
  host: string
  username?: string
  status: 'enabled' | 'disabled'
  egress_ip: string
  egress_country: string
  egress_region: string
  egress_city: string
  egress_org: string
  latency_ms: number
  prev_egress_ip?: string
  egress_changed_at: number
  checked_at: number
  check_error?: string
  accounts: { id: number; email: string }[]
  created_at: number
}
export interface ProxyAddResult {
  line: string
  status: 'added' | 'duplicate' | 'invalid'
  message?: string
  id?: number
}
// ProxyChoice 是添加账号、批量分配时选的代理：auto 自动分配空闲代理，none 直连，proxy 用 proxy_id。
export interface ProxyChoice {
  mode: 'auto' | 'none' | 'proxy'
  proxy_id?: number
}

// --- 数据 ---

// RetentionView 是用量记录的保留设置和现状：records 条数、oldest_at 最早一条（Unix 毫秒，没有记录为 0）、
// expired 超过保留期待清理的条数、db_bytes 数据库文件（含 WAL）大小、last 最近一次清理、running 正在清理。
export interface RetentionSettings {
  enabled: boolean
  days: number
}
export interface PurgeResult {
  at: number
  days: number
  deleted: number
  duration_ms: number
  manual: boolean
  error?: string
}
export interface RetentionView {
  settings: RetentionSettings
  records: number
  oldest_at: number
  expired: number
  db_bytes: number
  last: PurgeResult | null
  running: boolean
}
