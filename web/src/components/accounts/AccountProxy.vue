<script setup lang="ts">
import { computed } from 'vue'
import type { Account } from '@/api/client'
import { egressText } from '@/lib/proxy'
import { fmtShortTime } from '@/lib/utils'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import DotBadge from '@/components/DotBadge.vue'

// 账号绑定的代理：显示出口（没测试过时显示代理名），没绑的显示"直连"；代理停用了显示"代理已停用"，
// 代理连不上、暂停调度中时显示"代理故障"（这两种情况账号都不参与调度）。
const props = defineProps<{ account: Account }>()

const paused = computed(() => props.account.proxy_paused_until > Date.now())
const disabled = computed(() => props.account.proxy?.status === 'disabled')
const text = computed(() => {
  const p = props.account.proxy
  return p ? egressText(p.egress_ip, p.egress_country) || p.name : ''
})
</script>

<template>
  <Tooltip v-if="disabled">
    <TooltipTrigger as-child>
      <span class="cursor-default"><DotBadge tone="muted">代理已停用</DotBadge></span>
    </TooltipTrigger>
    <TooltipContent>{{ account.proxy?.name }} 已停用，这个账号暂停调度；在代理页启用，或给账号换代理</TooltipContent>
  </Tooltip>
  <Tooltip v-else-if="paused">
    <TooltipTrigger as-child>
      <span class="cursor-default"><DotBadge tone="warning">代理故障</DotBadge></span>
    </TooltipTrigger>
    <TooltipContent>{{ account.proxy?.name }} 连不上，暂停调度到 {{ fmtShortTime(account.proxy_paused_until) }}</TooltipContent>
  </Tooltip>
  <Tooltip v-else-if="account.proxy">
    <TooltipTrigger as-child>
      <span class="cursor-default whitespace-nowrap tabular-nums">{{ text }}</span>
    </TooltipTrigger>
    <TooltipContent>
      代理：{{ account.proxy.name }}<template v-if="account.proxy.check_error"><br />最近测试失败：{{ account.proxy.check_error }}</template>
    </TooltipContent>
  </Tooltip>
  <span v-else class="text-muted-foreground">直连</span>
</template>
