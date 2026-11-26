<script setup lang="ts">
import { computed } from 'vue'
import { ArrowLeftRight, Link2 } from '@lucide/vue'
import type { UsageAffinity } from '@/api/client'
import { affinityText } from '@/lib/usage'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

// 用量列表里账号后面的会话粘性标记：沿用绑定的账号为灰色链接图标，临时借号为灰色换号图标（之后回到原账号），
// 改绑为橙色换号图标，悬停看原因；新对话是常态，不显示。
const props = defineProps<{ affinity: UsageAffinity }>()
const text = computed(() => affinityText(props.affinity))
</script>

<template>
  <Tooltip v-if="affinity.result !== 'new'">
    <TooltipTrigger as-child>
      <span
        tabindex="0"
        :aria-label="`会话粘性：${text.value}`"
        class="inline-flex shrink-0 cursor-default rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <Link2 v-if="affinity.result === 'hit'" class="size-3.5 text-muted-foreground" />
        <ArrowLeftRight v-else-if="affinity.result === 'diverted'" class="size-3.5 text-muted-foreground" />
        <ArrowLeftRight v-else class="size-3.5 text-amber-600 dark:text-amber-400" />
      </span>
    </TooltipTrigger>
    <TooltipContent class="block max-w-80">
      <div>会话粘性：{{ text.value }}</div>
      <div v-if="text.muted">{{ text.muted }}</div>
    </TooltipContent>
  </Tooltip>
</template>
