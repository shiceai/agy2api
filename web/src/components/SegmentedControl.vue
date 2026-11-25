<script setup lang="ts" generic="T extends string">
import { ToggleGroupItem, ToggleGroupRoot } from 'reka-ui'

// 分段控件：一组互斥选项，灰色底槽里选中项是白色块。始终有一项选中，再点当前项不会取消。
// label 是这组选项的名称，默认显示在左侧；hideLabel 时只作为无障碍名称。
defineProps<{ options: readonly { value: T; label: string }[]; label: string; hideLabel?: boolean }>()
const model = defineModel<T>({ required: true })

function onUpdate(v: unknown) {
  if (typeof v === 'string' && v && v !== model.value) model.value = v as T
}
</script>

<template>
  <div class="inline-flex items-center gap-2">
    <span v-if="!hideLabel" class="text-sm text-muted-foreground" aria-hidden="true">{{ label }}</span>
    <ToggleGroupRoot
      type="single"
      :model-value="model"
      :aria-label="label"
      class="inline-flex h-8 items-center rounded-lg bg-muted p-0.5"
      @update:model-value="onUpdate"
    >
      <ToggleGroupItem
        v-for="o in options"
        :key="o.value"
        :value="o.value"
        class="inline-flex h-7 items-center rounded-md px-2.5 text-sm whitespace-nowrap text-muted-foreground transition-[color,box-shadow] outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-[state=on]:bg-background data-[state=on]:text-foreground data-[state=on]:shadow-sm dark:data-[state=on]:bg-input/50"
      >
        {{ o.label }}
      </ToggleGroupItem>
    </ToggleGroupRoot>
  </div>
</template>
