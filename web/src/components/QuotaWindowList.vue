<script setup lang="ts">
import { computed, ref } from 'vue'
import { Ban, ChevronRight, TriangleAlert } from '@lucide/vue'
import type { Account } from '@/api/client'
import { cn, fmtTime } from '@/lib/utils'
import { fmtPercent, quotaSections, unavailableModels, type QuotaRow } from '@/lib/quota'
import QuotaMeter from './QuotaMeter.vue'

// 额度列表：名称、额度条、剩余、重置时间对齐成列。没拉每周额度时每个推算出的窗口一行；拉到了时上游的每组
// 一节（组名一行，下面 5 小时、每周各一行）。点组名（或单行）展开它包含的模型。低于避让阈值（lowQuota，百分比）
// 的带警告图标（选号时 5 小时和每周额度都看），每周额度用完的组重置前不选，底部说明；账号不能用的模型（上游的
// 占位条目）列在最后。额度弹窗和账号详情共用。
const props = defineProps<{ account: Pick<Account, 'models' | 'quota_summary' | 'weekly_quota_on'>; lowQuota: number }>()

const sections = computed(() => quotaSections(props.account))
const unavailable = computed(() => unavailableModels(props.account.models))

const expanded = ref(new Set<string>())
function toggle(key: string) {
  const next = new Set(expanded.value)
  if (!next.delete(key)) next.add(key)
  expanded.value = next
}

// 重置时间只取最大的一级单位（四舍五入），列宽稳定；悬停看具体时刻。
function resetText(r: QuotaRow): string {
  if (!Number.isFinite(r.reset)) return '—'
  const min = (r.reset - Date.now()) / 60_000
  if (min <= 0) return '待刷新'
  if (min < 60) return `${Math.ceil(min)} 分钟后`
  const h = Math.round(min / 60)
  if (h < 24) return `${h} 小时后`
  return `${Math.round(h / 24)} 天后`
}
// 选号时避开剩余低于阈值的；每周额度用完的组在重置前不选（还没到重置时间才算）。
const avoided = (r: QuotaRow) => r.remaining != null && props.lowQuota > 0 && r.remaining < props.lowQuota / 100
const anyAvoided = computed(() => sections.value.some((s) => s.rows.some(avoided)))
const weeklyOut = computed(() =>
  sections.value
    .filter((s) => s.rows.some((r) => r.window === 'weekly' && r.remaining === 0 && r.reset > Date.now()))
    .map((s) => s.label),
)
</script>

<template>
  <p v-if="!sections.length && !unavailable.length" class="px-4 py-3 text-xs text-muted-foreground">没有额度信息</p>
  <template v-else>
    <!-- 一个网格，各行用 subgrid 对齐列：名称（放不下时截断）、额度条（吃掉剩余宽度）、剩余、重置时间 -->
    <div v-if="sections.length" class="grid max-h-96 grid-cols-[minmax(0,max-content)_minmax(3rem,1fr)_auto_auto] overflow-y-auto p-1.5 text-xs">
      <template v-for="s in sections" :key="s.key">
        <!-- 单行：推算出的窗口，名称、额度条在同一行 -->
        <button
          v-if="s.rows.length === 1 && s.rows[0].label === s.label"
          type="button"
          class="col-span-4 grid grid-cols-subgrid items-center gap-x-3 rounded-md px-2.5 py-2 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50"
          :aria-expanded="expanded.has(s.key)"
          @click="toggle(s.key)"
        >
          <span class="flex min-w-0 items-center gap-1">
            <ChevronRight :class="cn('size-3.5 shrink-0 text-muted-foreground transition-transform', expanded.has(s.key) && 'rotate-90')" />
            <span class="truncate" :title="s.label">{{ s.label }}</span>
          </span>
          <QuotaMeter :value="s.rows[0].remaining" class="h-1.5" />
          <span class="flex items-center justify-end gap-1 tabular-nums">
            <TriangleAlert v-if="avoided(s.rows[0])" class="size-3.5 text-amber-600 dark:text-amber-400" aria-label="选号时避开" />
            {{ s.rows[0].remaining == null ? '—' : fmtPercent(s.rows[0].remaining) }}
          </span>
          <span class="min-w-14 text-right text-muted-foreground tabular-nums" :title="fmtTime(s.rows[0].reset)">{{ resetText(s.rows[0]) }}</span>
        </button>
        <!-- 一节：上游的组，组名一行，下面每个窗口一行 -->
        <template v-else>
          <button
            type="button"
            class="col-span-4 flex items-center gap-1 rounded-md px-2.5 pt-2 pb-1 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50"
            :aria-expanded="expanded.has(s.key)"
            @click="toggle(s.key)"
          >
            <ChevronRight :class="cn('size-3.5 shrink-0 text-muted-foreground transition-transform', expanded.has(s.key) && 'rotate-90')" />
            <span class="truncate" :title="s.label">{{ s.label }}</span>
          </button>
          <p v-if="expanded.has(s.key)" class="col-span-4 px-2.5 pb-1 pl-7 leading-relaxed text-muted-foreground">
            {{ s.names.join('、') }}
          </p>
          <div v-for="r in s.rows" :key="r.key" class="col-span-4 grid grid-cols-subgrid items-center gap-x-3 px-2.5 py-1.5 pl-7">
            <span class="truncate text-muted-foreground">{{ r.label }}</span>
            <QuotaMeter :value="r.remaining" class="h-1.5" />
            <span class="flex items-center justify-end gap-1 tabular-nums">
              <TriangleAlert v-if="avoided(r)" class="size-3.5 text-amber-600 dark:text-amber-400" aria-label="选号时避开" />
              {{ r.remaining == null ? '—' : fmtPercent(r.remaining) }}
            </span>
            <span class="min-w-14 text-right text-muted-foreground tabular-nums" :title="fmtTime(r.reset)">{{ resetText(r) }}</span>
          </div>
        </template>
        <p v-if="expanded.has(s.key) && s.rows.length === 1 && s.rows[0].label === s.label" class="col-span-4 px-2.5 pb-2 pl-7 leading-relaxed text-muted-foreground">
          {{ s.names.join('、') }}
        </p>
      </template>
    </div>
    <p v-if="unavailable.length" class="flex items-start gap-1.5 border-t px-4 py-2 text-xs text-muted-foreground">
      <Ban class="mt-px size-3.5 shrink-0" />
      这个账号不能用：{{ unavailable.join('、') }}
    </p>
    <p v-if="weeklyOut.length" class="flex items-start gap-1.5 border-t px-4 py-2 text-xs text-muted-foreground">
      <Ban class="mt-px size-3.5 shrink-0 text-destructive" />
      {{ weeklyOut.join('、') }} 的每周额度用完了，重置前不选这个账号
    </p>
    <p v-if="anyAvoided" class="flex items-center gap-1.5 border-t px-4 py-2 text-xs text-muted-foreground">
      <TriangleAlert class="size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
      剩余低于 {{ lowQuota }}% 的模型，选号时先避开这个账号
    </p>
  </template>
</template>
