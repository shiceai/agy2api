<script setup lang="ts">
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

// 表格行里的图标操作按钮，悬停显示文字说明；label 同时作为无障碍名称。按操作的危险程度区分颜色：
// - default：平时灰色，悬停变深（同步、设置、启用）。
// - warning：平时灰色，悬停变橙，表示影响使用但可以恢复（停用）。
// - danger：常驻淡红，悬停变红，表示不可恢复（删除），点击后还有确认框。
// size 默认 32px；单行的密集表格（如用量）用 xs（24px），不把行撑高。
// 其余属性（含外层 DropdownMenuTrigger as-child 传进来的事件和 aria）落到按钮上，不落到 Tooltip 上
// （同 shadcn 的 SidebarMenuButton），所以可以放进下拉菜单的触发器里当菜单按钮用。
defineOptions({ inheritAttrs: false })
const props = withDefaults(
  defineProps<{ label: string; disabled?: boolean; tone?: 'default' | 'warning' | 'danger'; size?: 'sm' | 'xs' }>(),
  { tone: 'default', size: 'sm' },
)
// click 带上原始事件：放进下拉菜单触发器时，reka 的点击处理要读 event.defaultPrevented。
defineEmits<{ click: [event: MouseEvent] }>()

const tones = {
  default: 'text-muted-foreground hover:text-foreground',
  warning: 'text-muted-foreground hover:bg-amber-500/10 hover:text-amber-600 dark:hover:bg-amber-500/20 dark:hover:text-amber-400',
  danger: 'text-destructive/70 hover:bg-destructive/10 hover:text-destructive dark:hover:bg-destructive/20',
}
</script>

<template>
  <Tooltip>
    <TooltipTrigger as-child>
      <Button
        v-bind="$attrs"
        variant="ghost"
        :size="props.size === 'xs' ? 'icon-xs' : 'icon-sm'"
        :aria-label="label"
        :disabled="disabled"
        :class="cn(tones[props.tone])"
        @click="$emit('click', $event)"
      >
        <slot />
      </Button>
    </TooltipTrigger>
    <TooltipContent>{{ label }}</TooltipContent>
  </Tooltip>
</template>
