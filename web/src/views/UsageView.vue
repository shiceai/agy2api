<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { RefreshCw, X, Search, ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight, Database, FileText } from '@lucide/vue'
import { api, errorMessage, type UsageLog } from '@/api/client'
import { cn, fmtDuration, fmtNum, fmtShortTime, fmtTime, fmtTokens } from '@/lib/utils'
import { fmtCost } from '@/lib/cost'
import { stickyActionCell, stickyActionHead, useColumnPrefs, type ColumnDef } from '@/lib/columns'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { HoverCard, HoverCardContent, HoverCardTrigger } from '@/components/ui/hover-card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableEmpty, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import ColumnSettings from '@/components/ColumnSettings.vue'
import CopyId from '@/components/CopyId.vue'
import FilterButton from '@/components/FilterButton.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import IconAction from '@/components/IconAction.vue'
import LoadError from '@/components/LoadError.vue'
import LatencyBreakdown from '@/components/usage/LatencyBreakdown.vue'
import CostBreakdown from '@/components/usage/CostBreakdown.vue'
import AffinityIcon from '@/components/usage/AffinityIcon.vue'
import UsageDetailDialog from '@/components/usage/UsageDetailDialog.vue'
import UsageStatus from '@/components/usage/UsageStatus.vue'
import { genMs, PROBE_KEY_ID, PROBE_KEY_LABEL, rateOf, reasoningClass, reasoningText, uaShort } from '@/lib/usage'

type RangeKey = '1h' | '6h' | '24h' | '7d' | '30d'
type Dimension = 'model' | 'account' | 'apiKey'

const ranges: { value: RangeKey; label: string; ms: number }[] = [
  { value: '1h', label: '1 小时', ms: 3600_000 },
  { value: '6h', label: '6 小时', ms: 6 * 3600_000 },
  { value: '24h', label: '24 小时', ms: 24 * 3600_000 },
  { value: '7d', label: '7 天', ms: 7 * 86400_000 },
  { value: '30d', label: '30 天', ms: 30 * 86400_000 },
]
// 每页条数：默认 10（同 shadcn 数据表格），可选 10 / 20 / 50 / 100，记在浏览器本地；读写失败（隐私模式等）时用默认值。
const pageSizes = [10, 20, 50, 100]
const pageSizeKey = 'agy2api_usage_page_size'
function loadPageSize(): number {
  try {
    const v = Number(localStorage.getItem(pageSizeKey))
    return pageSizes.includes(v) ? v : 10
  } catch {
    return 10
  }
}
const pageSize = ref(loadPageSize())

// 宽屏表格的列；顺序和显示与否由用户在"显示列"里设置，存在浏览器本地。
// 默认顺序：先看调用本身（时间、状态、模型、推理强度），再看结果（Token、耗时、速率），最后是来源和标识。
const columnDefs: ColumnDef[] = [
  { key: 'time', label: '时间', defaultVisible: true },
  { key: 'status', label: '状态', defaultVisible: true },
  { key: 'model', label: '模型', defaultVisible: true },
  { key: 'protocol', label: '接口', defaultVisible: true },
  { key: 'reasoning', label: '推理强度', defaultVisible: true },
  { key: 'token', label: 'Token', defaultVisible: true },
  // 按官方价估算，悬停看算式。
  { key: 'cost', label: '费用', defaultVisible: true },
  // 流式显示"首字 / 总耗时"，非流式只有总耗时。
  { key: 'latency', label: '耗时', defaultVisible: true },
  { key: 'rate', label: '速率', defaultVisible: true },
  { key: 'account', label: '账号', defaultVisible: true },
  { key: 'apiKey', label: '密钥', defaultVisible: true },
  // 只显示前 8 位，悬停看完整，点击复制。实际发出、上游返回的模型在悬停模型名时看。
  { key: 'requestId', label: '请求 ID', defaultVisible: true },
  // IP、客户端默认隐藏，详情弹窗里都有。User-Agent 只显示第一段，悬停看完整。
  { key: 'ip', label: 'IP', defaultVisible: false },
  { key: 'client', label: '客户端', defaultVisible: false },
]
const rightAligned = new Set(['token', 'cost', 'latency', 'rate'])
// v3：调整默认列顺序、IP 和客户端默认隐藏，换键让已保存的设置按新默认重置一次。
const prefsKey = 'agy2api_usage_columns_v3'
const columnPrefs = useColumnPrefs(prefsKey, columnDefs)
const labelOf = Object.fromEntries(columnDefs.map((d) => [d.key, d.label]))

const route = useRoute()
const range = ref<RangeKey>('24h')
const logs = ref<UsageLog[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const error = ref('')

// 日志筛选。点表格里的模型、账号、密钥加筛选条件：不同字段之间叠加（并且），同一字段换值替换，
// 再点同一个值取消。label 用于筛选条显示可读名称。
interface DimFilter {
  id: number // 账号、密钥的 ID；模型为 0
  value: string // 模型名；账号、密钥为空
  label: string
}
const status = ref<'all' | 'success' | 'error'>('all')
const stream = ref<'all' | 'stream' | 'sync'>('all')
const protocol = ref<'all' | 'gemini' | 'anthropic' | 'openai' | 'openai_responses'>('all')
const dimFilters = ref<Partial<Record<Dimension, DimFilter>>>({})
const activeDims = computed(() => (['model', 'account', 'apiKey'] as const).filter((d) => dimFilters.value[d]))
const requestIdInput = ref('')
const requestId = ref('')
// 本次查询的起点：换时间范围或刷新时更新，翻页、筛选沿用，保证分页结果稳定。
const since = ref(0)

// 详情弹窗当前显示的记录 ID，null 表示关闭。
const detailId = ref<number | null>(null)

const pages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const hasFilter = computed(
  () => status.value !== 'all' || stream.value !== 'all' || protocol.value !== 'all' || activeDims.value.length > 0 || !!requestId.value,
)

function handle(e: unknown) {
  error.value = errorMessage(e)
}

// --- 展示 ---

// 账号、Key 已删除时只剩 ID，显示为"已删除 #ID"，表格里用灰色。
function accountLabel(r: UsageLog): string {
  if (r.account_email) return r.account_email
  return r.account_id ? `已删除 #${r.account_id}` : '—'
}
function keyLabel(r: UsageLog): string {
  if (r.api_key_name) return r.api_key_name
  if (r.api_key_prefix) return `${r.api_key_prefix}…`
  if (r.api_key_id === PROBE_KEY_ID) return PROBE_KEY_LABEL
  return `已删除 #${r.api_key_id}`
}
const accountGone = (r: UsageLog) => !r.account_email
const keyGone = (r: UsageLog) => !r.api_key_name && !r.api_key_prefix

// 普通输入 = 输入总数减去缓存读取（Gemini 的输入数包含缓存部分）。
function plainInput(r: UsageLog): number {
  return Math.max(0, r.prompt_tokens - (r.cached_tokens ?? 0))
}
function optTokens(v: number | null): string {
  return v == null ? '—' : fmtNum(v)
}

// --- 数据 ---

function logsQuery(): string {
  const q = new URLSearchParams({
    since: String(since.value),
    limit: String(pageSize.value),
    offset: String((page.value - 1) * pageSize.value),
  })
  if (status.value !== 'all') q.set('status', status.value)
  if (stream.value !== 'all') q.set('stream', stream.value)
  if (protocol.value !== 'all') q.set('protocol', protocol.value)
  if (requestId.value) q.set('request_id', requestId.value)
  const f = dimFilters.value
  if (f.model) q.set('model', f.model.value)
  if (f.account) q.set('account_id', String(f.account.id))
  if (f.apiKey) q.set('api_key_id', String(f.apiKey.id))
  return q.toString()
}

async function loadLogs() {
  loading.value = true
  try {
    const res = await api.get<{ records: UsageLog[]; total: number }>(`/api/admin/usage/logs?${logsQuery()}`)
    logs.value = res.records ?? []
    total.value = res.total ?? 0
    error.value = ''
    updatedAt.value = Date.now()
  } catch (e) {
    handle(e)
  } finally {
    loading.value = false
  }
}

// 刷新：按当前时间范围重新取起点再查。
function reload() {
  since.value = Date.now() - (ranges.find((r) => r.value === range.value)?.ms ?? 0)
  return loadLogs()
}
// 从别的页面跳过来时带的筛选（如 Key 详情的"在用量页查看"）：range、api_key_id 和显示用的 key_label。
function applyQuery() {
  const q = route.query
  if (typeof q.range === 'string' && ranges.some((r) => r.value === q.range)) range.value = q.range as RangeKey
  if (typeof q.api_key_id === 'string' && /^\d+$/.test(q.api_key_id)) {
    const id = Number(q.api_key_id)
    const label = typeof q.key_label === 'string' && q.key_label ? q.key_label : '#' + id
    dimFilters.value = { ...dimFilters.value, apiKey: { id, value: '', label } }
  }
}
onMounted(() => {
  applyQuery()
  reload()
})

// 刷新按钮：本地请求几十毫秒就回来，图标至少转 0.6 秒，旁边的"更新于"时间也会变，数据没变化也看得出刷新过。
const spinning = ref(false)
const updatedAt = ref(0)
async function refresh() {
  if (spinning.value) return
  spinning.value = true
  await Promise.all([reload(), new Promise((r) => setTimeout(r, 600))])
  spinning.value = false
}

// 分段控件只在值真正变化时发出更新。
function setRange(v: RangeKey) {
  range.value = v
  page.value = 1
  reload()
}
function setStatus(v: typeof status.value) {
  status.value = v
  applyFilters()
}
function setStream(v: typeof stream.value) {
  stream.value = v
  applyFilters()
}
function setProtocol(v: typeof protocol.value) {
  protocol.value = v
  applyFilters()
}
function applyFilters() {
  page.value = 1
  loadLogs()
}
function filterBy(dim: Dimension, id: number, value: string, label: string) {
  const f = dimFilters.value[dim]
  const next = { ...dimFilters.value }
  if (f && f.id === id && f.value === value) {
    delete next[dim] // 再点同一个值取消
  } else {
    next[dim] = { id, value, label } // 新增，或替换同一字段的旧值
  }
  dimFilters.value = next
  applyFilters()
}
function removeFilter(dim: Dimension) {
  const next = { ...dimFilters.value }
  delete next[dim]
  dimFilters.value = next
  applyFilters()
}
function applyRequestId() {
  requestId.value = requestIdInput.value.trim()
  applyFilters()
}
function clearRequestId() {
  requestId.value = ''
  requestIdInput.value = ''
  applyFilters()
}
function clearFilters() {
  status.value = 'all'
  stream.value = 'all'
  protocol.value = 'all'
  dimFilters.value = {}
  requestId.value = ''
  requestIdInput.value = ''
  applyFilters()
}
function goPage(p: number) {
  page.value = Math.min(Math.max(1, p), pages.value)
  loadLogs()
}
function setPageSize(v: unknown) {
  const n = Number(v)
  if (!pageSizes.includes(n) || n === pageSize.value) return
  pageSize.value = n
  try {
    localStorage.setItem(pageSizeKey, String(n))
  } catch {
    // 存不了只影响下次打开的默认值，忽略。
  }
  applyFilters()
}

const clickable = 'cursor-pointer decoration-dotted underline-offset-2 hover:underline'
const dimLabels: Record<Dimension, string> = { model: '模型', account: '账号', apiKey: '密钥' }
const statusOptions = [{ value: 'all', label: '全部' }, { value: 'success', label: '成功' }, { value: 'error', label: '错误' }] as const
const streamOptions = [{ value: 'all', label: '全部' }, { value: 'stream', label: '流式' }, { value: 'sync', label: '非流式' }] as const
const protocolLabels: Record<string, string> = { gemini: 'Gemini', anthropic: 'Anthropic', openai: 'OpenAI', openai_responses: 'OpenAI Responses' }
const protocolOptions = [
  { value: 'all', label: '全部' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'openai_responses', label: 'OpenAI Responses' },
] as const
const protocolLabel = (p: string) => protocolLabels[p] ?? 'Gemini'
</script>

<template>
  <div class="space-y-6">
    <LoadError v-if="error" :message="error" />

    <div class="space-y-3">
      <!-- 工具栏：所有筛选在一行，放不下时换行；右侧是条数、显示列和刷新 -->
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
        <SegmentedControl :model-value="range" :options="ranges" label="时间范围" hide-label @update:model-value="setRange" />
        <SegmentedControl :model-value="status" :options="statusOptions" label="状态" @update:model-value="setStatus" />
        <SegmentedControl :model-value="stream" :options="streamOptions" label="类型" @update:model-value="setStream" />
        <SegmentedControl :model-value="protocol" :options="protocolOptions" label="接口" @update:model-value="setProtocol" />
        <form class="relative min-w-0 flex-1 basis-48 sm:max-w-64" @submit.prevent="applyRequestId">
          <Search class="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input v-model="requestIdInput" placeholder="按请求 ID 查找，回车确认" class="h-8 pl-8" aria-label="请求 ID" />
        </form>
        <div class="ml-auto flex items-center gap-2">
          <!-- 显示列只对表格有用：和表格同一个容器查询，卡片布局时不显示 -->
          <div class="hidden @4xl/main:block">
            <ColumnSettings v-model="columnPrefs" :defs="columnDefs" />
          </div>
          <span v-if="updatedAt" class="hidden text-xs text-muted-foreground tabular-nums sm:inline" aria-live="polite">
            更新于 {{ fmtShortTime(updatedAt) }}
          </span>
          <Button variant="outline" size="icon-sm" aria-label="刷新" :aria-busy="spinning" @click="refresh">
            <RefreshCw :class="spinning && 'animate-spin'" />
          </Button>
        </div>
      </div>
      <div v-if="hasFilter" class="flex flex-wrap items-center gap-2 text-xs">
        <!-- 每个字段一个条件标签，各自可取消 -->
        <Badge v-for="d in activeDims" :key="d" variant="secondary" class="h-6 max-w-full gap-1 pr-1">
          <span class="text-muted-foreground">{{ dimLabels[d] }}</span>
          <span class="truncate">{{ dimFilters[d]?.label }}</span>
          <button type="button" :aria-label="`取消${dimLabels[d]}筛选`" class="rounded-full p-0.5 hover:bg-foreground/10" @click="removeFilter(d)">
            <X class="size-3" />
          </button>
        </Badge>
        <Badge v-if="requestId" variant="secondary" class="h-6 max-w-full gap-1 pr-1">
          <span class="text-muted-foreground">请求 ID</span>
          <span class="truncate font-mono">{{ requestId }}</span>
          <button type="button" aria-label="取消筛选" class="rounded-full p-0.5 hover:bg-foreground/10" @click="clearRequestId">
            <X class="size-3" />
          </button>
        </Badge>
        <Button variant="link" size="xs" class="h-6 px-1 text-muted-foreground" @click="clearFilters">清除全部筛选</Button>
      </div>

      <!-- 内容区窄于 56rem（@4xl，按内容区宽度切换）：记录卡片，字段固定。默认列实测约需 100rem，宽屏也常放不下，
           表格靠横向滚动、操作列固定在最右 -->
      <div :class="cn('divide-y overflow-hidden rounded-lg border transition-opacity duration-200 @4xl/main:hidden', loading && 'opacity-60')">
        <div v-for="r in logs" :key="r.id" class="space-y-1.5 p-4">
          <div class="flex items-start justify-between gap-3">
            <div class="flex min-w-0 flex-wrap items-center gap-1.5">
              <button type="button" :class="cn('text-sm break-all text-left', clickable)" @click="filterBy('model', 0, r.request_model, r.request_model)">
                {{ r.request_model }}
              </button>
              <Badge v-if="r.reasoning" variant="outline" :class="reasoningClass(r.reasoning)">{{ reasoningText(r.reasoning) }}</Badge>
              <Badge v-if="r.stream || r.detail?.fake_stream" variant="outline" class="text-muted-foreground">{{ r.detail?.fake_stream ? '假流' : '流' }}</Badge>
              <Badge v-if="r.protocol !== 'gemini'" variant="outline" class="text-muted-foreground">{{ protocolLabel(r.protocol) }}</Badge>
            </div>
            <UsageStatus :record="r" inline class="shrink-0" />
          </div>
          <div class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <span :title="fmtTime(r.created_at)">{{ fmtShortTime(r.created_at) }}</span>
            <span v-if="r.account_id" class="inline-flex items-center gap-1">
              <button type="button" :class="clickable" @click="filterBy('account', r.account_id, '', accountLabel(r))">{{ accountLabel(r) }}</button>
              <AffinityIcon v-if="r.detail?.affinity" :affinity="r.detail.affinity" />
            </span>
            <span v-else>无账号</span>
            <button type="button" :class="clickable" @click="filterBy('apiKey', r.api_key_id, '', keyLabel(r))">{{ keyLabel(r) }}</button>
          </div>
          <div class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground tabular-nums">
            <span>↓ {{ fmtNum(plainInput(r)) }} ↑ {{ fmtNum(r.output_tokens) }}</span>
            <span v-if="r.cached_tokens">缓存 {{ fmtTokens(r.cached_tokens) }}</span>
            <span v-if="r.thoughts_tokens">推理 {{ fmtTokens(r.thoughts_tokens) }}</span>
            <span v-if="r.cost_nanos != null">{{ fmtCost(r.cost_nanos) }}</span>
            <span>耗时 <template v-if="r.first_token_ms != null">{{ fmtDuration(r.first_token_ms) }} / </template>{{ fmtDuration(r.latency_ms) }}</span>
            <span :class="rateOf(r).tone">{{ rateOf(r).text }}</span>
            <button type="button" class="ml-auto text-foreground underline-offset-2 hover:underline" @click="detailId = r.id">详情</button>
          </div>
        </div>
        <p v-if="!logs.length && !error" class="px-4 py-10 text-center text-sm text-muted-foreground">
          {{ hasFilter ? '当前筛选条件下没有调用记录' : '这段时间没有调用记录' }}
        </p>
      </div>

      <!-- 宽屏：表格，每条记录一行；列可在"显示列"里设置 -->
      <!-- 加载中保留原有内容、稍微变淡，不闪也不跳 -->
      <div :class="cn('hidden overflow-hidden rounded-lg border transition-opacity duration-200 @4xl/main:block', loading && 'opacity-60')">
        <Table>
          <TableHeader>
            <TableRow>
              <!-- 模型列吃掉多余宽度，其他列按内容收紧 -->
              <TableHead
                v-for="key in columnPrefs.visible"
                :key="key"
                :class="cn('first:pl-4 last:pr-4', rightAligned.has(key) && 'text-right', key === 'model' && 'w-full')"
              >
                <Tooltip v-if="key === 'latency'">
                  <TooltipTrigger as-child>
                    <span class="cursor-help border-b border-dashed border-muted-foreground/40">{{ labelOf[key] }}</span>
                  </TooltipTrigger>
                  <TooltipContent>流式：首字 / 总耗时；非流式：总耗时</TooltipContent>
                </Tooltip>
                <template v-else>{{ labelOf[key] }}</template>
              </TableHead>
              <!-- 操作列固定在最右：列多时表格横向滚动，详情按钮始终看得到 -->
              <TableHead :class="['w-px pr-4 text-center', stickyActionHead]">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="r in logs" :key="r.id" class="group">
              <TableCell
                v-for="key in columnPrefs.visible"
                :key="key"
                :class="cn('first:pl-4 last:pr-4', rightAligned.has(key) && 'text-right tabular-nums')"
              >
                <span v-if="key === 'time'" class="text-muted-foreground tabular-nums" :title="fmtTime(r.created_at)">{{ fmtShortTime(r.created_at) }}</span>

                <UsageStatus v-else-if="key === 'status'" :record="r" />

                <!-- 模型名和"流"之间留出 16px，放得下悬停时浮出的筛选图标 -->
                <span v-else-if="key === 'model'" class="inline-flex max-w-72 items-center gap-4">
                  <FilterButton :label="r.request_model" dimension="模型" class="min-w-0" @click="filterBy('model', 0, r.request_model, r.request_model)">
                    <template #tip>
                      <div>实际发出：{{ r.resolved_model || '—' }}</div>
                      <div>上游返回：{{ r.upstream_model || '—' }}</div>
                    </template>
                  </FilterButton>
                  <Badge v-if="r.stream || r.detail?.fake_stream" variant="outline" class="text-muted-foreground">{{ r.detail?.fake_stream ? '假流' : '流' }}</Badge>
                </span>

                <span v-else-if="key === 'protocol'" class="text-muted-foreground">{{ protocolLabel(r.protocol) }}</span>

                <template v-else-if="key === 'reasoning'">
                  <Badge v-if="r.reasoning" variant="outline" :class="reasoningClass(r.reasoning)">{{ reasoningText(r.reasoning) }}</Badge>
                  <span v-else class="text-muted-foreground">—</span>
                </template>

                <!-- 没拿到上游响应的失败可能没有账号，不能按它筛选 -->
                <span v-else-if="key === 'account' && !r.account_id" class="text-muted-foreground">—</span>
                <!-- 账号和会话粘性标记之间留出 16px，放得下悬停时浮出的筛选图标 -->
                <span v-else-if="key === 'account'" class="inline-flex max-w-64 items-center gap-4">
                  <FilterButton
                    :label="accountLabel(r)"
                    dimension="账号"
                    :class="cn('min-w-0 max-w-52', accountGone(r) && 'text-muted-foreground')"
                    @click="filterBy('account', r.account_id, '', accountLabel(r))"
                  />
                  <AffinityIcon v-if="r.detail?.affinity" :affinity="r.detail.affinity" />
                </span>

                <FilterButton
                  v-else-if="key === 'apiKey'"
                  :label="keyLabel(r)"
                  dimension="密钥"
                  :class="cn('max-w-40', keyGone(r) && 'text-muted-foreground')"
                  @click="filterBy('apiKey', r.api_key_id, '', keyLabel(r))"
                />

                <!-- Token：虚线下划线提示可悬停看明细 -->
                <HoverCard v-else-if="key === 'token'" :open-delay="150" :close-delay="80">
                  <HoverCardTrigger as-child>
                    <span
                      tabindex="0"
                      class="inline-flex cursor-default items-center gap-1.5 border-b border-dashed border-muted-foreground/40 outline-none focus-visible:rounded-sm focus-visible:ring-2 focus-visible:ring-ring/50"
                    >
                      <span><span class="text-muted-foreground">↓</span>{{ fmtNum(plainInput(r)) }}</span>
                      <span><span class="text-muted-foreground">↑</span>{{ fmtNum(r.output_tokens) }}</span>
                      <span v-if="r.cached_tokens" class="inline-flex items-center gap-0.5 text-muted-foreground">
                        <Database class="size-3" />{{ fmtTokens(r.cached_tokens) }}
                      </span>
                    </span>
                  </HoverCardTrigger>
                  <HoverCardContent align="end" class="w-56">
                    <div class="mb-2 text-sm font-semibold">Token 明细</div>
                    <dl class="space-y-1 text-xs tabular-nums">
                      <div class="flex justify-between gap-3"><dt class="text-muted-foreground">普通输入</dt><dd>{{ fmtNum(plainInput(r)) }}</dd></div>
                      <div class="flex justify-between gap-3"><dt class="text-muted-foreground">输出</dt><dd>{{ fmtNum(r.output_tokens) }}</dd></div>
                      <div class="flex justify-between gap-3"><dt class="text-muted-foreground">缓存读取</dt><dd>{{ optTokens(r.cached_tokens) }}</dd></div>
                      <div class="flex justify-between gap-3"><dt class="text-muted-foreground">推理</dt><dd>{{ optTokens(r.thoughts_tokens) }}</dd></div>
                      <div class="mt-1 flex justify-between gap-3 border-t pt-1.5 font-semibold"><dt>总 Token</dt><dd>{{ fmtNum(r.total_tokens) }}</dd></div>
                    </dl>
                  </HoverCardContent>
                </HoverCard>

                <!-- 费用：没有 Token 的失败请求不计费，直接显示 —；其余悬停看算式或没算的原因 -->
                <span v-else-if="key === 'cost' && !r.total_tokens" class="text-muted-foreground">—</span>
                <HoverCard v-else-if="key === 'cost'" :open-delay="150" :close-delay="80">
                  <HoverCardTrigger as-child>
                    <span
                      tabindex="0"
                      :class="cn('cursor-default border-b border-dashed border-muted-foreground/40 outline-none focus-visible:rounded-sm focus-visible:ring-2 focus-visible:ring-ring/50', r.cost_nanos == null && 'text-muted-foreground')"
                    >{{ fmtCost(r.cost_nanos) }}</span>
                  </HoverCardTrigger>
                  <HoverCardContent align="end" class="w-72">
                    <div class="mb-2 text-sm font-semibold">费用（按官方价估算）</div>
                    <CostBreakdown :record="r" />
                  </HoverCardContent>
                </HoverCard>

                <span v-else-if="key === 'ip'" class="text-muted-foreground tabular-nums">{{ r.client_ip || '—' }}</span>

                <Tooltip v-else-if="key === 'client'">
                  <TooltipTrigger as-child>
                    <span class="block max-w-40 cursor-default truncate text-muted-foreground">{{ uaShort(r.user_agent) || '—' }}</span>
                  </TooltipTrigger>
                  <TooltipContent v-if="r.user_agent" class="max-w-96 break-all">{{ r.user_agent }}</TooltipContent>
                </Tooltip>

                <!-- 耗时：流式在总耗时前面用灰色带上首字，总耗时始终在右边对齐；悬停看各阶段明细 -->
                <HoverCard v-else-if="key === 'latency'" :open-delay="150" :close-delay="80">
                  <HoverCardTrigger as-child>
                    <span tabindex="0" class="cursor-default border-b border-dashed border-muted-foreground/40 outline-none focus-visible:rounded-sm focus-visible:ring-2 focus-visible:ring-ring/50">
                      <span v-if="r.first_token_ms != null" class="text-muted-foreground">{{ fmtDuration(r.first_token_ms) }} / </span>{{ fmtDuration(r.latency_ms) }}
                    </span>
                  </HoverCardTrigger>
                  <HoverCardContent align="end" class="w-60">
                    <LatencyBreakdown :record="r" />
                  </HoverCardContent>
                </HoverCard>

                <Tooltip v-else-if="key === 'rate'">
                  <TooltipTrigger as-child>
                    <span :class="cn('cursor-default', rateOf(r).tone)">{{ rateOf(r).text }}</span>
                  </TooltipTrigger>
                  <TooltipContent class="block">
                    <div>(输出 + 推理) ÷ {{ r.first_token_ms != null ? '生成耗时' : '耗时' }} {{ fmtDuration(genMs(r)) }}</div>
                    <div v-if="rateOf(r).note">{{ rateOf(r).note }}</div>
                  </TooltipContent>
                </Tooltip>

                <CopyId v-else-if="key === 'requestId'" :id="r.request_id" />
              </TableCell>
              <!-- 固定列要不透明才能盖住滚动中的内容；悬停色和整行的半透明悬停色混合后一致，
                   渐变时长也和整行（TableRow 的 transition-colors）一样，两边同时变色。按钮用 24px，不把行撑高。 -->
              <TableCell
                :class="['pr-4 text-center', stickyActionCell]"
              >
                <IconAction label="详情" size="xs" @click="detailId = r.id"><FileText /></IconAction>
              </TableCell>
            </TableRow>
            <TableEmpty v-if="!logs.length && !error" :colspan="columnPrefs.visible.length + 1">
              {{ hasFilter ? '当前筛选条件下没有调用记录' : '这段时间没有调用记录' }}
            </TableEmpty>
          </TableBody>
        </Table>
      </div>

      <!-- 分页栏（shadcn 数据表格的标准位置）：左侧条数，右侧每页条数、页码、翻页；只有一页时也显示，按钮置灰 -->
      <div class="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 text-sm">
        <div class="text-muted-foreground">
          共 <span class="tabular-nums">{{ fmtNum(total) }}</span> 条
        </div>
        <div class="flex items-center gap-4 lg:gap-6">
          <div class="hidden items-center gap-2 sm:flex">
            <span id="usage-page-size">每页</span>
            <Select :model-value="pageSize" @update:model-value="setPageSize">
              <SelectTrigger size="sm" class="w-20" aria-labelledby="usage-page-size"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="n in pageSizes" :key="n" :value="n">{{ n }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <span class="tabular-nums">第 {{ page }} / {{ pages }} 页</span>
          <div class="flex items-center gap-1">
            <Button variant="outline" size="icon-sm" class="hidden sm:inline-flex" aria-label="第一页" :disabled="page <= 1 || loading" @click="goPage(1)">
              <ChevronsLeft />
            </Button>
            <Button variant="outline" size="icon-sm" aria-label="上一页" :disabled="page <= 1 || loading" @click="goPage(page - 1)">
              <ChevronLeft />
            </Button>
            <Button variant="outline" size="icon-sm" aria-label="下一页" :disabled="page >= pages || loading" @click="goPage(page + 1)">
              <ChevronRight />
            </Button>
            <Button variant="outline" size="icon-sm" class="hidden sm:inline-flex" aria-label="最后一页" :disabled="page >= pages || loading" @click="goPage(pages)">
              <ChevronsRight />
            </Button>
          </div>
        </div>
      </div>
    </div>

    <UsageDetailDialog v-model="detailId" />
  </div>
</template>
