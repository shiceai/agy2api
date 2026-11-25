<script setup lang="ts">
import { computed, h, type FunctionalComponent } from 'vue'
import { parseNotes, type Inline } from '@/lib/notes'

// 发布说明：标题、列表、段落，行内的加粗、代码和链接（见 lib/notes.ts，不用 v-html）。
const props = defineProps<{ notes: string }>()
const blocks = computed(() => parseNotes(props.notes))

const Inlines: FunctionalComponent<{ inlines: Inline[] }> = ({ inlines }) =>
  inlines.map((i) => {
    switch (i.kind) {
      case 'strong':
        return h('strong', { class: 'font-semibold' }, i.text)
      case 'code':
        return h('code', { class: 'rounded bg-muted px-1 py-0.5 font-sans text-[0.9em]' }, i.text)
      case 'link':
        return h('a', { href: i.href, target: '_blank', rel: 'noopener noreferrer', class: 'underline underline-offset-4' }, i.text)
      default:
        return i.text
    }
  })
Inlines.props = ['inlines']
</script>

<template>
  <div class="space-y-2 text-sm leading-relaxed">
    <template v-for="(b, i) in blocks" :key="i">
      <h4 v-if="b.kind === 'heading'" class="pt-1 font-semibold"><Inlines :inlines="b.inlines" /></h4>
      <ul v-else-if="b.kind === 'list'" class="list-disc space-y-1 pl-5">
        <li v-for="(item, j) in b.items" :key="j"><Inlines :inlines="item" /></li>
      </ul>
      <p v-else><Inlines :inlines="b.inlines" /></p>
    </template>
  </div>
</template>
