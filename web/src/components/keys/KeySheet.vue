<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowRight, Pencil } from '@lucide/vue'
import { api, type ApiKey, type KeyModelStat } from '@/api/client'
import { expiryText, fmtCompact, fmtLeft, keyState, keyStateBadge, periodLabels } from '@/lib/keys'
import { cn, fmtAgo, fmtDuration, fmtNum, fmtTime, fmtTokens } from '@/lib/utils'
import { fmtCost, sumCost } from '@/lib/cost'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import DotBadge from '@/components/DotBadge.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'

// API Key 详情：在 Key 列表点"详情"打开（右侧面板，手机上占满）。上半部分是限制（到期、Token 上限和本周期用量、
// 允许的模型），下半部分是所选时间范围内的用量（按最终请求统计：请求、成功率、Token、平均耗时、各模型明细），
// 可以跳到用量页按这个 Key 筛选。
const key = defineModel<ApiKey | null>('apiKey', { required: true })
defineEmits<{ edit: [k: ApiKey] }>()
const router = useRouter()

// 关闭时保留最后显示的 Key，淡出动画里不变空。
const shown = ref<ApiKey | null>(null)
watch(key, (k) => k && (shown.value = k), { immediate: true })

type RangeKey = '24h' | '7d' | '30d'
const ranges: { value: RangeKey; label: string; ms: number }[] = [
  { value: '24h', label: '24 小时', ms: 86400_000 },
  { value: '7d', label: '7 天', ms: 7 * 86400_000 },
  { value: '30d', label: '30 天', ms: 30 * 86400_000 },
]
const range = ref<RangeKey>('24h')
const stats = ref<KeyModelStat[] | null>(null)
const loading = ref(false)
const error = ref('')

// 换 Key 时清空旧数据；只换时间范围时保留原内容、稍微变淡，取到后替换。
let lastID: number | null = null
watch(
  [() => key.value?.id, range],
  async ([id]) => {
    if (id == null) return
    if (id !== lastID) stats.value = null
    lastID = id
    error.value = ''
    loading.value = true
    const since = Date.now() - (ranges.find((r) => r.value === range.value)?.ms ?? 0)
    try {
      const res = await api.get<{ models: KeyModelStat[] }>(`/api/admin/keys/${id}/stats?since=${since}`)
      if (key.value?.id === id) stats.value = res.models
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const summary = computed(() => {
  const list = stats.value ?? []
  const requests = list.reduce((s, m) => s + m.requests, 0)
  const success = list.reduce((s, m) => s + m.success, 0)
  const tokens = list.reduce((s, m) => s + m.tokens, 0)
  const cost = sumCost(list.map((m) => m.cost_nanos))
  const totalMs = list.reduce((s, m) => s + m.avg_ms * m.success, 0)
  return { requests, success, tokens, cost, avgMs: success ? totalMs / success : null }
})
function fmtRate(success: number, requests: number): string {
  if (!requests) return '—'
  const v = (success / requests) * 100
  return `${v === 100 || v === 0 ? v : v.toFixed(1)}%`
}

const state = computed(() => (shown.value ? keyStateBadge[keyState(shown.value)] : null))
const usedRatio = computed(() => (shown.value?.token_limit ? shown.value.period_used / shown.value.token_limit : 0))

// 跳到用量页，按这个 Key 筛选，时间范围跟面板一致。
function openUsage() {
  const k = shown.value
  if (!k) return
  key.value = null
  router.push({ path: '/usage', query: { api_key_id: String(k.id), key_label: k.name || `${k.prefix}…`, range: range.value } })
}
</script>

<template>
  <Sheet :open="!!key" @update:open="(v) => !v && (key = null)">
    <SheetContent class="gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-xl">
      <template v-if="shown">
        <SheetHeader class="gap-2 border-b px-6 py-4">
          <SheetTitle class="truncate pr-8">{{ shown.name || '（未命名）' }}</SheetTitle>
          <SheetDescription as="div" class="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span class="font-mono text-xs">{{ shown.prefix }}…</span>
            <DotBadge v-if="state" :tone="state.tone">{{ state.label }}</DotBadge>
            <span>创建于 {{ fmtAgo(shown.created_at) }}</span>
          </SheetDescription>
          <div class="flex gap-2 pt-1">
            <Button variant="outline" size="sm" @click="$emit('edit', shown)"><Pencil />编辑</Button>
          </div>
        </SheetHeader>

        <ScrollArea class="min-h-0 flex-1">
          <div class="divide-y px-6">
            <!-- 限制 -->
            <section class="py-5">
              <h3 class="mb-2.5 text-sm font-semibold">限制</h3>
              <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-3 text-sm">
                <dt class="text-muted-foreground">到期</dt>
                <dd>
                  <template v-if="shown.expires_at">
                    <span :class="shown.expired && 'text-destructive'">{{ expiryText(shown) }}</span>
                    <span class="ml-2 text-muted-foreground tabular-nums">{{ fmtTime(shown.expires_at) }}</span>
                  </template>
                  <span v-else>不过期</span>
                </dd>

                <dt class="text-muted-foreground">Token 上限</dt>
                <dd>
                  <div v-if="shown.token_limit" class="space-y-1.5">
                    <div class="flex flex-wrap items-baseline gap-x-2">
                      <span class="tabular-nums">{{ fmtCompact(shown.period_used) }} / {{ fmtCompact(shown.token_limit) }}</span>
                      <span class="text-muted-foreground">{{ periodLabels[shown.token_period as 'day'] }}</span>
                      <span v-if="shown.period_reset_at" class="text-xs text-muted-foreground" :title="fmtTime(shown.period_reset_at)">
                        {{ fmtLeft(shown.period_reset_at - Date.now()) }}后重置
                      </span>
                    </div>
                    <QuotaMeter :value="usedRatio" used class="h-1.5 max-w-64" />
                  </div>
                  <span v-else>不限</span>
                </dd>

                <dt class="text-muted-foreground">允许的模型</dt>
                <dd>
                  <div v-if="shown.allowed_models.length" class="flex flex-wrap gap-1.5">
                    <span v-for="m in shown.allowed_models" :key="m" class="rounded-md bg-secondary px-1.5 py-0.5 text-xs">{{ m }}</span>
                  </div>
                  <span v-else>不限制</span>
                </dd>
              </dl>
            </section>

            <!-- 用量：按最终请求统计 -->
            <section class="py-5">
              <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
                <h3 class="text-sm font-semibold">用量</h3>
                <SegmentedControl v-model="range" :options="ranges" label="时间范围" hide-label />
              </div>
              <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
              <div v-else-if="!stats" class="h-32" />
              <div v-else :class="cn('space-y-5 transition-opacity duration-200', loading && 'opacity-60')">
                <dl class="grid grid-cols-3 gap-4 rounded-lg bg-muted/50 px-4 py-3 sm:grid-cols-5">
                  <div>
                    <dt class="text-xs text-muted-foreground">请求</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ fmtNum(summary.requests) }}</dd>
                  </div>
                  <div>
                    <dt class="text-xs text-muted-foreground">成功率</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ fmtRate(summary.success, summary.requests) }}</dd>
                  </div>
                  <div>
                    <dt class="text-xs text-muted-foreground">Token</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ fmtTokens(summary.tokens) }}</dd>
                  </div>
                  <div>
                    <dt class="text-xs text-muted-foreground">平均耗时</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ summary.avgMs == null ? '—' : fmtDuration(Math.round(summary.avgMs)) }}</dd>
                  </div>
                  <div>
                    <dt class="text-xs text-muted-foreground">费用</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ fmtCost(summary.cost) }}</dd>
                  </div>
                </dl>

                <div v-if="stats.length">
                  <h4 class="mb-1.5 text-xs font-semibold text-muted-foreground">各模型</h4>
                  <table class="w-full text-sm whitespace-nowrap tabular-nums">
                    <thead class="text-xs text-muted-foreground">
                      <tr class="border-b">
                        <th class="w-full py-1.5 pr-3 text-left font-normal">模型</th>
                        <th class="py-1.5 pr-3 text-right font-normal">请求</th>
                        <th class="py-1.5 pr-3 text-right font-normal">成功率</th>
                        <th class="py-1.5 pr-3 text-right font-normal">Token</th>
                        <th class="py-1.5 pr-3 text-right font-normal">平均耗时</th>
                        <th class="py-1.5 text-right font-normal">费用</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="m in stats" :key="m.model" class="border-b last:border-0">
                        <td class="max-w-0 truncate py-1.5 pr-3" :title="m.model">{{ m.model }}</td>
                        <td class="py-1.5 pr-3 text-right">{{ fmtNum(m.requests) }}</td>
                        <td :class="['py-1.5 pr-3 text-right', m.success < m.requests && 'text-amber-700 dark:text-amber-400']">
                          {{ fmtRate(m.success, m.requests) }}
                        </td>
                        <td class="py-1.5 pr-3 text-right">{{ fmtTokens(m.tokens) }}</td>
                        <td class="py-1.5 pr-3 text-right">{{ m.success ? fmtDuration(Math.round(m.avg_ms)) : '—' }}</td>
                        <td :class="['py-1.5 text-right', m.cost_nanos == null && 'text-muted-foreground']">{{ fmtCost(m.cost_nanos) }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <p v-else class="py-4 text-center text-sm text-muted-foreground">这段时间没有用这个密钥的调用</p>

                <Button variant="link" size="sm" class="h-auto px-0" @click="openUsage">在用量页查看<ArrowRight /></Button>
              </div>
            </section>
          </div>
        </ScrollArea>
      </template>
    </SheetContent>
  </Sheet>
</template>
