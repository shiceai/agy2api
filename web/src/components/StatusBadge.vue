<script setup lang="ts">
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import DotBadge from './DotBadge.vue'

// 启用/停用状态：启用为绿点，停用为灰点。停用带原因（系统自动停用时）的，悬停显示原因。
defineProps<{ enabled: boolean; reason?: string }>()
</script>

<template>
  <Tooltip v-if="!enabled && reason">
    <TooltipTrigger as-child>
      <DotBadge tone="muted" tabindex="0" class="cursor-help outline-none focus-visible:ring-2 focus-visible:ring-ring/50">停用</DotBadge>
    </TooltipTrigger>
    <TooltipContent>停用原因：{{ reason }}</TooltipContent>
  </Tooltip>
  <DotBadge v-else :tone="enabled ? 'success' : 'muted'">{{ enabled ? '启用' : '停用' }}</DotBadge>
</template>
