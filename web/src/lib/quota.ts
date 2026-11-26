import type { Account, ModelInfo, QuotaBucket, QuotaSummary } from '@/api/client'

// 额度条的严重程度：快用完（<20%）红，偏低（<50%）黄，其余蓝。
export type Severity = 'ok' | 'warning' | 'critical'
export function severity(remaining: number): Severity {
  if (remaining < 0.2) return 'critical'
  if (remaining < 0.5) return 'warning'
  return 'ok'
}
export const severityColor: Record<Severity, string> = {
  ok: 'var(--chart-success)',
  warning: 'var(--chart-warning)',
  critical: 'var(--chart-failure)',
}

// 剩余比例的百分比写法：向下取整，避免没用完显示 100%；不到 1% 但没用完显示 <1%。
export function fmtPercent(remaining: number): string {
  if (remaining > 0 && remaining < 0.01) return '<1%'
  return `${Math.floor(remaining * 100)}%`
}

// 有额度窗口的模型：带剩余比例和重置时间。没有重置时间的（如补全用的 tab_* 模型）不限额，不计。
function metered(models: ModelInfo[]): ModelInfo[] {
  return (models ?? []).filter((m) => m.remaining_fraction != null && !!m.reset_time && !isPlaceholder(m.id))
}

// 上游的占位条目：模型对这个账号不开放时，目录里只放一个 <家族>-disabled（如 gemini-4-carbon-disabled），
// 它不是能用的模型（后端同样忽略它，见 antigravity.IsUnavailablePlaceholder）。
export function isPlaceholder(id: string): boolean {
  return /(^|[-_])disabled($|[-_])/i.test(id)
}

// unavailableModels 是这个账号不能用的模型名（占位条目的显示名），去重排序。
export function unavailableModels(models: ModelInfo[]): string[] {
  const names = new Set<string>()
  for (const m of models ?? []) {
    if (isPlaceholder(m.id)) names.add(m.display_name || m.id.replace(/[-_]disabled$/i, ''))
  }
  return [...names].sort()
}

// quotaStale 报告额度是不是可能已过期：停用的账号不再刷新；后台刷新开着时超过两个间隔（再宽限 5 分钟）没更新，
// 关着时超过一天没更新。quotaRefresh 是刷新间隔（秒）。
export function quotaStale(account: Account, quotaRefresh: number): boolean {
  if (!account.models_refreshed_at) return false
  if (account.status !== 'enabled') return true
  const limit = quotaRefresh > 0 ? quotaRefresh * 2000 + 5 * 60_000 : 24 * 3600_000
  return Date.now() - account.models_refreshed_at > limit
}

export interface QuotaGroup {
  label: string
  // 组内最低的剩余额度（拉到了每周额度时也算上），组里没有带额度的模型时为 null。
  remaining: number | null
}

// 表格里的两条：Gemini 一组，Claude 和 GPT-OSS 一组（上游给它们同一个额度窗口），各取组内最低的剩余额度。
const groups = [
  { label: 'Gemini', match: (id: string) => id.startsWith('gemini') },
  { label: 'Claude', match: (id: string) => id.startsWith('claude') || id.startsWith('gpt') },
]
export function quotaGroups(models: ModelInfo[], summary?: QuotaSummary | null): QuotaGroup[] {
  const list = metered(models)
  const sections = summarySections(summary)
  return groups.map((g) => {
    const values = list.filter((m) => g.match(m.id)).map((m) => m.remaining_fraction as number)
    for (const sec of sections) {
      if (summaryGroupOf(sec.label) !== g.label.toLowerCase()) continue
      for (const r of sec.rows) if (r.remaining != null) values.push(r.remaining)
    }
    return { label: g.label, remaining: values.length ? Math.min(...values) : null }
  })
}

// 额度窗口：剩余比例和重置时间都相同的模型算同一个窗口（上游不直接给窗口，只能这样推）。
// Gemini 和 Claude·GPT 已知是不同的窗口，先按组分开：没用过的窗口都是 100%，重置时间也可能相同，不分组会合到一起。
export interface QuotaWindow {
  key: string
  label: string
  // 窗口里的模型系列名（显示名去掉档位后缀），展开时列出。
  names: string[]
  count: number
  remaining: number
  reset: number // Unix 毫秒
  // group 是所在的组；main 是组里模型最多的窗口（拉到每周额度时由上游的分组代替）。
  group: Group
  main: boolean
}

type Group = 'gemini' | 'claude' | 'other'
const groupOrder: Group[] = ['gemini', 'claude', 'other']
function groupOf(id: string): Group {
  if (id.startsWith('gemini')) return 'gemini'
  if (id.startsWith('claude') || id.startsWith('gpt')) return 'claude'
  return 'other'
}

// 组名：Gemini；Claude 和 GPT-OSS 共用窗口，按实际包含的写；其它按系列名。
function groupLabel(group: Group, models: ModelInfo[], names: string[]): string {
  if (group === 'gemini') return 'Gemini'
  if (group === 'claude') {
    const claude = models.some((m) => m.id.startsWith('claude'))
    const gpt = models.some((m) => m.id.startsWith('gpt'))
    return claude && gpt ? 'Claude · GPT' : claude ? 'Claude' : 'GPT-OSS'
  }
  return shortNames(names)
}

// 同组有多个窗口时，模型最多的叫组名，其余用它们自己的系列名区分（如 "Gemini 4 Argon"），
// 优先取以组名开头的系列（跳过 Orchestrator 这类内部模型），最多两个。
function shortNames(names: string[], prefix = ''): string {
  const main = prefix ? names.filter((n) => n.toLowerCase().startsWith(prefix)) : names
  const list = main.length ? main : names
  return list.length > 2 ? `${list.slice(0, 2).join(' · ')} 等` : list.join(' · ')
}

// 按组排（Gemini、Claude·GPT、其它），组内模型最多的窗口在前，其余按剩余从低到高。
export function quotaWindows(models: ModelInfo[]): QuotaWindow[] {
  const buckets = new Map<string, { group: Group; models: ModelInfo[] }>()
  for (const m of metered(models)) {
    const group = groupOf(m.id)
    const key = `${group}|${m.remaining_fraction}|${m.reset_time}`
    const b = buckets.get(key) ?? { group, models: [] }
    b.models.push(m)
    buckets.set(key, b)
  }
  const out: QuotaWindow[] = []
  for (const group of groupOrder) {
    const list = [...buckets.entries()]
      .filter(([, b]) => b.group === group)
      .map(([key, b]) => ({
        key,
        models: b.models,
        names: families(b.models),
        remaining: b.models[0].remaining_fraction as number,
        reset: Date.parse(b.models[0].reset_time as string),
      }))
      .sort((a, b) => b.models.length - a.models.length)
    const [main, ...rest] = list
    if (!main) continue
    rest.sort((a, b) => a.remaining - b.remaining || a.reset - b.reset)
    const toWindow = (w: typeof main, label: string, isMain: boolean): QuotaWindow => ({
      key: w.key,
      label,
      names: w.names,
      count: w.models.length,
      remaining: w.remaining,
      reset: w.reset,
      group,
      main: isMain,
    })
    out.push(toWindow(main, groupLabel(group, main.models, main.names), true))
    for (const w of rest) out.push(toWindow(w, shortNames(w.names, group === 'other' ? '' : group), false))
  }
  return out
}

// 模型系列名：有官方分组信息时只取对外列出的（家族用家族名，不含自动分档、监控这类内部变体），
// 否则显示名去掉"(High)"这类档位后缀，没有显示名用 ID，去重。"(Argon)"这类不是档位的保留。
function families(models: ModelInfo[]): string[] {
  const listed = new Set<string>()
  for (const m of models) {
    if (m.listed) listed.add(m.family_display_name || m.display_name || m.id)
  }
  if (listed.size) return [...listed].sort()
  const names = new Set<string>()
  for (const m of models) {
    names.add((m.display_name || m.id).replace(/\s*\((minimal|low|medium|high|xhigh)\)\s*$/i, '').trim())
  }
  return [...names].sort()
}

// --- 每周额度（开了每周额度的账号拉到的分组额度，见 antigravity.QuotaSummary）---

export interface QuotaRow {
  key: string
  label: string
  // window 是上游的窗口名（5h、weekly）。选号避让只看 5 小时窗口。
  window: string
  remaining: number | null
  reset: number // Unix 毫秒，没有时为 NaN
}
export interface QuotaSection {
  key: string
  label: string
  // names 是组里的模型名，展开时列出。
  names: string[]
  rows: QuotaRow[]
}

const windowLabels: Record<string, string> = { '5h': '5 小时', weekly: '每周', daily: '每天' }
const windowOrder = ['5h', 'daily', 'weekly']

// 组名："Gemini Models" → Gemini，"Claude and GPT models" → Claude · GPT，其余原样。
function summaryLabel(name: string): string {
  return name.replace(/\s+models?$/i, '').replace(/\s+and\s+/gi, ' · ').trim() || name
}

// summaryGroupOf 把上游的组对应到表格里的两条（gemini、claude），对不上时为空串。
function summaryGroupOf(label: string): string {
  const l = label.toLowerCase()
  if (l.startsWith('gemini')) return 'gemini'
  if (l.includes('claude') || l.includes('gpt')) return 'claude'
  return ''
}

// 组说明里的模型名（"Models within this group: A, B"），对不上格式时用整句。
function describedModels(description?: string): string[] {
  if (!description) return []
  const m = description.match(/:\s*(.+)$/)
  return m
    ? m[1]
        .split(/,\s*/)
        .map((n) => n.trim())
        .filter(Boolean)
    : [description]
}

// 上游省略了值为 0 的比例（proto3）：有重置时间而没有比例时按用完算。
function bucketRemaining(b: QuotaBucket): number | null {
  if (b.remaining_fraction != null) return b.remaining_fraction
  return b.reset_time ? 0 : null
}

function windowRank(window?: string): number {
  const i = windowOrder.indexOf(window ?? '')
  return i < 0 ? windowOrder.length : i
}

// summarySections 把分组额度整理成每组一节、每个窗口一行（5 小时在前、每周在后）。没拉过时为空。
export function summarySections(summary?: QuotaSummary | null): QuotaSection[] {
  return (summary?.groups ?? [])
    .filter((g) => g.buckets?.length)
    .map((g, i) => {
      const label = summaryLabel(g.display_name || `第 ${i + 1} 组`)
      const rows = [...g.buckets]
        .sort((a, b) => windowRank(a.window) - windowRank(b.window))
        .map((b, j) => ({
          key: `${label}|${b.bucket_id || b.window || j}`,
          label: windowLabels[b.window ?? ''] ?? (b.display_name || b.window || '额度'),
          window: b.window ?? '',
          remaining: bucketRemaining(b),
          reset: b.reset_time ? Date.parse(b.reset_time) : NaN,
        }))
      return { key: `summary|${label}`, label, names: describedModels(g.description), rows }
    })
}

// activeSummary 是生效中的分组额度：账号关了每周额度时库里留着的旧数据不参与选号，也不显示。
export function activeSummary(account: Pick<Account, 'quota_summary' | 'weekly_quota_on'>): QuotaSummary | null {
  return account.weekly_quota_on ? account.quota_summary : null
}

// quotaSections 是额度弹窗和账号详情里的额度列表：拉到了每周额度时，上游的分组（每组 5 小时、每周两行）代替推算的
// 主窗口，推算出的其它窗口（如单独计算的 Gemini 4 Argon）照旧列在后面；没拉过时全是推算的窗口，每个一行。
export function quotaSections(account: Pick<Account, 'models' | 'quota_summary' | 'weekly_quota_on'>): QuotaSection[] {
  const windows = quotaWindows(account.models)
  const summary = summarySections(activeSummary(account))
  const toSection = (w: QuotaWindow): QuotaSection => ({
    key: w.key,
    label: w.label,
    names: w.names,
    rows: [{ key: w.key, label: w.label, window: '5h', remaining: w.remaining, reset: w.reset }],
  })
  if (!summary.length) return windows.map(toSection)
  const covered = new Set(summary.map((sec) => summaryGroupOf(sec.label)).filter(Boolean))
  const labels = new Set(summary.map((sec) => sec.label.toLowerCase()))
  const rest = windows.filter((w) => !(w.main && covered.has(w.group)) && !labels.has(w.label.toLowerCase()))
  return [...summary, ...rest.map(toSection)]
}
