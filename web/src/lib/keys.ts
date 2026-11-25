import type { ApiKey, TokenPeriod } from '@/api/client'

export const periodLabels: Record<TokenPeriod, string> = { day: '每天', month: '每月', total: '总量' }

// Token 数的紧凑写法：5M、1.2M、500K，不到 1000 原样。和输入框接受的写法一致。
export function fmtCompact(n: number): string {
  const units: [number, string][] = [
    [1e9, 'B'],
    [1e6, 'M'],
    [1e3, 'K'],
  ]
  for (const [v, u] of units) {
    if (n >= v) return `${Number((n / v).toFixed(n / v >= 100 ? 0 : 1))}${u}`
  }
  return String(n)
}

// 解析 Token 上限的输入：纯数字（可带千位逗号），或带 K、M、B 后缀（不分大小写，可带小数）。
// 空串返回 0（不限）；格式不对返回 null。
export function parseTokens(input: string): number | null {
  const s = input.trim().replace(/,/g, '')
  if (s === '') return 0
  const m = /^(\d+(?:\.\d+)?)\s*([kmb])?$/i.exec(s)
  if (!m) return null
  const mult = { k: 1e3, m: 1e6, b: 1e9 }[(m[2] ?? '').toLowerCase() as 'k' | 'm' | 'b'] ?? 1
  const v = Math.round(Number(m[1]) * mult)
  return Number.isSafeInteger(v) ? v : null
}

// Key 的综合状态：手动停用优先，其次已过期、额度用完。
export type KeyState = 'enabled' | 'disabled' | 'expired' | 'exhausted'
export function keyState(k: ApiKey): KeyState {
  if (k.status !== 'enabled') return 'disabled'
  if (k.expired) return 'expired'
  if (k.token_limit > 0 && k.period_used >= k.token_limit) return 'exhausted'
  return 'enabled'
}
export const keyStateBadge: Record<KeyState, { tone: 'success' | 'warning' | 'danger' | 'muted'; label: string }> = {
  enabled: { tone: 'success', label: '启用' },
  disabled: { tone: 'muted', label: '停用' },
  expired: { tone: 'muted', label: '已过期' },
  exhausted: { tone: 'warning', label: '额度用完' },
}

// 到期时间的口语写法：不过期、N 天后、已过期。
export function expiryText(k: Pick<ApiKey, 'expires_at'>): string {
  if (!k.expires_at) return '不过期'
  const left = k.expires_at - Date.now()
  return left <= 0 ? '已过期' : `${fmtLeft(left)}后`
}

// 剩余时长只取最大的一级单位（四舍五入）：分钟、小时、天。
export function fmtLeft(ms: number): string {
  const min = ms / 60_000
  if (min < 60) return `${Math.max(1, Math.ceil(min))} 分钟`
  const h = Math.round(min / 60)
  if (h < 24) return `${h} 小时`
  return `${Math.round(h / 24)} 天`
}
