<script setup lang="ts">
import { computed } from 'vue'
import type { WarmupState } from '@/api/client'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import DotBadge from './DotBadge.vue'

// 新号预热：蓝点标签"预热 N%"，点开看进度、压低后的并发上限和请求间隔（点击而不是悬停，手机上也能看）。
// 进度向下取整，没预热完不会显示 100%。
const props = defineProps<{ state: WarmupState }>()

const percent = computed(() => Math.floor(props.state.progress * 100))
const interval = computed(() => {
  const ms = props.state.interval_ms
  if (ms <= 0) return '不限'
  return ms < 1000 ? `${ms} 毫秒` : `${(ms / 1000).toFixed(1)} 秒`
})
// 号龄、成功数两项都参与时，上面两行都列出来了，这里只说"任一达标"。
const finish = computed(() => (props.state.age_target && props.state.success_target ? '任一达标即完成，之后恢复正常' : '达标后恢复正常'))
</script>

<template>
  <Popover>
    <PopoverTrigger as-child>
      <button
        type="button"
        class="rounded-4xl outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        :aria-label="`新号预热中，进度 ${percent}%，点开查看`"
      >
        <DotBadge tone="info" class="cursor-pointer hover:bg-muted">预热 {{ percent }}%</DotBadge>
      </button>
    </PopoverTrigger>
    <PopoverContent align="start" class="w-auto max-w-[calc(100vw-2rem)] gap-0 p-0">
      <div class="border-b px-3 py-2 text-sm font-semibold">新号预热中</div>
      <table class="text-xs">
        <tbody>
          <tr class="border-b">
            <td class="py-1.5 pr-4 pl-3 align-top whitespace-nowrap text-muted-foreground">进度</td>
            <td class="py-1.5 pr-3 whitespace-nowrap tabular-nums">
              <div>{{ percent }}%</div>
              <div v-if="state.age_target" class="text-muted-foreground">号龄 {{ state.age_minutes }} / {{ state.age_target }} 分钟</div>
              <div v-if="state.success_target" class="text-muted-foreground">成功 {{ state.successes }} / {{ state.success_target }} 次</div>
            </td>
          </tr>
          <tr class="border-b">
            <td class="py-1.5 pr-4 pl-3 whitespace-nowrap text-muted-foreground">并发上限</td>
            <td class="py-1.5 pr-3 whitespace-nowrap tabular-nums">{{ state.limit }}（正常 {{ state.normal }}）</td>
          </tr>
          <tr class="border-b">
            <td class="py-1.5 pr-4 pl-3 whitespace-nowrap text-muted-foreground">请求间隔</td>
            <td class="py-1.5 pr-3 whitespace-nowrap tabular-nums">{{ interval }}</td>
          </tr>
        </tbody>
      </table>
      <div class="px-3 py-2 text-xs text-muted-foreground">{{ finish }}</div>
    </PopoverContent>
  </Popover>
</template>
