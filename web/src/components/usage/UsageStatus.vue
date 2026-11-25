<script setup lang="ts">
import { computed } from 'vue'
import type { UsageLog } from '@/api/client'
import HttpStatus from '@/components/HttpStatus.vue'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { resultLabel } from '@/lib/usage'

// 用量记录的状态码。失败的悬停显示失败分类、上游原因码和说明；成功但中途换过号的，悬停显示前面每次失败的账号和分类。
// 完整的尝试列表在详情弹窗里。inline 用在手机卡片上（没有悬停）：失败分类直接写在状态码左边。
const props = defineProps<{ record: UsageLog; inline?: boolean }>()

const attempts = computed(() => props.record.detail?.attempts ?? [])
// 老记录没有失败原因（kind 为空），不显示悬停说明。
const failure = computed(() => (props.record.detail?.error?.kind ? props.record.detail.error : undefined))
// 成功前失败的尝试（最后一次是成功的那次）。
const retried = computed(() => (failure.value ? [] : attempts.value.slice(0, -1).filter((a) => a.result !== 'success')))
</script>

<template>
  <span class="inline-flex items-center gap-1.5">
    <span v-if="inline && failure" class="text-xs text-muted-foreground">{{ resultLabel(failure.kind) }}</span>
    <Tooltip v-if="!inline && (failure || retried.length)">
      <TooltipTrigger as-child>
        <span tabindex="0" class="cursor-default rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
          <HttpStatus :code="record.status_code" />
        </span>
      </TooltipTrigger>
      <TooltipContent class="block max-w-80 space-y-1">
        <template v-if="failure">
          <div class="font-semibold">
            {{ resultLabel(failure.kind) }}<span v-if="failure.reason" class="ml-1.5 font-normal opacity-70">{{ failure.reason }}</span>
          </div>
          <div v-if="failure.message" class="line-clamp-4 break-words">{{ failure.message }}</div>
          <div v-if="attempts.length > 1" class="opacity-70">共试了 {{ attempts.length }} 个账号</div>
        </template>
        <template v-else>
          <div class="font-semibold">换号 {{ retried.length }} 次后成功</div>
          <div v-for="a in retried" :key="a.try" class="break-all">{{ a.account || `#${a.account_id}` }}：{{ resultLabel(a.result) }}</div>
        </template>
      </TooltipContent>
    </Tooltip>
    <HttpStatus v-else :code="record.status_code" />
  </span>
</template>
