<script setup lang="ts">
import { computed, ref } from 'vue'
import { ComboboxAnchor, ComboboxInput, ComboboxRoot } from 'reka-ui'
import { ComboboxEmpty, ComboboxGroup, ComboboxItem, ComboboxList } from '@/components/ui/combobox'
import { TagsInput, TagsInputInput, TagsInputItem, TagsInputItemDelete, TagsInputItemText } from '@/components/ui/tags-input'

// 允许的模型：标签输入框（shadcn TagsInput + Combobox 的组合做法）。输入时从号池的模型目录里联想
// （家族名、具体 ID，以及 gemini-*、claude-* 这类通配），回车或逗号把输入的内容直接加成标签，支持 * 通配。
const model = defineModel<string[]>({ required: true })
const props = defineProps<{ suggestions: string[]; id?: string; placeholder?: string }>()

const search = ref('')
const open = ref(false)
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  const chosen = new Set(model.value.map((m) => m.toLowerCase()))
  return props.suggestions.filter((s) => !chosen.has(s.toLowerCase()) && (!q || s.toLowerCase().includes(q))).slice(0, 50)
})

function add(v: string) {
  if (!model.value.some((m) => m.toLowerCase() === v.toLowerCase())) model.value = [...model.value, v]
  search.value = ''
}
</script>

<template>
  <ComboboxRoot v-model:open="open" :ignore-filter="true" :reset-search-term-on-blur="false" class="w-full">
    <ComboboxAnchor as-child>
      <TagsInput v-model="model" :delimiter="/[,，\s]+/" add-on-paste add-on-blur class="w-full gap-1.5 px-2 py-1.5">
        <TagsInputItem v-for="m in model" :key="m" :value="m">
          <TagsInputItemText class="text-xs" />
          <TagsInputItemDelete />
        </TagsInputItem>
        <ComboboxInput v-model="search" as-child>
          <TagsInputInput :id="id" :placeholder="model.length ? '' : placeholder" class="min-w-40" @focus="open = true" @keydown.enter.prevent />
        </ComboboxInput>
      </TagsInput>
    </ComboboxAnchor>
    <ComboboxList align="start" class="w-(--reka-popper-anchor-width) p-1">
      <ComboboxEmpty class="px-2 py-1.5 text-xs text-muted-foreground">没有匹配的模型，回车直接添加</ComboboxEmpty>
      <ComboboxGroup>
        <ComboboxItem v-for="s in filtered" :key="s" :value="s" class="text-xs" @select.prevent="add(s)">{{ s }}</ComboboxItem>
      </ComboboxGroup>
    </ComboboxList>
  </ComboboxRoot>
</template>
