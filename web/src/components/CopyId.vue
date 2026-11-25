<script setup lang="ts">
import { computed, ref } from 'vue'
import { copyText } from '@/lib/clipboard'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

// 长 ID 的紧凑显示：只显示前 8 位，悬停看完整，点击复制，复制后说明里提示"已复制"。
// 提示框默认点一下触发元素就关，这里设成点击不关，复制结果才看得到。
const props = defineProps<{ id: string }>()
const copied = ref(false)
const short = computed(() => (props.id.length > 8 ? props.id.slice(0, 8) + '…' : props.id))

async function copy() {
  if (!(await copyText(props.id))) return
  copied.value = true
  setTimeout(() => (copied.value = false), 1500)
}
</script>

<template>
  <span v-if="!id" class="text-muted-foreground">—</span>
  <Tooltip v-else disable-closing-trigger>
    <TooltipTrigger as-child>
      <button
        type="button"
        class="rounded-sm font-mono text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
        :aria-label="`复制请求 ID ${id}`"
        @click="copy"
      >
        {{ short }}
      </button>
    </TooltipTrigger>
    <TooltipContent class="block">
      <div class="font-mono">{{ id }}</div>
      <div class="mt-1 opacity-70">{{ copied ? '已复制' : '点击复制' }}</div>
    </TooltipContent>
  </Tooltip>
</template>
