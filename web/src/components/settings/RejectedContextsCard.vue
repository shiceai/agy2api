<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { FileText, Trash2 } from '@lucide/vue'
import { api, errorMessage, type RejectedContext, type RejectedContextsView } from '@/api/client'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { fmtAgo, fmtNum, fmtTime } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import IconAction from '@/components/IconAction.vue'
import LoadError from '@/components/LoadError.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'

// "请求处理"的被拒上下文拦截：开关（切换即保存，默认开）、最近被拒的上下文（首次被拒的请求、消息条数、之后被拦的次数），
// 单条删除（之后包含它的请求照常发往上游）、全部删除。只存指纹，不存对话原文。
const router = useRouter()
const confirm = useConfirm()
const view = ref<RejectedContextsView | null>(null)
const loadError = ref('')
const saving = ref(false)
const busy = ref('')

onMounted(async () => {
  try {
    view.value = await api.get<RejectedContextsView>('/api/admin/rejected-contexts')
  } catch (e) {
    loadError.value = errorMessage(e)
  }
})

async function setEnabled(on: boolean) {
  saving.value = true
  try {
    view.value = await api.put<RejectedContextsView>('/api/admin/rejected-contexts', { enabled: on })
  } catch (e) {
    toastError(on ? '开启被拒上下文拦截失败' : '关闭被拒上下文拦截失败', e)
  } finally {
    saving.value = false
  }
}

function keyText(c: RejectedContext): string {
  return c.api_key_name || `#${c.api_key_id}`
}

// 查看首次被拒的那条用量记录：用量页按请求 ID 查，范围放宽到 30 天。
function openUsage(c: RejectedContext) {
  router.push({ path: '/usage', query: { request_id: c.request_id, range: '30d' } })
}

async function remove(c: RejectedContext) {
  const ok = await confirm({
    title: '删除这条被拒上下文？',
    description: '删除后，包含这段对话内容的请求会重新发往上游；再被拒时会重新记下。',
    confirmText: '删除',
    destructive: true,
  })
  if (!ok) return
  busy.value = c.fingerprint
  try {
    view.value = await api.del<RejectedContextsView>(`/api/admin/rejected-contexts/${c.fingerprint}`)
  } catch (e) {
    toastError('删除被拒上下文失败', e)
  } finally {
    busy.value = ''
  }
}

async function clearAll() {
  const v = view.value
  if (!v) return
  const ok = await confirm({
    title: '删除全部被拒上下文？',
    description: `将删除 ${fmtNum(v.total)} 条，删除后包含这些对话内容的请求会重新发往上游。开关不变。`,
    confirmText: '全部删除',
    destructive: true,
  })
  if (!ok) return
  busy.value = '*'
  try {
    view.value = await api.del<RejectedContextsView>('/api/admin/rejected-contexts')
  } catch (e) {
    toastError('删除全部被拒上下文失败', e)
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <LoadError v-if="loadError" :message="loadError" />
  <SettingCard v-else-if="view" title="被拒上下文拦截" description="上游拒绝过的对话，重试和续写在本地拒绝" raw content-class="px-0">
    <template #info>
      <p>
        多个账号都对同一个请求回了不带原因的 429（用量里的"请求被拒"）时，记下这段对话内容的指纹。之后的请求只要包含这段内容（原样重试、
        在后面继续对话），就在本地返回 400，不再发往上游，少让账号反复碰被拒的内容。
      </p>
      <p>删改了相关内容的、新开的对话照常发往上游。只存指纹，不存对话原文。</p>
      <p>{{ view.ttl_days }} 天没再拦到的自动删除，最多保留 {{ fmtNum(view.max) }} 条。</p>
    </template>
    <template #actions>
      <Button variant="outline" size="sm" :disabled="!!busy || !view.total" @click="clearAll"><Trash2 />全部删除</Button>
    </template>

    <div class="px-6 pb-4">
      <SettingRow label="拦截" description="减少被拒内容反复发往上游" input-id="cb-on">
        <Switch id="cb-on" :model-value="view.settings.enabled" :disabled="saving" @update:model-value="setEnabled(Boolean($event))" />
      </SettingRow>
    </div>

    <p v-if="!view.contexts.length" class="border-t px-6 py-8 text-center text-sm text-muted-foreground">还没有被拒的上下文</p>
    <template v-else>
      <!-- 窄：卡片列表；宽：表格。按内容区宽度切换 -->
      <div class="divide-y border-t @3xl/main:hidden">
        <div v-for="c in view.contexts" :key="c.fingerprint" class="flex items-start gap-3 px-6 py-3">
          <div class="min-w-0 flex-1 space-y-1 text-sm">
            <div class="flex flex-wrap items-center gap-x-2">
              <span class="break-all">{{ c.model }}</span>
              <span class="text-xs text-muted-foreground">{{ c.turns }} 条消息</span>
            </div>
            <div class="text-xs text-muted-foreground tabular-nums">
              {{ fmtTime(c.created_at) }} 首次被拒 · {{ keyText(c) }}<template v-if="c.client_ip"> · {{ c.client_ip }}</template>
            </div>
            <div class="text-xs tabular-nums">
              已拦 {{ fmtNum(c.hits) }} 次<span v-if="c.hits" class="text-muted-foreground">，最近 {{ fmtAgo(c.last_hit_at) }}</span>
            </div>
          </div>
          <div class="-mr-1 flex shrink-0">
            <IconAction label="查看首次被拒的用量" @click="openUsage(c)"><FileText /></IconAction>
            <IconAction label="删除" tone="danger" :disabled="!!busy" @click="remove(c)"><Trash2 /></IconAction>
          </div>
        </div>
      </div>

      <div class="hidden border-t @3xl/main:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead class="pl-6">首次被拒</TableHead>
              <TableHead class="w-full">模型</TableHead>
              <TableHead class="text-right">消息</TableHead>
              <TableHead>密钥</TableHead>
              <TableHead>客户端 IP</TableHead>
              <TableHead class="text-right">已拦</TableHead>
              <TableHead>最近拦截</TableHead>
              <TableHead class="px-6 text-center">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="c in view.contexts" :key="c.fingerprint">
              <TableCell class="pl-6 whitespace-nowrap text-muted-foreground tabular-nums">{{ fmtTime(c.created_at) }}</TableCell>
              <TableCell>{{ c.model }}</TableCell>
              <TableCell class="text-right tabular-nums">{{ c.turns }}</TableCell>
              <TableCell class="whitespace-nowrap">{{ keyText(c) }}</TableCell>
              <TableCell class="whitespace-nowrap tabular-nums">{{ c.client_ip || '—' }}</TableCell>
              <TableCell class="text-right tabular-nums">{{ fmtNum(c.hits) }}</TableCell>
              <TableCell class="whitespace-nowrap text-muted-foreground" :title="c.hits ? fmtTime(c.last_hit_at) : undefined">
                {{ c.hits ? fmtAgo(c.last_hit_at) : '—' }}
              </TableCell>
              <TableCell class="px-6">
                <div class="flex justify-center gap-0.5">
                  <IconAction label="查看首次被拒的用量" @click="openUsage(c)"><FileText /></IconAction>
                  <IconAction label="删除" tone="danger" :disabled="!!busy" @click="remove(c)"><Trash2 /></IconAction>
                </div>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
      <p v-if="view.total > view.contexts.length" class="border-t px-6 py-3 text-xs text-muted-foreground">
        显示最近 {{ view.contexts.length }} 条，共 {{ fmtNum(view.total) }} 条
      </p>
    </template>

  </SettingCard>
</template>
