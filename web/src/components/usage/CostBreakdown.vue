<script setup lang="ts">
import { computed } from 'vue'
import type { UsageRecord } from '@/api/client'
import { fmtCost, fmtRate } from '@/lib/cost'
import { fmtNum } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'

// 一条用量的费用算法：按哪个价格（型号、官方或手动），各部分 token × 单价，合计。费用悬浮和详情弹窗共用。
// 没算费用时说明原因：没有 token（失败的请求），或记录时还没有这个模型的价格（有了价格后会补算）。
const props = defineProps<{ record: UsageRecord }>()

const cost = computed(() => props.record.detail?.cost)
const rows = computed(() => {
  const r = props.record
  const c = cost.value
  if (!c) return []
  const cached = Math.min(r.cached_tokens ?? 0, r.prompt_tokens)
  const thoughts = r.thoughts_tokens ?? 0
  const out = [{ label: '普通输入', tokens: r.prompt_tokens - cached, rate: c.input }]
  if (cached > 0) out.push({ label: '缓存读取', tokens: cached, rate: c.cache_read })
  out.push({ label: thoughts > 0 ? '输出 + 推理' : '输出', tokens: r.output_tokens + thoughts, rate: c.output })
  // token × 百万分之一美元每百万 token ÷ 1000 = 十亿分之一美元
  return out.map((x) => ({ ...x, nanos: Math.round((x.tokens * x.rate) / 1000) }))
})
const reason = computed(() =>
  props.record.total_tokens > 0
    ? `记录时还没有 ${props.record.resolved_model || props.record.request_model} 的价格，在设置 → 价格里设置后会补算。`
    : '没有 Token，不计费用。',
)
</script>

<template>
  <div v-if="cost" class="space-y-2 text-xs">
    <div class="flex flex-wrap items-center gap-1.5">
      <span class="text-muted-foreground">按</span>
      <span>{{ cost.model }}</span>
      <span class="text-muted-foreground">{{ cost.source === 'manual' ? '手动价' : '官方价' }}</span>
      <Badge v-if="cost.promo" variant="outline" class="text-muted-foreground">限时价</Badge>
      <Badge v-if="cost.tier" variant="outline" class="text-muted-foreground">长提示价</Badge>
    </div>
    <dl class="space-y-1 tabular-nums">
      <div v-for="row in rows" :key="row.label" class="flex justify-between gap-3">
        <dt class="text-muted-foreground">{{ row.label }} {{ fmtNum(row.tokens) }} × {{ fmtRate(row.rate) }}</dt>
        <dd>{{ fmtCost(row.nanos, true) }}</dd>
      </div>
      <div class="mt-1 flex justify-between gap-3 border-t pt-1.5 font-semibold">
        <dt>合计</dt>
        <dd>{{ fmtCost(cost.nanos, true) }}</dd>
      </div>
    </dl>
    <p class="text-muted-foreground">
      单价为美元每百万 token<template v-if="cost.backfilled">；记录时还没有价格，按当时有效的价格补算</template>。
    </p>
  </div>
  <p v-else class="text-xs text-muted-foreground">{{ reason }}</p>
</template>
