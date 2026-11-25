<script setup lang="ts">
import { Field, FieldContent, FieldDescription, FieldLabel } from '@/components/ui/field'

// 设置项一行（shadcn Field，horizontal）：名称和一行说明在左、控件在右。可以换行：文字至少留 12rem，
// 放得下的控件（开关）留在右边，放不下的（输入框组）在窄卡片上自己折到下一行。
// 说明只写效果，最多一行；背景和原理放到卡片的 ⓘ 里。inputId 让名称可点击聚焦到控件。放在 SettingCard 里用。
defineProps<{ label: string; description?: string; inputId?: string }>()
</script>

<template>
  <Field orientation="horizontal" class="flex-wrap gap-x-6 gap-y-3 py-4 first:pt-0 last:pb-0">
    <FieldContent class="min-w-48">
      <FieldLabel :for="inputId">{{ label }}</FieldLabel>
      <FieldDescription v-if="description">{{ description }}</FieldDescription>
    </FieldContent>
    <div class="flex shrink-0 items-center gap-2">
      <slot />
    </div>
  </Field>
</template>
