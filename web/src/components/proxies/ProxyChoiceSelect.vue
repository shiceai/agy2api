<script setup lang="ts">
import { computed } from 'vue'
import type { Proxy } from '@/api/client'
import { egressText, freeProxies, proxyValue } from '@/lib/proxy'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

// 选代理的下拉框：值为 auto（自动分配空闲代理，allowAuto 时才有）、none（直连）或 p:<ID>（指定代理，停用的不能选）。
// 每个代理后面灰字写出口和绑定情况。添加账号、批量分配、账号详情里改绑共用。
const value = defineModel<string>({ required: true })
const props = defineProps<{ proxies: Proxy[]; allowAuto?: boolean; id?: string; disabled?: boolean }>()

const free = computed(() => freeProxies(props.proxies))
const usage = (p: Proxy) => (p.accounts.length ? `已绑 ${p.accounts.length} 个账号` : '空闲')
const egress = (p: Proxy) => (p.check_error ? '测试失败' : egressText(p.egress_ip, p.egress_country) || '未测试')
</script>

<template>
  <Select v-model="value" :disabled="disabled">
    <SelectTrigger :id="id" class="w-full min-w-0"><SelectValue /></SelectTrigger>
    <SelectContent>
      <SelectItem v-if="allowAuto" value="auto" :disabled="!free">自动分配空闲代理（可用 {{ free }} 个）</SelectItem>
      <SelectItem value="none">直连（不用代理）</SelectItem>
      <!-- 停用的代理不能选（选了账号也会暂停调度），先在代理页启用 -->
      <SelectItem v-for="p in proxies" :key="p.id" :value="proxyValue(p.id)" :disabled="p.status !== 'enabled'">
        {{ p.name }}<span class="text-muted-foreground"> · {{ p.status === 'enabled' ? egress(p) : '已停用' }} · {{ usage(p) }}</span>
      </SelectItem>
    </SelectContent>
  </Select>
</template>
