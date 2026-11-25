<script setup lang="ts">
import { computed } from 'vue'
import DotBadge from './DotBadge.vue'

// HTTP 状态码，和启用/停用同一种徽章：2xx 绿点，4xx（含 429 限流）橙点，5xx 及其他红点。
const props = defineProps<{ code: number }>()

const tone = computed(() => {
  if (props.code >= 200 && props.code < 300) return 'success'
  if (props.code >= 400 && props.code < 500) return 'warning'
  return 'danger'
})
</script>

<template>
  <DotBadge :tone="tone" class="tabular-nums">{{ code }}</DotBadge>
</template>
