import type { ModelInfo } from '@/api/client'

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
  return (models ?? []).filter((m) => m.remaining_fraction != null && !!m.reset_time)
}

export interface QuotaGroup {
  label: string
  // 组内最低的剩余额度，组里没有带额度的模型时为 null。
  remaining: number | null
}

// 表格里的两条：Gemini 一组，Claude 和 GPT-OSS 一组（上游给它们同一个额度窗口），各取组内最低的剩余额度。
const groups = [
  { label: 'Gemini', match: (id: string) => id.startsWith('gemini') },
  { label: 'Claude', match: (id: string) => id.startsWith('claude') || id.startsWith('gpt') },
]
export function quotaGroups(models: ModelInfo[]): QuotaGroup[] {
  const list = metered(models)
  return groups.map((g) => {
    const values = list.filter((m) => g.match(m.id)).map((m) => m.remaining_fraction as number)
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
    const toWindow = (w: typeof main, label: string): QuotaWindow => ({
      key: w.key,
      label,
      names: w.names,
      count: w.models.length,
      remaining: w.remaining,
      reset: w.reset,
    })
    out.push(toWindow(main, groupLabel(group, main.models, main.names)))
    for (const w of rest) out.push(toWindow(w, shortNames(w.names, group === 'other' ? '' : group)))
  }
  return out
}

// 模型系列名：显示名去掉"(High)"这类档位后缀，没有显示名用 ID，去重。"(Argon)"这类不是档位的保留。
function families(models: ModelInfo[]): string[] {
  const names = new Set<string>()
  for (const m of models) {
    names.add((m.display_name || m.id).replace(/\s*\((minimal|low|medium|high|xhigh)\)\s*$/i, '').trim())
  }
  return [...names].sort()
}
