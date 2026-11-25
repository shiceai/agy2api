<script setup lang="ts">
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'

// 带圆点的描边徽章：文字用正文颜色，状态只由圆点颜色表达。启用/停用、HTTP 状态码共用，形状和高度一致。
defineProps<{ tone: 'success' | 'warning' | 'danger' | 'muted' }>()

const dots = {
  success: 'bg-emerald-500',
  warning: 'bg-amber-500',
  danger: 'bg-red-500',
  muted: 'bg-muted-foreground/50',
}
</script>

<template>
  <Badge variant="outline" class="gap-1.5">
    <span :class="cn('size-1.5 rounded-full', dots[tone])" aria-hidden="true" />
    <slot />
  </Badge>
</template>
