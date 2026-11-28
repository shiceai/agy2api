<script setup lang="ts">
import { ref } from 'vue'
import { Check, Copy } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { copyText } from '@/lib/clipboard'
import IconAction from './IconAction.vue'

// 一段可复制的代码或配置：顶部一行是标题（如 bash、~/.codex/config.toml）和复制按钮，复制后按钮短暂变成对勾，不弹提示。
// 内容超宽时横向滚动，不折行。
const props = defineProps<{ label: string; code: string }>()
const copied = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined

async function copy() {
  if (!(await copyText(props.code))) {
    toast.error('复制失败', { description: '浏览器不允许访问剪贴板，请手动选中复制。' })
    return
  }
  copied.value = true
  clearTimeout(timer)
  timer = setTimeout(() => (copied.value = false), 1500)
}
</script>

<template>
  <div class="min-w-0 overflow-hidden rounded-lg border bg-muted/40">
    <div class="flex items-center justify-between gap-2 border-b py-1 pr-1 pl-3 text-xs text-muted-foreground">
      <span class="truncate">{{ label }}</span>
      <IconAction :label="copied ? '已复制' : '复制'" size="xs" @click="copy">
        <Check v-if="copied" class="text-emerald-600 dark:text-emerald-400" />
        <Copy v-else />
      </IconAction>
    </div>
    <pre class="overflow-x-auto p-3 font-mono text-xs leading-relaxed"><code>{{ code }}</code></pre>
  </div>
</template>
