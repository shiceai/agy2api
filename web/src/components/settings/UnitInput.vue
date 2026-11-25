<script setup lang="ts">
import { Input } from '@/components/ui/input'

// 带单位的数字输入框。清空时值为 NaN，交给表单校验提示。
const model = defineModel<number>({ required: true })
defineProps<{ unit: string; min?: number; max?: number; id?: string; disabled?: boolean; label?: string }>()

function onInput(v: string | number) {
  model.value = v === '' ? NaN : Number(v)
}
</script>

<template>
  <div class="flex items-center gap-2">
    <Input
      :id="id"
      :model-value="Number.isNaN(model) ? '' : model"
      type="number"
      inputmode="numeric"
      step="1"
      :min="min"
      :max="max"
      :disabled="disabled"
      :aria-label="label"
      class="h-8 w-20 text-right tabular-nums"
      @update:model-value="onInput"
    />
    <span v-if="unit" class="min-w-8 text-sm text-muted-foreground">{{ unit }}</span>
  </div>
</template>
