<script setup lang="ts">
import { computed } from 'vue'
import type { Account } from '@/api/client'
import { cn, fmtAgo, fmtTime } from '@/lib/utils'
import { fmtPercent, quotaGroups, quotaWindows } from '@/lib/quota'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import QuotaMeter from './QuotaMeter.vue'
import QuotaWindowList from './QuotaWindowList.vue'

// 账号额度：两条迷你额度条（Gemini、Claude 各取组内最低），点开是额度窗口列表（点击而不是悬停，手机上也能看）。
// lowQuota 是避让阈值（百分比）；row 为 true 时两条横排（手机卡片）。
const props = defineProps<{ account: Account; lowQuota: number; row?: boolean }>()

const groups = computed(() => quotaGroups(props.account.models))
const hasQuota = computed(() => quotaWindows(props.account.models).length > 0)
</script>

<template>
  <span v-if="!hasQuota" class="text-muted-foreground">—</span>
  <Popover v-else>
    <PopoverTrigger as-child>
      <button
        type="button"
        :class="
          cn(
            'flex cursor-pointer rounded-sm text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
            row ? 'flex-wrap gap-x-4 gap-y-1' : 'flex-col gap-1',
          )
        "
        aria-label="查看各模型额度"
      >
        <span v-for="g in groups" :key="g.label" class="flex items-center gap-2">
          <span class="w-10 text-left text-muted-foreground">{{ g.label }}</span>
          <QuotaMeter :value="g.remaining" class="w-12" />
          <span class="w-9 text-right tabular-nums">{{ g.remaining == null ? '—' : fmtPercent(g.remaining) }}</span>
        </span>
      </button>
    </PopoverTrigger>
    <PopoverContent align="start" class="w-[22rem] max-w-[calc(100vw-2rem)] gap-0 p-0">
      <div class="flex items-baseline justify-between gap-3 border-b px-4 py-2.5">
        <span class="text-sm font-semibold">模型额度</span>
        <span class="text-xs text-muted-foreground" :title="fmtTime(account.models_refreshed_at)">
          更新于 {{ fmtAgo(account.models_refreshed_at) }}
        </span>
      </div>
      <QuotaWindowList :models="account.models" :low-quota="lowQuota" />
    </PopoverContent>
  </Popover>
</template>
