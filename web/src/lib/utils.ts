// cn 合并 Tailwind 类名，用 shadcn 官方的 cn 包（clsx + tailwind-merge 的替代），
// 与 shadcn-vue 生成的组件一致。
export { cn } from 'cn'

// 格式化 Unix 毫秒为本地时间字符串。
export function fmtTime(ms: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleString()
}

// 格式化相对时间。
export function fmtAgo(ms: number): string {
  if (!ms) return '从未'
  const diff = Date.now() - ms
  const s = Math.floor(diff / 1000)
  if (s < 60) return `${s} 秒前`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分钟前`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时前`
  return `${Math.floor(h / 24)} 天前`
}

// 格式化计数：千位分隔。
export function fmtNum(n: number): string {
  return (n ?? 0).toLocaleString()
}

// 格式化 token 数：大数用 K、M 缩写，保留一位小数。
export function fmtTokens(n: number): string {
  const v = n ?? 0
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 10_000) return `${(v / 1000).toFixed(1)}K`
  return v.toLocaleString()
}

// 格式化耗时：1 秒以下用毫秒，以上用秒。
export function fmtDuration(ms: number): string {
  const v = ms ?? 0
  return v < 1000 ? `${v} ms` : `${(v / 1000).toFixed(1)} s`
}

// 紧凑的本地时间：今天只显示时分秒，否则带月日。
export function fmtShortTime(ms: number): string {
  if (!ms) return '—'
  const d = new Date(ms)
  const pad = (x: number) => String(x).padStart(2, '0')
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  if (d.toDateString() === new Date().toDateString()) return time
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${time}`
}

// 时长（毫秒）的口语写法，取最大的一到两级单位：秒、分钟、小时、天。用于冷却、剩余时间这类较长的时长。
export function fmtSpan(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000))
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分钟`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h} 小时 ${m % 60} 分` : `${h} 小时`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d} 天 ${h % 24} 小时` : `${d} 天`
}

// 距离某个时间点（Unix 毫秒）还剩多久。
export function fmtRemaining(until: number): string {
  return fmtSpan(until - Date.now())
}
