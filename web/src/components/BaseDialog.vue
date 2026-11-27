<script setup lang="ts">
import { computed, ref, type HTMLAttributes } from 'vue'
import { useResizeObserver } from '@vueuse/core'
import { cn } from '@/lib/utils'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ScrollArea } from '@/components/ui/scroll-area'

// 弹窗的公共外壳：标题区、正文、底部按钮三段，标题和按钮固定，内容多时只有正文滚动（ScrollArea 的细滚动条，
// 和弹窗边缘留出间距），正文真的需要滚动时才在标题区下方、按钮区上方出分隔线，内容短的弹窗和普通弹窗一样。
// 业务弹窗只填插槽：
//   <BaseDialog v-model:open="open" title="…" description="…" size="lg">正文<template #footer>按钮</template></BaseDialog>
// 标题、说明要放控件时用 title、description 插槽；说明只给屏幕阅读器时加 description-sr-only。
// 其余属性（如 open-auto-focus 等事件）传给 DialogContent。
defineOptions({ inheritAttrs: false })

const open = defineModel<boolean>('open', { default: false })
const props = withDefaults(
  defineProps<{
    title?: string
    description?: string
    descriptionSrOnly?: boolean
    // size 是桌面端的最大宽度，手机上都是屏幕宽减边距。
    size?: 'md' | 'lg' | 'xl' | '2xl'
    class?: HTMLAttributes['class']
  }>(),
  { size: 'md' },
)
const slots = defineSlots<{
  default?: () => unknown
  title?: () => unknown
  description?: () => unknown
  footer?: () => unknown
}>()

const sizes = { md: 'sm:max-w-md', lg: 'sm:max-w-lg', xl: 'sm:max-w-xl', '2xl': 'sm:max-w-2xl' }

// 正文超出时标出分隔线：正文内容或视口（弹窗高度跟着屏幕变）尺寸变了都重新量。
const body = ref<HTMLElement>()
const viewport = computed(() => body.value?.closest<HTMLElement>('[data-slot=scroll-area-viewport]') ?? undefined)
const scrollable = ref(false)
useResizeObserver(
  computed(() => [body.value, viewport.value]),
  () => {
    const v = viewport.value
    scrollable.value = !!v && v.scrollHeight > v.clientHeight + 1
  },
)

// ScrollArea 的视口带 tabindex=0（能用键盘滚动），打开时 reka 默认会把焦点给它。改成和普通弹窗一样：
// 给第一个可聚焦的控件（同 reka，跳过链接），一般是正文里的第一个输入框。
const focusable = 'button:not([disabled]),input:not([disabled]):not([type=hidden]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'
function focusFirst(e: Event) {
  const content = e.target as HTMLElement | null
  const first = [...(content?.querySelectorAll<HTMLElement>(focusable) ?? [])].find(
    (el) => !el.hasAttribute('data-reka-scroll-area-viewport') && el.tagName !== 'A' && el.getClientRects().length > 0,
  )
  if (!first) return
  e.preventDefault()
  first.focus()
}
</script>

<template>
  <Dialog v-model:open="open">
    <!-- 三行网格，中间一行 minmax(0,1fr)：内容超过 max-h 时在 ScrollArea 里滚动。不用 flex 列：只有 max-h 时
         子项高度不确定，ScrollArea 收不住。grid-cols-1 限制列宽，正文里的宽表格只在自己的容器里横向滚动。
         三段之间合计 24px，和 DialogContent 默认的 gap-6 一致。 -->
    <DialogContent
      v-bind="$attrs"
      :class="cn('max-h-[calc(100dvh-2rem)] grid-cols-1 grid-rows-[auto_minmax(0,1fr)_auto] gap-0 p-0', sizes[props.size], props.class)"
      @open-auto-focus="focusFirst"
    >
      <!-- 右侧留出关闭按钮的位置，长标题折行不压到按钮下面 -->
      <DialogHeader :class="['px-6 pt-6 pr-12 pb-3', scrollable && 'border-b']">
        <DialogTitle class="wrap-anywhere">
          <slot name="title">{{ title }}</slot>
        </DialogTitle>
        <DialogDescription v-if="slots.description || description" :class="descriptionSrOnly && 'sr-only'">
          <slot name="description">{{ description }}</slot>
        </DialogDescription>
      </DialogHeader>

      <ScrollArea
        class="min-h-0 [&>[data-slot=scroll-area-scrollbar]]:w-3.5 [&>[data-slot=scroll-area-scrollbar]]:py-1.5 [&>[data-slot=scroll-area-scrollbar]]:pr-1.5"
      >
        <div ref="body" :class="['grid grid-cols-1 gap-6 px-6 pt-3', slots.footer ? 'pb-3' : 'pb-6']">
          <slot />
        </div>
      </ScrollArea>

      <DialogFooter v-if="slots.footer" :class="['px-6 pt-3 pb-6', scrollable && 'border-t']">
        <slot name="footer" />
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
