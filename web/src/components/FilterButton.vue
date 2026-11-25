<script setup lang="ts">
import { useSlots } from 'vue'
import { ListFilter } from '@lucide/vue'
import { cn } from '@/lib/utils'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

// 表格里可点击筛选的值（模型、账号、密钥）。悬停时文字出现下划线，右侧浮出漏斗图标；
// 图标绝对定位，落在单元格右内边距里，平时不占位置、不引起列宽变化。
// 悬停说明里先放调用方的补充内容（tip 插槽），最后一行说明点击筛选。
defineOptions({ inheritAttrs: false })
const props = defineProps<{ label: string; dimension: string; class?: string }>()
defineEmits<{ click: [] }>()
const slots = useSlots()
</script>

<template>
  <Tooltip>
    <TooltipTrigger as-child>
      <button
        type="button"
        :aria-label="`按${dimension}筛选：${label}`"
        :class="cn('group/filter relative inline-flex max-w-full cursor-pointer items-center align-middle outline-none', props.class)"
        @click="$emit('click')"
      >
        <span class="min-w-0 truncate decoration-dotted underline-offset-2 group-hover/filter:underline group-focus-visible/filter:underline">
          {{ label }}
        </span>
        <ListFilter
          class="pointer-events-none absolute top-1/2 left-full ml-0.5 size-3 -translate-y-1/2 text-muted-foreground opacity-0 transition-opacity group-hover/filter:opacity-100 group-focus-visible/filter:opacity-100"
          aria-hidden="true"
        />
      </button>
    </TooltipTrigger>
    <TooltipContent class="block">
      <slot name="tip" />
      <div :class="slots.tip && 'mt-1 opacity-70'">点击按此{{ dimension }}筛选，再点一次取消</div>
    </TooltipContent>
  </Tooltip>
</template>
