// 演示模式的模拟后端：演示站是部署在 Cloudflare Pages 上的纯静态站点，没有服务器，管理端的 /api/admin 请求
// 都在浏览器里由这里应答（api/client.ts 只在 vite --mode demo 构建时动态加载本模块，正式构建不包含）。
//
// 模型目录、思考档位、价格表、客户端过滤的预置名单是真实快照（catalog.json，用 scripts/demo-catalog.mjs 更新）；
// 账号、Key、代理、用量都是编造的：邮箱用保留域名 example.com，IP 用文档专用网段，运营商用文档专用 ASN。
// 改动只留在内存里，刷新页面恢复。
import catalogJson from './catalog.json'
import type {
  QuotaSummary,
  Account,
  AccountList,
  AccountStats,
  ApiKey,
  ApiKeySettings,
  ClientFilter,
  ClientFilterResponse,
  Cooldown,
  HourlyUsage,
  ImportResult,
  KeyModelStat,
  Live,
  ModelInfo,
  ModelStat,
  Overview,
  PriceEntry,
  PricesView,
  ProbeResult,
  Proxy,
  ProxyAddResult,
  ProxyScheme,
  RequestHandling,
  RequestHandlingResponse,
  RetentionSettings,
  RetentionView,
  Scheduling,
  SchedulingResponse,
  Settings,
  SettingsResponse,
  Subscription,
  SystemPrompt,
  SystemPromptResponse,
  SystemView,
  TierFamily,
  UsageAttempt,
  UsageLog,
  UsageRecord,
} from '@/api/client'

export const DEMO_PASSWORD = 'demo'

export interface DemoResponse {
  status: number
  data: unknown
}

interface Catalog {
  models: ModelInfo[]
  tiers: string[]
  families: TierFamily[]
  prices: PricesView
  model_names: { models: string[]; families: string[] }
  default_keywords: string[]
}
// JSON 里的字符串推断不出 'official' 这类字面量类型，这里按接口类型转一下；快照由脚本从真实接口生成，结构一致。
const catalog = catalogJson as unknown as Catalog

const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR
const start = Date.now()

// 固定种子的伪随机数：每次打开演示站看到的数据一样（时间相对于打开的时刻）。
function mulberry32(seed: number) {
  return () => {
    seed |= 0
    seed = (seed + 0x6d2b79f5) | 0
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}
const rand = mulberry32(20261125)
const between = (lo: number, hi: number) => lo + Math.floor(rand() * (hi - lo + 1))
function weighted<T>(items: [T, number][]): T {
  let r = rand() * items.reduce((s, [, w]) => s + w, 0)
  for (const [v, w] of items) if ((r -= w) < 0) return v
  return items[items.length - 1][0]
}
const hex = (n: number) => Array.from({ length: n }, () => between(0, 15).toString(16)).join('')

// family 返回模型所属的思考档位家族（如 gemini-3.8-flash-medium → gemini-3.8-flash），不属于任何家族时原样返回。
function family(id: string): string {
  let best = ''
  for (const f of catalog.families) if ((id === f.family || id.startsWith(f.family + '-')) && f.family.length > best.length) best = f.family
  return best || id
}
// resolve 把家族名和档位解析成目录里的模型 ID，目录里没有时退回家族名。
function resolve(fam: string, tier: string): string {
  const id = `${fam}-${tier}`
  return catalog.models.some((m) => m.id === id) ? id : fam
}

// --- 代理 ---

interface DemoProxy extends Omit<Proxy, 'accounts'> {}
let proxies: DemoProxy[] = [
  {
    id: 1, name: '美西 1', scheme: 'socks5h', host: '203.0.113.10:1080', username: 'demo', status: 'enabled',
    egress_ip: '203.0.113.10', egress_country: 'US', egress_region: 'California', egress_city: 'San Jose',
    egress_org: 'AS64500 Example Networks', latency_ms: 182, egress_changed_at: 0, checked_at: start - 2 * HOUR, created_at: start - 6 * DAY,
  },
  {
    id: 2, name: '东京 1', scheme: 'http', host: '198.51.100.20:8080', status: 'enabled',
    egress_ip: '198.51.100.20', egress_country: 'JP', egress_region: 'Tokyo', egress_city: 'Tokyo',
    egress_org: 'AS64501 Example Cloud', latency_ms: 96, prev_egress_ip: '198.51.100.19', egress_changed_at: start - DAY,
    checked_at: start - 2 * HOUR, created_at: start - 6 * DAY,
  },
  {
    id: 3, name: '新加坡 1', scheme: 'socks5', host: '192.0.2.30:1080', status: 'disabled',
    egress_ip: '', egress_country: '', egress_region: '', egress_city: '', egress_org: '', latency_ms: 0, egress_changed_at: 0,
    checked_at: start - 3 * HOUR, check_error: 'proxyconnect tcp: dial tcp 192.0.2.30:1080: i/o timeout', created_at: start - 2 * DAY,
  },
]
let nextProxyId = 4

// --- 账号 ---

interface DemoAccount extends Omit<Account, 'proxy' | 'in_flight' | 'model_count' | 'weekly_quota_on'> {
  proxy_id: number
}
const proSubscription = (): Subscription => ({
  current_tier: 'Antigravity', current_tier_id: 'free-tier', paid_tier: 'Google AI Pro', paid_tier_id: 'g1-pro-tier',
  credits: 0, downgraded: false, checked_at: start - 3 * HOUR,
})
// weeklySummary 是开了每周额度的账号拉到的分组额度（照 2026-11-26 实抓的格式）。
function weeklySummary(gemini5h: number, geminiWeek: number, third5h: number, thirdWeek: number): QuotaSummary {
  const bucket = (id: string, window: string, remaining: number, resetHours: number) => ({
    bucket_id: id, window, display_name: window === '5h' ? 'Five Hour Limit Remaining' : 'Weekly Limit Remaining',
    remaining_fraction: remaining, reset_time: new Date(start + resetHours * HOUR).toISOString(),
  })
  return {
    groups: [
      {
        display_name: 'Gemini Models',
        description: 'Models within this group: Gemini 4 Neon, Gemini 4 Carbon, Gemini 4 Argon, Gemini 4 Hydrogen, Orchestrator (Neon), Orchestrator (Carbon), Orchestrator (Argon), Gemini 3.8 Flash',
        buckets: [bucket('gemini-weekly', 'weekly', geminiWeek, 6 * 24 + 23), bucket('gemini-5h', '5h', gemini5h, 4)],
      },
      {
        display_name: 'Claude and GPT models',
        description: 'Models within this group: Claude Opus 5.5, Claude Sonnet 5.5, GPT-OSS 120B',
        buckets: [bucket('3p-weekly', 'weekly', thirdWeek, 6 * 24 + 2), bucket('3p-5h', '5h', third5h, 2)],
      },
    ],
  }
}
// carbonPlaceholder 模拟 Carbon 对这个账号不开放：目录里没有 Carbon 各档，只有一个占位条目。
function withoutCarbon(models: ModelInfo[]): ModelInfo[] {
  return [
    ...models.filter((m) => !m.id.startsWith('gemini-4-carbon')),
    { id: 'gemini-4-carbon-disabled', display_name: 'Gemini 4 Carbon', supports_thinking: true },
  ]
}
// accountModels 给每个模型配上额度：同一家族同一个剩余比例；low 家族的额度压到避让阈值以下。
function accountModels(seed: number, low = ''): ModelInfo[] {
  const r = mulberry32(seed)
  const frac = new Map<string, number>()
  return catalog.models.map((m) => {
    const fam = family(m.id)
    if (!frac.has(fam)) frac.set(fam, fam === low ? 0.06 : Math.round((0.35 + r() * 0.65) * 100) / 100)
    return { ...m, remaining_fraction: frac.get(fam), reset_time: new Date(start + (2 + Math.floor(r() * 4)) * HOUR).toISOString() }
  })
}
const userAgent = 'antigravity/hub/2.31.2 (aidev_client; os_type=linux; arch=amd64; cl=1019359392)'
function newAccount(id: number, email: string, name: string, proxyId: number, extra: Partial<DemoAccount> = {}): DemoAccount {
  return {
    id, email, name, project_id: `demo-project-${hex(5)}`, user_project_id: '', user_agent: userAgent, status: 'enabled',
    max_concurrency: 0, cooldowns: [], models: accountModels(id), models_refreshed_at: start - between(2, 14) * MIN,
    proxy_id: proxyId, proxy_paused_until: 0, subscription: proSubscription(), synced_at: start - between(1, 5) * DAY,
    created_at: start - between(5, 9) * DAY, weekly_quota: '', quota_summary: null, quota_summary_at: 0, ...extra,
  }
}
const sonnet = resolve('claude-sonnet-5-5', 'medium')
let accounts: DemoAccount[] = [
  newAccount(1, 'alice@example.com', 'Alice', 1, {
    weekly_quota: 'on', quota_summary: weeklySummary(0.96, 0.97, 1, 0.93), quota_summary_at: start - 6 * MIN,
  }),
  newAccount(2, 'bob@example.com', 'Bob', 2, {
    max_concurrency: 5,
    // Claude 和 GPT 的每周额度用完了：重置前选号不选它
    weekly_quota: 'on', quota_summary: weeklySummary(0.8, 0.62, 0.9, 0), quota_summary_at: start - 6 * MIN,
    cooldowns: [{ model: sonnet, until: start + 4 * MIN, reason: 'rate_limit' }],
  }),
  newAccount(3, 'carol@example.com', 'Carol', 0, { models: withoutCarbon(accountModels(3, 'gemini-3.8-flash')) }),
  newAccount(4, 'dave@example.com', 'Dave', 3),
  newAccount(5, 'erin@example.com', 'Erin', 0, {
    status: 'disabled', disabled_reason: 'refresh token 已失效（invalid_grant）', subscription: null,
  }),
]
let nextAccountId = 6

// --- Key ---

interface DemoKey extends ApiKeySettings {
  id: number
  prefix: string
  status: string
  created_at: number
  last_used_at: number
}
let keys: DemoKey[] = [
  { id: 1, name: 'Cursor 编辑器', prefix: 'agy-8fK2', status: 'enabled', created_at: start - 8 * DAY, last_used_at: 0, expires_at: 0, token_limit: 0, token_period: '', allowed_models: [] },
  { id: 2, name: '团队共享', prefix: 'agy-Qw71', status: 'enabled', created_at: start - 7 * DAY, last_used_at: 0, expires_at: 0, token_limit: 2_000_000, token_period: 'day', allowed_models: ['gemini-*'] },
  { id: 3, name: '临时测试', prefix: 'agy-Zp03', status: 'enabled', created_at: start - 6 * DAY, last_used_at: 0, expires_at: start - 2 * DAY, token_limit: 0, token_period: '', allowed_models: [] },
]
let nextKeyId = 4

// --- 价格 ---

const prices: PricesView = structuredClone(catalog.prices)
function priceOf(model: string): PriceEntry | null {
  const fam = family(model)
  return prices.models.find((r) => r.model === fam || r.ids.includes(model))?.price ?? null
}

// --- 用量 ---

const models: [[string, string[]], number][] = [
  [['gemini-3.8-flash', ['low', 'medium', 'high']], 45],
  [['claude-sonnet-5-5', ['medium', 'high']], 25],
  [['gemini-4-neon', ['high']], 15],
  [['claude-opus-5-5', ['high']], 10],
  [['gpt-oss-120b', ['medium']], 5],
]
const clients: [string, string][] = [
  ['192.0.2.15', 'google-genai-sdk/1.39.0 gl-python/3.12.4'],
  ['192.0.2.27', 'Cline/3.21.0 (VS Code)'],
  ['198.51.100.7', 'CherryStudio/1.5.3'],
  ['192.0.2.15', 'curl/8.13.0'],
]

let usage: UsageRecord[] = []
let nextUsageId = 1
// genUsage 编造近 3 天的用量：白天多、夜里少；大部分一次成功，少数换号后成功、无可用账号、请求错误或上游 5xx。
function genUsage() {
  const list: UsageRecord[] = []
  for (let t = start - 3 * DAY; t < start - MIN; ) {
    const hour = new Date(t).getHours()
    const busy = hour >= 9 && hour <= 23 ? 1 : 0.25
    t += Math.round((4 + rand() * 14) * MIN / busy)
    if (t >= start) break
    list.push(fakeRecord(t))
  }
  usage = list
}
function fakeRecord(at: number): UsageRecord {
  const [fam, tiers] = weighted(models)
  const tier = tiers[between(0, tiers.length - 1)]
  const resolved = resolve(fam, tier)
  const keyId = at < start - 2 * DAY ? weighted([[1, 5], [2, 3], [3, 2]]) : weighted([[1, 6], [2, 4]])
  const [ip, ua] = clients[between(0, clients.length - 1)]
  const stream = rand() < 0.7
  const ready = accounts.filter((a) => a.id <= 4)
  const first = ready[between(0, ready.length - 1)]
  const outcome = weighted<'ok' | 'retry' | 'no_account' | 'client_error' | 'server_error'>([['ok', 88], ['retry', 6], ['no_account', 3], ['client_error', 2], ['server_error', 1]])
  const header = between(500, 2400)
  const total = stream ? header + between(1500, 22000) : header + between(200, 6000)
  const attempts: UsageAttempt[] = []
  const att = (a: DemoAccount, status: number, result: string, extra: Partial<UsageAttempt> = {}): UsageAttempt => ({
    try: attempts.length + 1, account_id: a.id, account: a.email, model: resolved, status, result, duration_ms: between(300, 1800), ...extra,
  })
  let status = 200
  let error: { kind: string; message?: string } | undefined
  if (outcome === 'ok') attempts.push(att(first, 200, 'success', { duration_ms: total }))
  else if (outcome === 'retry') {
    const second = ready.find((a) => a.id !== first.id) ?? first
    attempts.push(att(first, 429, 'rate_limit', { error: 'RESOURCE_EXHAUSTED: Resource has been exhausted (e.g. check quota).', cooldown_ms: 60_000 }))
    attempts.push(att(second, 200, 'success', { duration_ms: total }))
  } else if (outcome === 'no_account') {
    status = 429
    error = { kind: 'no_account', message: 'all accounts are cooling down for this model' }
  } else if (outcome === 'client_error') {
    status = 400
    attempts.push(att(first, 400, 'client_error', { error: 'Request contains an invalid argument.' }))
    error = { kind: 'client_error', message: 'Request contains an invalid argument.' }
  } else {
    status = 503
    for (const a of ready.slice(0, 3)) attempts.push(att(a, 503, 'server_error', { error: 'The service is currently unavailable.', cooldown_ms: 30_000 }))
    error = { kind: 'server_error', message: 'upstream is temporarily unavailable, retry later' }
  }
  const ok = status === 200
  const prompt = ok || status === 503 ? between(600, 24000) : 0
  const output = ok ? between(40, 2600) : 0
  const thoughts = ok && tier !== 'low' && rand() < 0.7 ? between(100, 3000) : null
  const cached = ok && fam.startsWith('claude') && rand() < 0.5 ? Math.round(prompt * 0.6) : null
  const totalTokens = ok ? prompt + output + (thoughts ?? 0) : 0
  const price = ok ? priceOf(resolved) : null
  const nanos = price ? Math.round(((prompt - (cached ?? 0)) * price.price.input + (cached ?? 0) * (price.price.cache_read ?? price.price.input) + (output + (thoughts ?? 0)) * price.price.output) / 1000) : null
  const last = attempts.at(-1)
  // Claude 的请求大约一半来自 Claude Code（Anthropic 接口），请求模型是官方名；其余约两成来自 OpenAI 兼容客户端，
  // 一成多来自 Codex（Responses 接口）
  const pick = rand()
  const origin = fam.startsWith('claude') && rand() < 0.5
    ? { protocol: 'anthropic' as const, request_model: `${fam}-20261015` }
    : { protocol: pick < 0.2 ? ('openai' as const) : pick < 0.35 ? ('openai_responses' as const) : ('gemini' as const), request_model: fam }
  // 会话粘性的依据：Claude Code 带会话头（部分是子代理），Codex 和部分 OpenAI 客户端带 prompt_cache_key，其余按首条消息
  const affinitySource = origin.protocol === 'anthropic'
    ? (rand() < 0.2 ? 'claude_code_agent' as const : 'claude_code' as const)
    : origin.protocol === 'openai_responses' || (origin.protocol === 'openai' && rand() < 0.3) ? 'prompt_cache_key' as const : 'first_message' as const
  return {
    id: nextUsageId++, request_id: hex(16), api_key_id: keyId, account_id: ok && last ? last.account_id : 0,
    ...origin,
    resolved_model: resolved, upstream_model: resolved, reasoning: tier,
    prompt_tokens: ok ? prompt : 0, output_tokens: output, cached_tokens: cached, thoughts_tokens: thoughts, total_tokens: totalTokens,
    status_code: status, stream, first_token_ms: stream && ok ? header + between(20, 400) : null, latency_ms: ok ? total : between(40, 3000),
    created_at: at, client_ip: ip, user_agent: origin.protocol === 'openai_responses' ? 'codex_cli_rs/0.172.0 (Mac OS 15.5.0; arm64) iTerm.app/3.5.14' : ua,
    detail: {
      timings: ok ? { acquire_ms: 0, token_ms: 0, header_ms: header, upstream_ms: total - 30, ...(stream ? { first_event_ms: header + 5, first_text_ms: header + 120 } : {}) } : undefined,
      slot: ok ? { in_use: between(1, 3), limit: 15 } : undefined,
      attempts: attempts.length ? attempts : undefined,
      error,
      affinity: ok ? { result: weighted<'hit' | 'new' | 'diverted' | 'switched'>([['hit', 70], ['new', 22], ['diverted', 5], ['switched', 3]]), source: affinitySource } : undefined,
      cost: price && nanos !== null ? { nanos, model: price.model, source: price.source, input: price.price.input, cache_read: price.price.cache_read ?? price.price.input, output: price.price.output } : undefined,
    },
    cost_nanos: nanos,
  }
}
genUsage()
for (const k of keys) k.last_used_at = Math.max(0, ...usage.filter((r) => r.api_key_id === k.id).map((r) => r.created_at))

// --- 设置 ---

let tierSettings: Settings = { default_tier: 'medium', model_tiers: {} }
const schedulingDefaults: Scheduling = {
  token_refresh_early: true, token_refresh_min_lead: 300, token_refresh_max_lead: 900, concurrency: 15, slot_wait: 30,
  max_attempts: 3, cooldown_wait: 30, rate_limit_base: 60, rate_limit_max: 300, server_error_cooldown: 30,
  validation_cooldown: 600, model_forbidden_cooldown: 300, quota_refresh: 900, low_quota: 10, stream_idle: 120,
  require_proxy: false, strategy: 'round_robin', sticky: true, sticky_mode: 'strict', sticky_ttl: 3600, weekly_quota: false,
}
let scheduling: Scheduling = { ...schedulingDefaults }
let clientFilter: ClientFilter = { enabled: false, mode: 'rewrite', use_default_keywords: true, rules: '' }
let systemPrompt: SystemPrompt = { enabled: true, mode: 'as_user', template: '' }
// 内置官方基线（演示站只取开头做预览，完整内容见自建实例）。
const BUILTIN_SYSTEM_PROMPT =
  'You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.\n' +
  'You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.\n' +
  'The USER will send you requests, which you must always prioritize addressing. User requests are enclosed within <USER_REQUEST> tags.\n\n' +
  '（演示站省略其余内容，完整基线在自建实例上查看）'
let requestHandling: RequestHandling = { remote_images_enabled: false, return_thoughts: true }
let retention: RetentionSettings = { enabled: true, days: 90 }
let lastPurge: RetentionView['last'] = { at: start - 20 * HOUR, days: 90, deleted: 0, duration_ms: 12, manual: false }

// --- 视图 ---

const proxyById = (id: number) => proxies.find((p) => p.id === id)
function accountView(a: DemoAccount): Account {
  const p = proxyById(a.proxy_id)
  const now = Date.now()
  const cooldowns: Cooldown[] = a.cooldowns.filter((c) => c.until > now)
  const { proxy_id: _, ...rest } = a
  return {
    ...rest, cooldowns, model_count: a.models.length, in_flight: live.in_flight[String(a.id)] ?? 0,
    weekly_quota_on: a.weekly_quota === 'on' || (a.weekly_quota === '' && scheduling.weekly_quota),
    proxy: p ? { id: p.id, name: p.name, egress_ip: p.egress_ip, egress_country: p.egress_country, check_error: p.check_error, status: p.status } : null,
  }
}
function proxyView(p: DemoProxy): Proxy {
  return { ...p, accounts: accounts.filter((a) => a.proxy_id === p.id).map((a) => ({ id: a.id, email: a.email })) }
}
const proxyList = () => ({ proxies: proxies.map(proxyView) })

// 每天、每月按本地时区划分（服务器时区）。
function periodStart(period: string, now: number): number {
  const d = new Date(now)
  if (period === 'day') return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
  if (period === 'month') return new Date(d.getFullYear(), d.getMonth(), 1).getTime()
  return 0
}
function periodReset(period: string, now: number): number {
  const d = new Date(now)
  if (period === 'day') return new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1).getTime()
  if (period === 'month') return new Date(d.getFullYear(), d.getMonth() + 1, 1).getTime()
  return 0
}
function sumCost(list: UsageRecord[]): number | null {
  const priced = list.filter((r) => r.cost_nanos !== null)
  return priced.length ? priced.reduce((s, r) => s + (r.cost_nanos ?? 0), 0) : null
}
function keyView(k: DemoKey): ApiKey {
  const now = Date.now()
  const mine = usage.filter((r) => r.api_key_id === k.id)
  const recent = mine.filter((r) => r.created_at >= now - 30 * DAY)
  const since = periodStart(k.token_period, now)
  return {
    ...k, expired: k.expires_at > 0 && k.expires_at <= now,
    period_used: k.token_limit > 0 ? mine.filter((r) => r.created_at >= since).reduce((s, r) => s + r.total_tokens, 0) : 0,
    period_reset_at: k.token_limit > 0 ? periodReset(k.token_period, now) : 0,
    recent: { requests: recent.length, tokens: recent.reduce((s, r) => s + r.total_tokens, 0), cost_nanos: sumCost(recent) },
  }
}
function logView(r: UsageRecord): UsageLog {
  const a = accounts.find((x) => x.id === r.account_id)
  const k = keys.find((x) => x.id === r.api_key_id)
  return { ...r, account_email: a?.email ?? '', api_key_name: k?.name ?? '', api_key_prefix: k?.prefix ?? '' }
}

// 实时并发：每次读取时在 0～2 之间轻微变化，看起来像有流量在跑。
const live: Live = { in_flight: {}, total: 0, now: start }
function tickLive(): Live {
  const now = Date.now()
  live.in_flight = {}
  for (const a of accounts) {
    if (a.status !== 'enabled' || proxyById(a.proxy_id)?.status === 'disabled') continue
    const n = Math.max(0, Math.round(Math.sin(now / 9000 + a.id * 1.7) * 1.4))
    if (n) live.in_flight[String(a.id)] = n
  }
  live.total = Object.values(live.in_flight).reduce((s, n) => s + n, 0)
  live.now = now
  return { ...live, in_flight: { ...live.in_flight } }
}
tickLive()

function overview(): Overview {
  const now = Date.now()
  const day = usage.filter((r) => r.created_at >= now - DAY)
  const byModel = new Map<string, ModelStat>()
  for (const r of day) {
    const s = byModel.get(r.request_model) ?? { model: r.request_model, requests: 0, prompt_tokens: 0, output_tokens: 0, total_tokens: 0, cost_nanos: null }
    s.requests++
    s.prompt_tokens += r.prompt_tokens
    s.output_tokens += r.output_tokens
    s.total_tokens += r.total_tokens
    if (r.cost_nanos !== null) s.cost_nanos = (s.cost_nanos ?? 0) + r.cost_nanos
    byModel.set(r.request_model, s)
  }
  const first = Math.floor(now / HOUR) * HOUR - 23 * HOUR
  const hourly: HourlyUsage[] = Array.from({ length: 24 }, (_, i) => ({ start: first + i * HOUR, requests: 0, errors: 0, prompt_tokens: 0, output_tokens: 0, total_tokens: 0 }))
  for (const r of usage) {
    const i = Math.floor((r.created_at - first) / HOUR)
    if (i < 0 || i >= 24) continue
    const h = hourly[i]
    h.requests++
    if (r.status_code < 200 || r.status_code >= 300) h.errors++
    h.prompt_tokens += r.prompt_tokens
    h.output_tokens += r.output_tokens
    h.total_tokens += r.total_tokens
  }
  const lv = tickLive()
  return {
    accounts_total: accounts.length,
    accounts_ready: accounts.filter((a) => a.status === 'enabled' && proxyById(a.proxy_id)?.status !== 'disabled').length,
    api_keys_total: keys.length,
    api_keys_enabled: keys.filter((k) => k.status === 'enabled' && !(k.expires_at > 0 && k.expires_at <= now)).length,
    requests_24h: day.length,
    in_flight: lv.total,
    usage_by_model: [...byModel.values()].sort((a, b) => b.requests - a.requests),
    hourly,
  }
}

function accountStats(id: number, since: number): AccountStats {
  const byModel = new Map<string, { attempts: number; success: number; ms: number }>()
  const errors: AccountStats['recent_errors'] = []
  for (const r of usage) {
    if (r.created_at < since) continue
    for (const a of r.detail?.attempts ?? []) {
      if (a.account_id !== id) continue
      const s = byModel.get(a.model) ?? { attempts: 0, success: 0, ms: 0 }
      s.attempts++
      if (a.result === 'success') {
        s.success++
        s.ms += a.duration_ms
      } else {
        errors.push({ usage_id: r.id, created_at: r.created_at, model: a.model, status: a.status ?? 0, result: a.result, error: a.error ?? '', cooldown_ms: a.cooldown_ms ?? 0 })
      }
      byModel.set(a.model, s)
    }
  }
  return {
    models: [...byModel.entries()].map(([model, s]) => ({ model, attempts: s.attempts, success: s.success, avg_ms: s.success ? Math.round(s.ms / s.success) : 0 })).sort((a, b) => b.attempts - a.attempts),
    recent_errors: errors.sort((a, b) => b.created_at - a.created_at).slice(0, 10),
    cost_nanos: sumCost(usage.filter((r) => r.account_id === id && r.created_at >= since)),
  }
}

function keyStats(id: number, since: number): { models: KeyModelStat[] } {
  const byModel = new Map<string, KeyModelStat & { ms: number }>()
  for (const r of usage) {
    if (r.api_key_id !== id || r.created_at < since) continue
    const s = byModel.get(r.request_model) ?? { model: r.request_model, requests: 0, success: 0, tokens: 0, avg_ms: 0, cost_nanos: null, ms: 0 }
    s.requests++
    if (r.status_code >= 200 && r.status_code < 300) {
      s.success++
      s.ms += r.latency_ms
    }
    s.tokens += r.total_tokens
    if (r.cost_nanos !== null) s.cost_nanos = (s.cost_nanos ?? 0) + r.cost_nanos
    byModel.set(r.request_model, s)
  }
  return {
    models: [...byModel.values()].map(({ ms, ...s }) => ({ ...s, avg_ms: s.success ? Math.round(ms / s.success) : 0 })).sort((a, b) => b.requests - a.requests),
  }
}

function retentionView(): RetentionView {
  const oldest = usage.reduce((m, r) => Math.min(m, r.created_at), Infinity)
  return {
    settings: { ...retention }, records: usage.length, oldest_at: Number.isFinite(oldest) ? oldest : 0, expired: 0,
    db_bytes: 1_800_000 + usage.length * 900, last: lastPurge, running: false,
  }
}

// --- 路由 ---

class DemoError extends Error {
  constructor(public status: number, message: string) {
    super(message)
  }
}
const fail = (status: number, message: string): never => {
  throw new DemoError(status, message)
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const ok = { ok: true }

function findAccount(id: number): DemoAccount {
  return accounts.find((a) => a.id === id) ?? fail(404, 'account not found')
}
function findKey(id: number): DemoKey {
  return keys.find((k) => k.id === id) ?? fail(404, 'key not found')
}
function findProxy(id: number): DemoProxy {
  return proxies.find((p) => p.id === id) ?? fail(404, 'proxy not found')
}

type Body = Record<string, unknown>
type Handler = (m: RegExpMatchArray, q: URLSearchParams, body: Body) => unknown
const routes: [string, RegExp, Handler][] = []
const on = (method: string, pattern: string, h: Handler) => routes.push([method, new RegExp(`^${pattern}$`), h])

on('GET', '/session', () => ({ version: import.meta.env.VITE_APP_VERSION || 'demo' }))

// 版本：演示站已是最新版本，不演示升级（升级会重启服务、刷新页面）。
const demoVersion = import.meta.env.VITE_APP_VERSION || 'demo'
const systemView = (): SystemView => ({
  version: demoVersion,
  deployment: 'docker',
  can_check: true,
  can_upgrade: true,
  latest: { version: demoVersion, notes: '', url: 'https://github.com/shiceai/agy2api/releases', published_at: start - DAY },
  update_available: false,
  checked_at: start,
  progress: {},
  antigravity: { shipped: '2.35.2', latest: '2.35.2', outdated: false, checked_at: start - HOUR },
})
on('GET', '/system', systemView)

// 模型别名：演示站只存在内存里，试解析用简化的规则（先看目录，再去掉后缀，最后按规则通配）。
const aliasPresets = [
  { pattern: 'claude-*opus*', target: 'claude-opus-5-5' },
  { pattern: 'claude-*sonnet*', target: 'claude-sonnet-5-5' },
  { pattern: 'claude-*haiku*', target: 'claude-sonnet-5-5-low' },
]
let aliasSettings = { use_presets: true, rules: '' }
const aliasView = () => ({ settings: aliasSettings, presets: aliasPresets })
const escapeRegExp = (x: string) => x.replace(/[.+?^${}()|[\]\\]/g, (c) => '\\' + c)
on('GET', '/settings/model-aliases', aliasView)
on('PUT', '/settings/model-aliases', (_m, _q, b) => {
  aliasSettings = { use_presets: b.use_presets !== false, rules: String(b.rules ?? '') }
  return aliasView()
})
on('POST', '/settings/model-aliases/test', (_m, _q, b) => {
  const model = String(b.model ?? '')
  const known = (id: string) => catalog.models.some((m) => m.id === id) || catalog.families.some((f) => f.family === id)
  const glob = (p: string, x: string) => new RegExp('^' + p.split('*').map(escapeRegExp).join('.*') + '$').test(x)
  const custom = aliasSettings.rules
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith('#') && l.includes('=>'))
    .map((l) => ({ pattern: l.split('=>')[0].trim().toLowerCase(), target: l.split('=>')[1].trim() }))
  const rules = [...custom, ...(aliasSettings.use_presets ? aliasPresets : [])]
  const normalized = model.trim().toLowerCase().replace(/\[[^\]]*\]$/, '').replace(/-\d{8}$/, '')
  let target = known(model) ? model : normalized
  let rule: { pattern: string; target: string } | null = null
  if (!known(target)) {
    rule = rules.find((r) => glob(r.pattern, normalized)) ?? null
    if (rule) target = rule.target
  }
  const family = catalog.families.find((f) => f.family === target)
  const resolved = family ? `${target}-${family.tiers.includes('medium') ? 'medium' : family.tiers[0]}` : catalog.models.some((m) => m.id === target) ? target : undefined
  return { model, target, rule, ...(resolved ? { resolved } : {}) }
})
let anthropicCompat = { system_as_user: true }
on('GET', '/settings/anthropic-compat', () => ({ settings: anthropicCompat }))
on('PUT', '/settings/anthropic-compat', (_m, _q, b) => {
  anthropicCompat = { system_as_user: b.system_as_user !== false }
  return { settings: anthropicCompat }
})
on('POST', '/system/check', async () => {
  await sleep(600)
  return { ...systemView(), checked_at: Date.now() }
})
on('POST', '/system/(upgrade|revert)', () => fail(409, '演示站不能升级'))
on('GET', '/overview', () => overview())
on('GET', '/live', () => tickLive())

on('GET', '/accounts', (): AccountList => ({
  accounts: accounts.map(accountView), default_concurrency: scheduling.concurrency, low_quota: scheduling.low_quota,
  quota_refresh: scheduling.quota_refresh, weekly_quota: scheduling.weekly_quota,
}))
on('POST', '/accounts/(\\d+)/(enable|disable)', (m) => {
  const a = findAccount(Number(m[1]))
  a.status = m[2] === 'enable' ? 'enabled' : 'disabled'
  if (a.status === 'enabled') a.disabled_reason = undefined
  return ok
})
on('DELETE', '/accounts/(\\d+)', (m) => {
  findAccount(Number(m[1]))
  accounts = accounts.filter((a) => a.id !== Number(m[1]))
  return ok
})
on('PUT', '/accounts/(\\d+)/settings', (m, _q, b) => {
  const a = findAccount(Number(m[1]))
  if (b.max_concurrency !== undefined) a.max_concurrency = Math.max(0, Number(b.max_concurrency) || 0)
  if (b.weekly_quota !== undefined) {
    const mode = String(b.weekly_quota)
    if (mode !== '' && mode !== 'on' && mode !== 'off') return fail(400, 'weekly_quota must be empty (follow the global setting), on or off')
    a.weekly_quota = mode
    // 开启时模拟立即拉到一次
    if (accountView(a).weekly_quota_on && !a.quota_summary) {
      a.quota_summary = weeklySummary(0.88, 0.71, 0.95, 0.9)
      a.quota_summary_at = Date.now()
    }
  }
  return { account: accountView(a) }
})
on('PUT', '/accounts/(\\d+)/proxy', (m, _q, b) => {
  const id = Number(b.proxy_id) || 0
  if (id) findProxy(id)
  findAccount(Number(m[1])).proxy_id = id
  return ok
})
on('POST', '/accounts/(\\d+)/sync', (m) => {
  const a = findAccount(Number(m[1]))
  a.synced_at = a.models_refreshed_at = Date.now()
  return { account: accountView(a) }
})
on('POST', '/accounts/(\\d+)/test', async (m, _q, b): Promise<ProbeResult> => {
  findAccount(Number(m[1]))
  const latency = between(700, 1600)
  await sleep(latency)
  return { ok: true, model: String(b.model || resolve('gemini-3.8-flash', 'low')), status: 200, reply: '你好！这是演示站的模拟回复，没有真的调用上游。', latency_ms: latency }
})
on('GET', '/accounts/(\\d+)/stats', (m, q) => accountStats(Number(m[1]), Number(q.get('since')) || 0))
on('POST', '/accounts/batch', (_m, _q, b) => {
  const ids = new Set((b.ids as number[]) ?? [])
  for (const a of accounts.filter((x) => ids.has(x.id))) {
    if (b.action === 'enable') a.status = 'enabled'
    if (b.action === 'disable') a.status = 'disabled'
    if (b.action === 'sync') a.synced_at = a.models_refreshed_at = Date.now()
  }
  if (b.action === 'delete') accounts = accounts.filter((a) => !ids.has(a.id))
  return ok
})
on('POST', '/accounts/assign-proxy', (_m, _q, b) => {
  const ids = new Set((b.ids as number[]) ?? [])
  let assigned = 0
  let skipped = 0
  for (const a of accounts.filter((x) => ids.has(x.id))) {
    if (b.mode === 'none') a.proxy_id = 0
    else if (b.mode === 'proxy') a.proxy_id = Number(b.proxy_id) || 0
    else if (a.proxy_id) {
      skipped++
      continue
    } else {
      const free = proxies.find((p) => p.status === 'enabled' && !accounts.some((x) => x.proxy_id === p.id))
      if (!free) {
        skipped++
        continue
      }
      a.proxy_id = free.id
    }
    assigned++
  }
  return { assigned, skipped }
})
on('POST', '/accounts/import', (_m, _q, b): ImportResult => {
  if (!String(b.refresh_token ?? '').trim()) fail(400, 'refresh_token is required')
  const id = nextAccountId++
  const proxy = b.proxy as { mode?: string; proxy_id?: number } | undefined
  let proxyId = 0
  if (proxy?.mode === 'proxy') proxyId = Number(proxy.proxy_id) || 0
  if (proxy?.mode === 'auto') proxyId = proxies.find((p) => p.status === 'enabled' && !accounts.some((x) => x.proxy_id === p.id))?.id ?? 0
  const a = newAccount(id, `demo-user-${id}@example.com`, `Demo User ${id}`, proxyId, { created_at: Date.now(), synced_at: Date.now(), models_refreshed_at: Date.now() })
  accounts.push(a)
  return { account: accountView(a), created: true, reenabled: false }
})
const noOAuth = () => fail(400, '演示站不能真的登录 Google，可以在"导入"里随便填一行 refresh token 体验添加账号')
on('POST', '/accounts/oauth/start', noOAuth)
on('POST', '/accounts/oauth/complete', noOAuth)

on('GET', '/keys', () => ({ keys: keys.map(keyView) }))
function keySettings(b: Body): ApiKeySettings {
  const name = String(b.name ?? '').trim()
  if (!name) fail(400, 'name is required')
  return {
    name, expires_at: Number(b.expires_at) || 0, token_limit: Math.max(0, Number(b.token_limit) || 0),
    token_period: (b.token_period as ApiKeySettings['token_period']) ?? '', allowed_models: (b.allowed_models as string[]) ?? [],
  }
}
on('POST', '/keys', (_m, _q, b) => {
  const secret = `agy-demo${hex(24)}`
  keys.push({ id: nextKeyId++, ...keySettings(b), prefix: secret.slice(0, 8), status: 'enabled', created_at: Date.now(), last_used_at: 0 })
  return { secret }
})
on('PUT', '/keys/(\\d+)', (m, _q, b) => {
  Object.assign(findKey(Number(m[1])), keySettings(b))
  return ok
})
on('POST', '/keys/(\\d+)/(enable|disable)', (m) => {
  findKey(Number(m[1])).status = m[2] === 'enable' ? 'enabled' : 'disabled'
  return ok
})
on('DELETE', '/keys/(\\d+)', (m) => {
  findKey(Number(m[1]))
  keys = keys.filter((k) => k.id !== Number(m[1]))
  return ok
})
on('GET', '/keys/(\\d+)/stats', (m, q) => keyStats(Number(m[1]), Number(q.get('since')) || 0))
on('GET', '/models', () => catalog.model_names)

on('GET', '/proxies', () => proxyList())
on('POST', '/proxies', (_m, _q, b) => {
  const results: ProxyAddResult[] = []
  for (const raw of String(b.text ?? '').split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const host = line.replace(/^[a-z0-9]+:\/\//i, '').replace(/^.*@/, '')
    if (!/^[\w.-]+:\d{2,5}$/.test(host)) {
      results.push({ line, status: 'invalid', message: '认不出地址，写成 host:port 或 协议://用户名:密码@host:port' })
      continue
    }
    if (proxies.some((p) => p.host === host)) {
      results.push({ line, status: 'duplicate' })
      continue
    }
    const id = nextProxyId++
    const scheme = ((line.match(/^([a-z0-9]+):\/\//i)?.[1] ?? b.scheme ?? 'http') as string).toLowerCase() as ProxyScheme
    proxies.push({
      id, name: host, scheme, host, status: 'enabled', egress_ip: host.split(':')[0], egress_country: 'US', egress_region: 'Virginia',
      egress_city: 'Ashburn', egress_org: 'AS64502 Example Transit', latency_ms: between(80, 260), egress_changed_at: 0,
      checked_at: Date.now(), created_at: Date.now(),
    })
    results.push({ line, status: 'added', id })
  }
  return { results }
})
on('PUT', '/proxies/(\\d+)', (m, _q, b) => {
  const p = findProxy(Number(m[1]))
  p.name = String(b.name ?? '').trim() || p.name
  const url = String(b.url ?? '').trim()
  if (url) p.host = url.replace(/^[a-z0-9]+:\/\//i, '').replace(/^.*@/, '')
  if (b.scheme) p.scheme = b.scheme as ProxyScheme
  return proxyList()
})
on('DELETE', '/proxies/(\\d+)', (m) => {
  const id = Number(m[1])
  findProxy(id)
  if (accounts.some((a) => a.proxy_id === id)) fail(409, 'proxy is still bound to accounts')
  proxies = proxies.filter((p) => p.id !== id)
  return proxyList()
})
on('POST', '/proxies/(enable|disable)', (m, _q, b) => {
  const ids = new Set((b.ids as number[]) ?? [])
  for (const p of proxies) if (ids.has(p.id)) p.status = m[1] === 'enable' ? 'enabled' : 'disabled'
  return proxyList()
})
on('POST', '/proxies/check', async (_m, _q, b) => {
  await sleep(between(600, 1200))
  const ids = new Set((b.ids as number[]) ?? [])
  for (const p of proxies) {
    if (!ids.has(p.id)) continue
    p.checked_at = Date.now()
    if (!p.check_error) p.latency_ms = Math.max(40, p.latency_ms + between(-25, 25))
  }
  return proxyList()
})

on('GET', '/usage/logs', (_m, q) => {
  const since = Number(q.get('since')) || 0
  const status = q.get('status')
  const stream = q.get('stream')
  const protocol = q.get('protocol')
  const requestId = q.get('request_id')
  const model = q.get('model')
  const accountId = Number(q.get('account_id')) || 0
  const keyId = Number(q.get('api_key_id')) || 0
  const list = usage
    .filter((r) => r.created_at >= since)
    .filter((r) => !status || (status === 'success') === (r.status_code >= 200 && r.status_code < 300))
    .filter((r) => !stream || (stream === 'stream') === r.stream)
    .filter((r) => !protocol || r.protocol === protocol)
    .filter((r) => !requestId || r.request_id.startsWith(requestId))
    .filter((r) => !model || r.request_model === model || r.resolved_model === model)
    .filter((r) => !accountId || r.account_id === accountId)
    .filter((r) => !keyId || r.api_key_id === keyId)
    .sort((a, b) => b.created_at - a.created_at)
  const offset = Number(q.get('offset')) || 0
  const limit = Number(q.get('limit')) || 50
  return { records: list.slice(offset, offset + limit).map(logView), total: list.length }
})
on('GET', '/usage/logs/(\\d+)', (m) => ({ record: logView(usage.find((r) => r.id === Number(m[1])) ?? fail(404, 'record not found')) }))
on('GET', '/usage/retention', () => retentionView())
on('PUT', '/usage/retention', (_m, _q, b) => {
  const days = Number(b.days)
  if (!(days >= 32 && days <= 3650)) fail(400, 'days must be between 32 and 3650')
  retention = { enabled: !!b.enabled, days }
  return retentionView()
})
on('POST', '/usage/retention/run', () => {
  lastPurge = { at: Date.now(), days: retention.days, deleted: 0, duration_ms: between(5, 30), manual: true }
  return retentionView()
})

on('GET', '/settings', (): SettingsResponse => ({ settings: tierSettings, tiers: catalog.tiers, families: catalog.families }))
on('PUT', '/settings', (_m, _q, b): SettingsResponse => {
  tierSettings = { default_tier: String(b.default_tier ?? 'medium'), model_tiers: (b.model_tiers as Record<string, string>) ?? {} }
  return { settings: tierSettings, tiers: catalog.tiers, families: catalog.families }
})
on('GET', '/settings/scheduling', (): SchedulingResponse => ({ settings: scheduling, defaults: schedulingDefaults }))
on('PUT', '/settings/scheduling', (_m, _q, b): SchedulingResponse => {
  scheduling = { ...scheduling, ...(b as Partial<Scheduling>) }
  scheduling.sticky = scheduling.sticky_mode !== 'off'
  return { settings: scheduling, defaults: schedulingDefaults }
})
on('GET', '/settings/client-filter', (): ClientFilterResponse => ({ settings: clientFilter, default_keywords: catalog.default_keywords }))
on('PUT', '/settings/client-filter', (_m, _q, b): ClientFilterResponse => {
  clientFilter = { ...clientFilter, ...(b as Partial<ClientFilter>) }
  return { settings: clientFilter, default_keywords: catalog.default_keywords }
})
on('GET', '/settings/system-prompt', (): SystemPromptResponse => ({ settings: systemPrompt, builtin: BUILTIN_SYSTEM_PROMPT }))
on('PUT', '/settings/system-prompt', (_m, _q, b): SystemPromptResponse => {
  const next = { ...systemPrompt, ...(b as Partial<SystemPrompt>) }
  if (!['prepend', 'replace', 'as_user'].includes(next.mode)) fail(400, 'mode must be prepend, replace or as_user')
  systemPrompt = next
  return { settings: systemPrompt, builtin: BUILTIN_SYSTEM_PROMPT }
})
on('GET', '/settings/request-handling', (): RequestHandlingResponse => ({ settings: requestHandling }))
on('PUT', '/settings/request-handling', (_m, _q, b): RequestHandlingResponse => {
  requestHandling = { ...requestHandling, ...(b as Partial<RequestHandling>) }
  return { settings: requestHandling }
})

on('GET', '/prices', () => prices)
on('POST', '/prices/sync', async () => {
  await sleep(between(500, 1000))
  const now = Date.now()
  prices.status.attempt_at = now
  for (const s of prices.status.sources ?? []) s.success_at = now
  return prices
})
on('PUT', '/prices/(.+)', (m, _q, b) => {
  const model = decodeURIComponent(m[1])
  const row = prices.models.find((r) => r.model === model) ?? fail(404, 'model not found')
  const cache = b.cache_read === null || b.cache_read === undefined ? undefined : Number(b.cache_read)
  row.price = { model, source: 'manual', price: { input: Number(b.input) || 0, output: Number(b.output) || 0, ...(cache === undefined ? {} : { cache_read: cache }) }, updated_at: Date.now() }
  return prices
})
on('DELETE', '/prices/(.+)', (m) => {
  const row = prices.models.find((r) => r.model === decodeURIComponent(m[1])) ?? fail(404, 'model not found')
  row.price = row.official
  return prices
})

// demoRequest 应答一个管理端请求：口令不是 demo 时返回 401，和真实服务一样；其它错误返回 { error }。
// 稍等一小会儿再返回，加载状态和真实环境看起来一致。
export async function demoRequest(method: string, path: string, body: unknown, password: string): Promise<DemoResponse> {
  await sleep(between(60, 180))
  if (password !== DEMO_PASSWORD) return { status: 401, data: { error: 'invalid admin password' } }
  const url = new URL(path, 'http://demo.local')
  const sub = url.pathname.replace(/^\/api\/admin/, '')
  for (const [m, re, handler] of routes) {
    if (m !== method) continue
    const match = sub.match(re)
    if (!match) continue
    try {
      return { status: 200, data: structuredClone(await handler(match, url.searchParams, (body ?? {}) as Body)) }
    } catch (e) {
      if (e instanceof DemoError) return { status: e.status, data: { error: e.message } }
      throw e
    }
  }
  return { status: 404, data: { error: `演示站没有模拟这个接口：${method} ${url.pathname}` } }
}
