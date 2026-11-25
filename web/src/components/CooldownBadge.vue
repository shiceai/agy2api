<script setup lang="ts">
import type { Cooldown } from '@/api/client'
import { fmtRemaining, fmtTime } from '@/lib/utils'
import { resultLabel } from '@/lib/usage'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import DotBadge from './DotBadge.vue'

// 账号的冷却：橙点标签"冷却 N"，点开列出每个模型的原因和剩余时间（点击而不是悬停，手机上也能看）。
defineProps<{ cooldowns: Cooldown[] }>()
</script>

<template>
  <Popover>
    <PopoverTrigger as-child>
      <button
        type="button"
        class="rounded-4xl outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        :aria-label="`${cooldowns.length} 个模型在冷却，点开查看`"
      >
        <DotBadge tone="warning" class="cursor-pointer hover:bg-muted">冷却 {{ cooldowns.length }}</DotBadge>
      </button>
    </PopoverTrigger>
    <PopoverContent align="start" class="w-auto max-w-[calc(100vw-2rem)] gap-0 p-0">
      <div class="border-b px-3 py-2 text-sm font-semibold">冷却中的模型</div>
      <table class="text-xs">
        <tbody>
          <tr v-for="c in cooldowns" :key="c.model" class="border-b last:border-0">
            <td class="max-w-56 truncate py-1.5 pr-4 pl-3">{{ c.model }}</td>
            <td class="py-1.5 pr-4 whitespace-nowrap text-muted-foreground">{{ resultLabel(c.reason) }}</td>
            <td class="py-1.5 pr-3 text-right whitespace-nowrap tabular-nums" :title="`到 ${fmtTime(c.until)}`">剩 {{ fmtRemaining(c.until) }}</td>
          </tr>
        </tbody>
      </table>
    </PopoverContent>
  </Popover>
</template>
