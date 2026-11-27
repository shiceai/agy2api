<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, CircleAlert, Copy, Download } from '@lucide/vue'
import { api, type UsageLog } from '@/api/client'
import { copyText } from '@/lib/clipboard'
import { fmtDuration, fmtNum, fmtSpan, fmtTime } from '@/lib/utils'
import { affinityText, PROBE_KEY_ID, PROBE_KEY_LABEL, rateOf, reasoningClass, reasoningText, resultLabel } from '@/lib/usage'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import HttpStatus from '@/components/HttpStatus.vue'
import LatencyBreakdown from './LatencyBreakdown.vue'
import TokenBar from './TokenBar.vue'
import CostBreakdown from './CostBreakdown.vue'
import { fmtCost } from '@/lib/cost'
import BaseDialog from '@/components/BaseDialog.vue'

// 用量记录详情弹窗：打开时按 ID 取完整记录。顶部是摘要（状态、模型、类型、推理强度）和四个关键数字，
// 失败时紧接着给出失败原因；下面按模型、来源、耗时与 Token、尝试记录、上游标识分块，统一"左标签右值"。
// 可下载这条记录的 JSON。
const id = defineModel<number | null>({ required: true })
const record = ref<UsageLog | null>(null)
const error = ref('')

// 只在打开新记录时清空：关闭时保留内容，淡出动画里不会变成空壳闪一下。
watch(id, async (v) => {
  if (v == null) return
  record.value = null
  error.value = ''
  try {
    const res = await api.get<{ record: UsageLog }>(`/api/admin/usage/logs/${v}`)
    if (id.value === v) record.value = res.record
  } catch (e) {
    error.value = (e as Error).message
  }
})

const stats = computed(() => {
  const r = record.value
  if (!r) return []
  const rate = rateOf(r)
  return [
    { label: '总耗时', value: fmtDuration(r.latency_ms) },
    { label: '首字', value: r.first_token_ms != null ? fmtDuration(r.first_token_ms) : '—' },
    { label: 'Token', value: fmtNum(r.total_tokens) },
    { label: '速率', value: rate.text, tone: rate.tone, note: rate.note },
    { label: '费用', value: fmtCost(r.cost_nanos) },
  ]
})

interface Field {
  label: string
  value: string
  muted?: string // 跟在值后面的灰色补充，如账号 ID
  wide?: boolean // 占满一行
  mono?: boolean
}
// inboundAPI 描述客户端调用的接口：Anthropic 的 /v1/messages、OpenAI 的 /v1/chat/completions 和 /v1/responses，
// 或 Gemini 的方法名（假流式时上游实际用非流式）。
function inboundAPI(r: UsageLog): string {
  if (r.protocol === 'anthropic') return r.stream ? 'Anthropic /v1/messages（流式）' : 'Anthropic /v1/messages'
  if (r.protocol === 'openai') return r.stream ? 'OpenAI /v1/chat/completions（流式）' : 'OpenAI /v1/chat/completions'
  if (r.protocol === 'openai_responses') return r.stream ? 'OpenAI /v1/responses（流式）' : 'OpenAI /v1/responses'
  if (r.detail?.fake_stream) return 'Gemini streamGenerateContent（上游用非流式）'
  return r.stream ? 'Gemini streamGenerateContent' : 'Gemini generateContent'
}

const sections = computed(() => {
  const r = record.value
  if (!r) return []
  const account: Field = r.account_email
    ? { label: '账号', value: r.account_email, muted: `#${r.account_id}` }
    : { label: '账号', value: '', muted: r.account_id ? `已删除 #${r.account_id}` : '—' }
  const keyName = r.api_key_name || (r.api_key_prefix ? `${r.api_key_prefix}…` : '')
  const key: Field = keyName
    ? { label: '密钥', value: keyName, muted: `#${r.api_key_id}` }
    : r.api_key_id === PROBE_KEY_ID
      ? { label: '密钥', value: '', muted: `${PROBE_KEY_LABEL}（管理端）` }
      : { label: '密钥', value: '', muted: `已删除 #${r.api_key_id}` }
  return [
    {
      title: '模型',
      fields: [
        { label: '请求模型', value: r.request_model },
        { label: '实际发出', value: r.resolved_model || '—' },
        { label: '上游返回', value: r.upstream_model || '—' },
        { label: '接口', value: inboundAPI(r) },
      ] as Field[],
    },
    {
      title: '来源',
      fields: [
        account,
        ...(r.detail?.affinity ? [{ label: '会话', ...affinityText(r.detail.affinity) }] : []),
        key,
        { label: '客户端 IP', value: r.client_ip || '—' },
        { label: 'User-Agent', value: r.user_agent || '—', wide: true },
      ] as Field[],
    },
  ]
})

const identifiers = computed(() => {
  const d = record.value?.detail
  return [
    { label: '上游响应 ID', value: d?.response_id ?? '' },
    { label: 'trace ID', value: d?.trace_id ?? '' },
  ].filter((it) => it.value)
})

const copied = ref('')
async function copy(label: string, value: string) {
  if (!(await copyText(value))) return
  copied.value = label
  setTimeout(() => copied.value === label && (copied.value = ''), 1500)
}

function download() {
  const r = record.value
  if (!r) return
  const blob = new Blob([JSON.stringify(r, null, 2)], { type: 'application/json' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `usage-${r.request_id || r.id}.json`
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <BaseDialog :open="id != null" title="调用详情" size="2xl" @update:open="(v) => !v && (id = null)">
    <template #description>
      <span class="flex min-h-6 flex-wrap items-center gap-x-2">
        <template v-if="record">
          <span class="tabular-nums">{{ fmtTime(record.created_at) }}</span>
          <template v-if="record.request_id">
            <span aria-hidden="true">·</span>
            <span>请求 ID</span>
            <span class="font-mono text-xs">{{ record.request_id }}</span>
            <Button variant="ghost" size="icon-xs" aria-label="复制请求 ID" @click="copy('request', record.request_id)">
              <Check v-if="copied === 'request'" /><Copy v-else />
            </Button>
          </template>
        </template>
        <template v-else>{{ error ? '加载失败' : '加载中…' }}</template>
      </span>
    </template>

    <div>
      <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
      <div v-else-if="!record" class="h-40" />
      <template v-else>
        <!-- 摘要 -->
        <div class="flex flex-wrap items-center gap-2">
          <HttpStatus :code="record.status_code" />
          <span class="text-base font-semibold wrap-anywhere">{{ record.request_model }}</span>
          <Badge variant="outline" class="text-muted-foreground">{{ record.detail?.fake_stream ? '假流式' : record.stream ? '流式' : '非流式' }}</Badge>
          <Badge v-if="record.reasoning" variant="outline" :class="reasoningClass(record.reasoning)">
            {{ reasoningText(record.reasoning) }}
          </Badge>
        </div>
        <dl class="mt-3 grid grid-cols-3 gap-4 rounded-lg bg-muted/50 px-4 py-3 sm:grid-cols-5">
          <div v-for="s in stats" :key="s.label" :title="s.note || undefined">
            <dt class="text-xs text-muted-foreground">{{ s.label }}</dt>
            <dd :class="['mt-0.5 text-base font-semibold tabular-nums', s.tone]">{{ s.value }}</dd>
          </div>
        </dl>

        <Alert v-if="record.detail?.error" variant="destructive" class="mt-4">
          <CircleAlert />
          <AlertTitle class="flex flex-wrap items-baseline gap-x-2">
            {{ resultLabel(record.detail.error.kind) }}
            <span v-if="record.detail.error.reason" class="font-mono text-xs font-normal">{{ record.detail.error.reason }}</span>
          </AlertTitle>
          <AlertDescription class="wrap-anywhere">{{ record.detail.error.message || '上游没有给出说明' }}</AlertDescription>
        </Alert>

        <div class="mt-5 divide-y border-t">
          <section v-for="sec in sections" :key="sec.title" class="py-4 last:pb-0">
            <h3 class="mb-2.5 text-sm font-semibold">{{ sec.title }}</h3>
            <dl class="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
              <div v-for="f in sec.fields" :key="f.label" :class="['flex min-w-0 gap-3', f.wide && 'sm:col-span-2']">
                <dt class="w-20 shrink-0 text-muted-foreground">{{ f.label }}</dt>
                <dd class="min-w-0 wrap-anywhere">
                  {{ f.value }}<span v-if="f.muted" :class="['text-muted-foreground', f.value && 'ml-1.5']">{{ f.muted }}</span>
                </dd>
              </div>
            </dl>
          </section>

          <section class="grid gap-x-8 gap-y-5 py-4 last:pb-0 sm:grid-cols-2">
            <div>
              <h3 class="mb-2.5 text-sm font-semibold">耗时</h3>
              <LatencyBreakdown :record="record" />
            </div>
            <div>
              <h3 class="mb-2.5 text-sm font-semibold">Token</h3>
              <TokenBar :record="record" />
            </div>
          </section>

          <section class="py-4 last:pb-0">
            <h3 class="mb-2.5 text-sm font-semibold">费用 <span class="font-normal text-muted-foreground">按官方价估算，不扣费</span></h3>
            <CostBreakdown :record="record" class="sm:max-w-80" />
          </section>

          <section v-if="record.detail?.attempts?.length" class="py-4 last:pb-0">
            <h3 class="mb-2 text-sm font-semibold">尝试记录</h3>
            <div class="overflow-x-auto">
              <table class="w-full text-xs whitespace-nowrap tabular-nums">
                <thead class="text-muted-foreground">
                  <tr class="border-b">
                    <th class="py-1.5 pr-3 text-left font-normal">#</th>
                    <th class="py-1.5 pr-3 text-left font-normal">账号</th>
                    <th class="py-1.5 pr-3 text-left font-normal">模型</th>
                    <th class="py-1.5 pr-3 text-left font-normal">结果</th>
                    <th class="py-1.5 pr-3 text-left font-normal">状态码</th>
                    <th class="py-1.5 pr-3 text-right font-normal">冷却</th>
                    <th class="py-1.5 text-right font-normal">耗时</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="a in record.detail.attempts" :key="a.try" class="border-b last:border-0" :title="a.error || ''">
                    <td class="py-1.5 pr-3">{{ a.try }}</td>
                    <td class="max-w-48 truncate py-1.5 pr-3">{{ a.account || `#${a.account_id}` }}</td>
                    <td class="py-1.5 pr-3">{{ a.model }}</td>
                    <td
                      class="py-1.5 pr-3"
                      :class="a.result === 'success' ? 'text-emerald-700 dark:text-emerald-400' : 'text-destructive'"
                    >
                      {{ resultLabel(a.result) }}
                    </td>
                    <td class="py-1.5 pr-3">{{ a.status || '—' }}</td>
                    <td class="py-1.5 pr-3 text-right">{{ a.cooldown_ms ? fmtSpan(a.cooldown_ms) : '—' }}</td>
                    <td class="py-1.5 text-right">{{ fmtDuration(a.duration_ms) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>

          <section v-if="identifiers.length" class="py-4 last:pb-0">
            <h3 class="mb-2 text-sm font-semibold">上游标识</h3>
            <dl class="space-y-1 text-sm">
              <div v-for="it in identifiers" :key="it.label" class="flex min-w-0 items-center gap-3">
                <dt class="w-20 shrink-0 text-muted-foreground">{{ it.label }}</dt>
                <dd class="min-w-0 flex-1 truncate font-mono text-xs">{{ it.value }}</dd>
                <Button variant="ghost" size="icon-xs" class="text-muted-foreground" :aria-label="`复制${it.label}`" @click="copy(it.label, it.value)">
                  <Check v-if="copied === it.label" /><Copy v-else />
                </Button>
              </div>
            </dl>
          </section>
        </div>
      </template>
    </div>

    <template #footer>
      <Button variant="outline" :disabled="!record" @click="download"><Download />下载 JSON</Button>
      <Button @click="id = null">关闭</Button>
    </template>
  </BaseDialog>
</template>
