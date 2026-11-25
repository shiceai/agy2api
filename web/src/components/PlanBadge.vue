<script setup lang="ts">
import { computed } from 'vue'
import type { Subscription } from '@/api/client'
import { fmtAgo } from '@/lib/utils'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import DotBadge from './DotBadge.vue'

// 账号套餐：显示付费档位名称（如 Google AI Pro）；降到 Antigravity Starter Quota 时显示橙点"已降档"。
// 悬停看付费档位、当前档位、不符合资格的原因和检查时间。还没检查过（升级前导入的账号）显示"—"。
const props = defineProps<{ subscription: Subscription | null }>()

const name = computed(() => props.subscription?.paid_tier || props.subscription?.current_tier || '—')
</script>

<template>
  <span v-if="!subscription" class="text-muted-foreground">—</span>
  <Tooltip v-else>
    <TooltipTrigger as-child>
      <span class="inline-flex cursor-default items-center whitespace-nowrap">
        <DotBadge v-if="subscription.downgraded" tone="warning">已降档</DotBadge>
        <template v-else>{{ name }}</template>
      </span>
    </TooltipTrigger>
    <TooltipContent class="max-w-80">
      <div class="space-y-0.5">
        <div v-if="subscription.downgraded">付费档位降到了 Starter，订阅可能已到期或失效</div>
        <div>付费档位：{{ subscription.paid_tier || '无' }}</div>
        <div>当前档位：{{ subscription.current_tier || '—' }}</div>
        <div v-for="r in subscription.ineligible ?? []" :key="r">不符合资格：{{ r }}</div>
        <div class="opacity-70">检查于 {{ fmtAgo(subscription.checked_at) }}</div>
      </div>
    </TooltipContent>
  </Tooltip>
</template>
