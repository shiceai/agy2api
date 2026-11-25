<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { Columns3, GripVertical } from '@lucide/vue'
import { cn } from '@/lib/utils'
import { defaultPrefs, type ColumnDef, type ColumnPrefs } from '@/lib/columns'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'

// 表格"显示列"设置：勾选显示哪些列，拖动把手（或聚焦把手后按 ↑↓）调整顺序，至少保留一列。
const props = defineProps<{ defs: ColumnDef[] }>()
const prefs = defineModel<ColumnPrefs>({ required: true })

const list = ref<HTMLElement | null>(null)
const dragging = ref<string | null>(null)
// 拖动经过的上一个目标：同一目标上的连续 dragover 不重复移动，避免来回跳。
let lastOver: string | null = null

const labels = computed<Record<string, string>>(() => Object.fromEntries(props.defs.map((d) => [d.key, d.label])))
const visibleSet = computed(() => new Set(prefs.value.visible))

function update(order: string[], visible: Set<string>) {
  prefs.value = { order, visible: order.filter((k) => visible.has(k)) }
}

function toggle(key: string) {
  const visible = new Set(visibleSet.value)
  if (visible.has(key)) {
    if (visible.size === 1) return
    visible.delete(key)
  } else {
    visible.add(key)
  }
  update(prefs.value.order, visible)
}

function move(key: string, to: number) {
  const order = prefs.value.order.filter((k) => k !== key)
  order.splice(Math.max(0, Math.min(to, order.length)), 0, key)
  update(order, visibleSet.value)
}

function onDragStart(e: DragEvent, key: string) {
  dragging.value = key
  lastOver = null
  if (e.dataTransfer) {
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', key)
  }
}
function onDragOver(e: DragEvent, key: string) {
  if (!dragging.value) return
  e.preventDefault()
  if (key === dragging.value || key === lastOver) return
  lastOver = key
  move(dragging.value, prefs.value.order.indexOf(key))
}
function onDragEnd() {
  dragging.value = null
  lastOver = null
}

async function onHandleKey(e: KeyboardEvent, key: string) {
  const i = prefs.value.order.indexOf(key)
  const to = e.key === 'ArrowUp' ? i - 1 : e.key === 'ArrowDown' ? i + 1 : -1
  if (to < 0 || to >= prefs.value.order.length) return
  e.preventDefault()
  move(key, to)
  // 列表重排后焦点可能丢失，移回同一列的把手上。
  await nextTick()
  list.value?.querySelector<HTMLElement>(`[data-key="${key}"] [data-handle]`)?.focus()
}
</script>

<template>
  <Popover>
    <PopoverTrigger as-child>
      <Button variant="outline" size="sm"><Columns3 />显示列</Button>
    </PopoverTrigger>
    <PopoverContent align="end" class="w-64 gap-0 p-1.5">
      <div class="flex items-center justify-between px-2 py-1.5">
        <span class="text-sm font-semibold">显示列</span>
        <span class="text-xs text-muted-foreground tabular-nums">{{ prefs.visible.length }} / {{ defs.length }}</span>
      </div>
      <ul ref="list" class="max-h-[min(30rem,60vh)] space-y-0.5 overflow-y-auto py-1">
        <li
          v-for="key in prefs.order"
          :key="key"
          :data-key="key"
          draggable="true"
          :class="cn('flex items-center gap-2 rounded-md px-2 py-1.5 hover:bg-muted', dragging === key && 'opacity-50')"
          @dragstart="onDragStart($event, key)"
          @dragover="onDragOver($event, key)"
          @drop.prevent
          @dragend="onDragEnd"
        >
          <label class="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-sm">
            <Checkbox
              :model-value="visibleSet.has(key)"
              :disabled="visibleSet.has(key) && prefs.visible.length === 1"
              @update:model-value="toggle(key)"
            />
            <span class="truncate">{{ labels[key] }}</span>
          </label>
          <button
            type="button"
            data-handle
            class="cursor-grab rounded-sm p-0.5 text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 active:cursor-grabbing"
            :aria-label="`调整“${labels[key]}”的位置：拖动，或按上下方向键`"
            @keydown="onHandleKey($event, key)"
          >
            <GripVertical class="size-4" />
          </button>
        </li>
      </ul>
      <div class="flex justify-end border-t px-1 pt-1.5">
        <Button variant="ghost" size="xs" @click="prefs = defaultPrefs(defs)">恢复默认</Button>
      </div>
    </PopoverContent>
  </Popover>
</template>
