import type { UsageAffinity, UsageRecord } from '@/api/client'

// 用量和冷却里的结果分类对应的中文名：后端 antigravity.ErrorKind（上游错误），加上 success、transport，
// 以及没拿到上游响应的失败（relay 的 Kind* 常量）和流式中途断开。
export const resultLabels: Record<string, string> = {
  success: '成功',
  transport: '连接失败',
  proxy: '代理故障',
  unauthorized: '令牌失效',
  tos_violation: '条款封禁',
  validation_required: '需要验证',
  model_forbidden: '无权使用模型',
  forbidden: '无权访问',
  quota_exhausted: '额度耗尽',
  rate_limit: '瞬时限流',
  model_capacity: '容量不足',
  server_error: '上游 5xx',
  client_error: '请求错误',
  no_account: '无可用账号',
  model_not_found: '模型不存在',
  canceled: '客户端取消',
  key_quota: '密钥额度用完',
  model_not_allowed: '密钥不允许该模型',
  client_filtered: '客户端名称被拦截',
  remote_image: '图片链接处理失败',
  stream_idle: '流式空闲超时',
  response_too_large: '响应过大',
}

export function resultLabel(kind: string): string {
  return resultLabels[kind] ?? kind
}

// User-Agent 的第一段（产品名/版本），如 "Python-urllib/3.15"；表格里放不下整串。
export function uaShort(ua: string): string {
  return ua.trim().split(/\s+/)[0] ?? ''
}

// 推理强度：档位原样显示；"budget:N" 显示为思考预算。
export function reasoningText(v: string): string {
  if (!v.startsWith('budget:')) return v
  const n = Number(v.slice('budget:'.length))
  if (n < 0) return '动态'
  return n === 0 ? '不思考' : `预算 ${n.toLocaleString()}`
}
export function reasoningClass(v: string): string {
  const tones: Record<string, string> = {
    low: 'border-border text-muted-foreground',
    medium: 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300',
    high: 'border-violet-500/30 bg-violet-500/10 text-violet-700 dark:text-violet-300',
    xhigh: 'border-orange-500/30 bg-orange-500/10 text-orange-700 dark:text-orange-300',
  }
  return tones[v] ?? 'border-border text-foreground'
}

export function isOK(code: number): boolean {
  return code >= 200 && code < 300
}

// 生成耗时：流式按总耗时减首字延迟，非流式没有首字延迟，按总耗时。
export function genMs(r: UsageRecord): number {
  return r.first_token_ms != null ? r.latency_ms - r.first_token_ms : r.latency_ms
}

// 速率 = (输出 + 推理) ÷ 生成耗时，按快慢上色：≥ 30 tok/s 绿，10～30 橙，< 10 红。
// 输出加推理不足 20 个 token 时不评价（输出太短，速率主要被固定开销拉低），显示灰色。出错或没有输出时不算。
const rateFast = 30
const rateSlow = 10
const rateMinTokens = 20
export interface Rate {
  text: string
  tone: string
  note: string
}
export function rateOf(r: UsageRecord): Rate {
  const tokens = r.output_tokens + (r.thoughts_tokens ?? 0)
  const ms = genMs(r)
  if (!isOK(r.status_code) || tokens <= 0 || ms <= 0) return { text: '—', tone: 'text-muted-foreground', note: '' }
  const v = (tokens / ms) * 1000
  const text = `${v >= 100 ? Math.round(v) : v.toFixed(1)} tok/s`
  if (tokens < rateMinTokens) return { text, tone: 'text-muted-foreground', note: `输出太少（${tokens} 个 token），速率不具参考性` }
  if (v >= rateFast) return { text, tone: 'text-emerald-700 dark:text-emerald-400', note: `快（≥ ${rateFast} tok/s）` }
  if (v >= rateSlow) return { text, tone: 'text-amber-700 dark:text-amber-400', note: `一般（${rateSlow}～${rateFast} tok/s）` }
  return { text, tone: 'text-red-600 dark:text-red-400', note: `慢（< ${rateSlow} tok/s）` }
}

// 管理端的账号检测记进用量时密钥 ID 为 0（真实的 Key 从 1 开始），显示为"账号检测"。
export const PROBE_KEY_ID = 0
export const PROBE_KEY_LABEL = '账号检测'

// 会话粘性换号的原因（原账号这次为什么不能接）。
const affinityReasons: Record<string, string> = {
  unavailable: '原账号不可用（停用、删除、代理停用，或不支持这个模型）',
  failed: '原账号这次请求失败，换号重试',
  paused: '原账号的代理连不上',
  cooldown: '原账号这个模型在冷却',
  busy: '原账号并发已满',
  low_quota: '原账号这个模型的额度低于避让阈值',
}

// affinityText 是用量详情里"会话"一行：沿用上次的账号、换了账号（原因）、新对话。
export function affinityText(a: UsageAffinity): { value: string; muted?: string } {
  if (a.result === 'hit') return { value: '沿用上次的账号' }
  if (a.result === 'new') return { value: '新对话' }
  const from = a.from_account || (a.from_account_id ? `#${a.from_account_id}` : '')
  return { value: `换了账号：${affinityReasons[a.reason ?? ''] ?? '原账号不能接'}`, muted: from ? `原 ${from}` : undefined }
}
