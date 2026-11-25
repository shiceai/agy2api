<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'
import { severity, severityColor } from '@/lib/quota'

// 额度条：填充按严重程度着色，底轨是同色的浅色（dataviz 的 meter 规范），整条都能看出状态。
// value 是剩余比例，为 null 时只画灰色底轨。粗细由调用方的 class 定（表格里 4px，弹窗里 6px）。
// used 为 true 时 value 是已用比例（API Key 的 Token 上限）：填充画已用部分，颜色仍按剩余多少定。
const props = defineProps<{ value: number | null; used?: boolean; class?: string }>()

const color = computed(() => {
  if (props.value == null) return null
  return severityColor[severity(props.used ? 1 - props.value : props.value)]
})
const width = computed(() => `${Math.max(0, Math.min(1, props.value ?? 0)) * 100}%`)
</script>

<template>
  <span
    :class="cn('relative block h-1 overflow-hidden rounded-full', props.class)"
    :style="{ background: color ? `color-mix(in oklab, ${color} 22%, transparent)` : 'var(--muted)' }"
    aria-hidden="true"
  >
    <span v-if="color" class="absolute inset-y-0 left-0 rounded-full" :style="{ width, background: color }" />
  </span>
</template>
