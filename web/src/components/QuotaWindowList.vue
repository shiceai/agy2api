<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronRight, TriangleAlert } from '@lucide/vue'
import type { ModelInfo } from '@/api/client'
import { cn, fmtTime } from '@/lib/utils'
import { fmtPercent, quotaWindows, type QuotaWindow } from '@/lib/quota'
import QuotaMeter from './QuotaMeter.vue'

// 额度窗口列表：每个窗口一行，名称、额度条、剩余、重置时间对齐成列；点一行展开它包含的模型。
// 低于避让阈值（lowQuota，百分比）的带警告图标，底部说明。额度弹窗和账号详情共用。
const props = defineProps<{ models: ModelInfo[]; lowQuota: number }>()

const windows = computed(() => quotaWindows(props.models))

const expanded = ref(new Set<string>())
function toggle(key: string) {
  const next = new Set(expanded.value)
  if (!next.delete(key)) next.add(key)
  expanded.value = next
}

// 重置时间只取最大的一级单位（四舍五入），列宽稳定；悬停看具体时刻。
function resetText(w: QuotaWindow): string {
  if (!Number.isFinite(w.reset)) return '—'
  const min = (w.reset - Date.now()) / 60_000
  if (min <= 0) return '待刷新'
  if (min < 60) return `${Math.ceil(min)} 分钟后`
  const h = Math.round(min / 60)
  if (h < 24) return `${h} 小时后`
  return `${Math.round(h / 24)} 天后`
}
const avoided = (w: QuotaWindow) => props.lowQuota > 0 && w.remaining < props.lowQuota / 100
const anyAvoided = computed(() => windows.value.some(avoided))
</script>

<template>
  <p v-if="!windows.length" class="px-4 py-3 text-xs text-muted-foreground">没有额度信息</p>
  <template v-else>
    <!-- 一个网格，各行用 subgrid 对齐列：名称（放不下时截断）、额度条（吃掉剩余宽度）、剩余、重置时间 -->
    <div class="grid max-h-96 grid-cols-[minmax(0,max-content)_minmax(3rem,1fr)_auto_auto] overflow-y-auto p-1.5 text-xs">
      <template v-for="w in windows" :key="w.key">
        <button
          type="button"
          class="col-span-4 grid grid-cols-subgrid items-center gap-x-3 rounded-md px-2.5 py-2 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50"
          :aria-expanded="expanded.has(w.key)"
          @click="toggle(w.key)"
        >
          <span class="flex min-w-0 items-center gap-1">
            <ChevronRight :class="cn('size-3.5 shrink-0 text-muted-foreground transition-transform', expanded.has(w.key) && 'rotate-90')" />
            <span class="truncate" :title="w.label">{{ w.label }}</span>
          </span>
          <QuotaMeter :value="w.remaining" class="h-1.5" />
          <span class="flex items-center justify-end gap-1 tabular-nums">
            <TriangleAlert v-if="avoided(w)" class="size-3.5 text-amber-600 dark:text-amber-400" aria-label="选号时避开" />
            {{ fmtPercent(w.remaining) }}
          </span>
          <span class="min-w-14 text-right text-muted-foreground tabular-nums" :title="fmtTime(w.reset)">{{ resetText(w) }}</span>
        </button>
        <p v-if="expanded.has(w.key)" class="col-span-4 px-2.5 pb-2 pl-7 leading-relaxed text-muted-foreground">
          {{ w.names.join('、') }}<span class="whitespace-nowrap"> · 共 {{ w.count }} 个模型</span>
        </p>
      </template>
    </div>
    <p v-if="anyAvoided" class="flex items-center gap-1.5 border-t px-4 py-2 text-xs text-muted-foreground">
      <TriangleAlert class="size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
      剩余低于 {{ lowQuota }}% 的模型，选号时先避开这个账号
    </p>
  </template>
</template>
