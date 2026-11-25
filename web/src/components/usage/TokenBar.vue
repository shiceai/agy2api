<script setup lang="ts">
import { computed } from 'vue'
import type { UsageLog } from '@/api/client'
import { fmtNum } from '@/lib/utils'

// Token 构成：一条细的分段横条（宽度按占比，段间留 2px 缝，两端圆角，不放文字）加图例。
// 按 dataviz 规范，四五类的占比用横条比环形图好比较；图例按横条顺序列出名称、数值和占比，读屏也读图例。
// 总数由使用方显示（详情弹窗顶部的统计条）。
const props = defineProps<{ record: UsageLog }>()

const parts = computed(() => {
  const r = props.record
  const cached = r.cached_tokens ?? 0
  return [
    { name: '普通输入', value: Math.max(0, r.prompt_tokens - cached), color: 'bg-cat-1' },
    { name: '缓存读取', value: cached, color: 'bg-cat-2' },
    { name: '推理', value: r.thoughts_tokens ?? 0, color: 'bg-cat-3' },
    { name: '输出', value: r.output_tokens, color: 'bg-cat-4' },
  ]
})
const sum = computed(() => parts.value.reduce((a, p) => a + p.value, 0))
const share = (v: number) => (sum.value ? `${((v / sum.value) * 100).toFixed(v / sum.value < 0.1 ? 1 : 0)}%` : '—')
</script>

<template>
  <div class="space-y-3">
    <div v-if="sum" class="flex h-2 gap-0.5 overflow-hidden rounded-full" aria-hidden="true">
      <div
        v-for="p in parts.filter((x) => x.value > 0)"
        :key="p.name"
        :class="['h-full', p.color]"
        :style="{ flexGrow: p.value, flexBasis: 0 }"
        :title="`${p.name} ${fmtNum(p.value)}（${share(p.value)}）`"
      />
    </div>
    <dl class="space-y-1 text-xs tabular-nums">
      <div v-for="p in parts" :key="p.name" class="flex items-center justify-between gap-3">
        <dt class="flex items-center gap-1.5 text-muted-foreground"><span :class="['size-2 rounded-[2px]', p.color]" />{{ p.name }}</dt>
        <dd>{{ fmtNum(p.value) }}<span v-if="p.value > 0" class="ml-1.5 inline-block w-10 text-right text-muted-foreground">{{ share(p.value) }}</span><span v-else class="ml-1.5 inline-block w-10" /></dd>
      </div>
    </dl>
  </div>
</template>
