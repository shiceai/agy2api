<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import { X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { selectionMode } from '@/lib/selection'

// 批量操作的选择栏（Material 的上下文操作栏）：有勾选时顶栏进入选择模式，页名和页面操作收起，换成这一栏：
// 取消选择、已选数量，后面紧跟使用方放进来的操作按钮，从左排起，离表格的勾选列近。
// 传送到 App.vue 的 #page-selection；离开页面时退出选择模式。
const props = defineProps<{ count: number }>()
defineEmits<{ clear: [] }>()

watch(
  () => props.count > 0,
  (on) => (selectionMode.value = on),
  { immediate: true },
)
onBeforeUnmount(() => (selectionMode.value = false))
</script>

<template>
  <Teleport defer to="#page-selection">
    <!-- 顶栏是 muted 底，按钮悬停改用 background 色，否则看不出悬停。
         整栏左移 6px 让 ✕ 的图标和页名对齐；左移放在容器上，不放按钮上，否则按钮被横向滚动容器裁掉一截 -->
    <div
      v-if="count > 0"
      role="toolbar"
      aria-label="批量操作"
      class="-ml-1.5 flex min-w-0 flex-1 items-center gap-1 overflow-x-auto py-1 [&_[data-slot=button]:hover]:bg-background"
    >
      <Button variant="ghost" size="icon-sm" class="shrink-0 text-muted-foreground" aria-label="取消选择" @click="$emit('clear')">
        <X />
      </Button>
      <span class="mr-1 text-sm font-semibold whitespace-nowrap tabular-nums sm:mr-3">已选 {{ count }} 个</span>
      <slot />
    </div>
  </Teleport>
</template>
