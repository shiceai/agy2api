import type { Proxy, ProxyChoice } from '@/api/client'
import { fmtAgo } from '@/lib/utils'

// egressText 是出口的简写，如 203.0.113.9 · JP；还没测试成功过为空串。账号页、选代理的下拉框用。
export function egressText(ip: string, country: string): string {
  if (!ip) return ''
  return country ? `${ip} · ${country}` : ip
}

// proxyAddress 是不含用户名密码的代理地址，如 socks5h://10.0.0.2:1080（有没有密码看 username）。
export function proxyAddress(p: Proxy): string {
  return `${p.scheme}://${p.host}`
}

// countryName 把两位国家代码换成中文名（如 US → 美国），浏览器不支持时原样返回。
let regionNames: Intl.DisplayNames | null | undefined
export function countryName(code: string): string {
  if (!code) return ''
  if (regionNames === undefined) {
    try {
      regionNames = new Intl.DisplayNames(['zh-CN'], { type: 'region' })
    } catch {
      regionNames = null
    }
  }
  try {
    return regionNames?.of(code.toUpperCase()) ?? code
  } catch {
    return code
  }
}

// regionText 是出口地区：国家中文名加城市（没有城市时用地区），如"美国 · New York City"。
export function regionText(p: Proxy): string {
  const country = countryName(p.egress_country)
  const place = p.egress_city || p.egress_region
  return [country, place].filter(Boolean).join(' · ')
}

// latencyTone 按延迟快慢上色：300 ms 以内绿，1 秒以内橙，更慢红。
export function latencyTone(ms: number): string {
  if (ms <= 300) return 'text-emerald-700 dark:text-emerald-400'
  if (ms <= 1000) return 'text-amber-700 dark:text-amber-400'
  return 'text-red-600 dark:text-red-400'
}

// 出口变化后提示多久。
const changedRecently = 7 * 86400_000

export interface ProxyIssue {
  tone: 'warning' | 'danger' | 'muted'
  label: string
  tip?: string
}

// sameEgress 按出口 IP 分组，用来提示几个代理出口相同。
export function sameEgress(list: Proxy[]): Map<string, Proxy[]> {
  const m = new Map<string, Proxy[]>()
  for (const p of list) {
    if (!p.egress_ip) continue
    m.set(p.egress_ip, [...(m.get(p.egress_ip) ?? []), p])
  }
  return m
}

// proxyIssue 是代理需要留意的情况：未测试、测试失败、出口变了（7 天内）、和别的代理出口相同；正常时为 null。
// 启停另外用状态标签显示。
export function proxyIssue(p: Proxy, groups: Map<string, Proxy[]>): ProxyIssue | null {
  if (!p.checked_at) return { tone: 'muted', label: '未测试' }
  if (p.check_error) return { tone: 'danger', label: '测试失败', tip: p.check_error }
  if (p.prev_egress_ip && Date.now() - p.egress_changed_at < changedRecently) {
    return { tone: 'warning', label: '出口变了', tip: `${fmtAgo(p.egress_changed_at)}从 ${p.prev_egress_ip} 变成 ${p.egress_ip}` }
  }
  const same = (groups.get(p.egress_ip) ?? []).filter((x) => x.id !== p.id)
  if (same.length) return { tone: 'warning', label: '出口重复', tip: `和 ${same.map((x) => x.name).join('、')} 出口相同` }
  return null
}

// 代理下拉框的值：auto 自动分配空闲代理，none 直连，p:<ID> 指定代理。
export function choiceOf(value: string): ProxyChoice {
  if (value === 'auto' || value === 'none') return { mode: value }
  return { mode: 'proxy', proxy_id: Number(value.slice(2)) }
}
export const proxyValue = (id: number) => `p:${id}`

// freeProxies 是能自动分配的代理数：启用、没有账号绑定、最近一次测试没失败的（和服务端一致）。
export const freeProxies = (list: Proxy[]) =>
  list.filter((p) => p.status === 'enabled' && !p.accounts.length && !p.check_error).length
