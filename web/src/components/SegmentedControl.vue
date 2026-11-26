<script setup lang="ts" generic="T extends string">
import { nextTick, onMounted, useTemplateRef, watch } from 'vue'
import { ToggleGroupItem, ToggleGroupRoot } from 'reka-ui'

// 分段控件：一组互斥选项，灰色底槽里选中项是白色块。始终有一项选中，再点当前项不会取消。
// label 是这组选项的名称，默认显示在左侧；hideLabel 时只作为无障碍名称。
// 放不下时（手机上选项多）不超出所在行：底槽里横向滑动，名称不折行，选中项滑到看得见的地方。
defineProps<{ options: readonly { value: T; label: string }[]; label: string; hideLabel?: boolean }>()
const model = defineModel<T>({ required: true })

function onUpdate(v: unknown) {
  if (typeof v === 'string' && v && v !== model.value) model.value = v as T
}

const wrap = useTemplateRef<HTMLElement>('wrap')
// revealSelected 只横向滚动底槽，不动页面。
function revealSelected() {
  nextTick(() => {
    const groove = wrap.value?.querySelector<HTMLElement>('[role="group"]')
    const item = groove?.querySelector<HTMLElement>('[data-state="on"]')
    if (!groove || !item || groove.scrollWidth <= groove.clientWidth) return
    const left = item.offsetLeft - groove.offsetLeft
    if (left < groove.scrollLeft) groove.scrollLeft = left
    else if (left + item.offsetWidth > groove.scrollLeft + groove.clientWidth) groove.scrollLeft = left + item.offsetWidth - groove.clientWidth
  })
}
onMounted(revealSelected)
watch(model, revealSelected)
</script>

<template>
  <div ref="wrap" class="inline-flex max-w-full min-w-0 items-center gap-2">
    <span v-if="!hideLabel" class="shrink-0 text-sm whitespace-nowrap text-muted-foreground" aria-hidden="true">{{ label }}</span>
    <ToggleGroupRoot
      type="single"
      :model-value="model"
      :aria-label="label"
      class="inline-flex h-8 max-w-full min-w-0 items-center overflow-x-auto rounded-lg bg-muted p-0.5 [scrollbar-width:none]"
      @update:model-value="onUpdate"
    >
      <ToggleGroupItem
        v-for="o in options"
        :key="o.value"
        :value="o.value"
        class="inline-flex h-7 shrink-0 items-center rounded-md px-2.5 text-sm whitespace-nowrap text-muted-foreground transition-[color,box-shadow] outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-[state=on]:bg-background data-[state=on]:text-foreground data-[state=on]:shadow-sm dark:data-[state=on]:bg-input/50"
      >
        {{ o.label }}
      </ToggleGroupItem>
    </ToggleGroupRoot>
  </div>
</template>
