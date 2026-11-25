<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { FileText, Plus, Trash2, Power, PowerOff } from '@lucide/vue'
import { api, errorMessage, type ApiKey } from '@/api/client'
import { fmtAgo, fmtTime, fmtTokens } from '@/lib/utils'
import { fmtCost } from '@/lib/cost'
import { expiryText, fmtCompact, keyState, keyStateBadge, periodLabels } from '@/lib/keys'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableEmpty, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import DotBadge from '@/components/DotBadge.vue'
import IconAction from '@/components/IconAction.vue'
import LoadError from '@/components/LoadError.vue'
import PageActions from '@/components/PageActions.vue'
import QuotaMeter from '@/components/QuotaMeter.vue'
import KeyFormDialog from '@/components/keys/KeyFormDialog.vue'
import KeySheet from '@/components/keys/KeySheet.vue'

const router = useRouter()
const confirm = useConfirm()
const keys = ref<ApiKey[]>([])
// 列表加载失败的原因，显示在内容区顶部；操作失败用 Toast。
const loadError = ref('')
const busy = ref<number | null>(null)
// 加载失败又没有数据时只显示失败提示，不显示"还没有 Key"。
const showList = computed(() => !loadError.value || keys.value.length > 0)

// 新建、编辑弹窗（editing 为 null 是新建）和详情面板里的 Key；列表刷新后详情换成新数据，Key 被删除时关闭。
const formOpen = ref(false)
const editing = ref<ApiKey | null>(null)
const detail = ref<ApiKey | null>(null)


async function load() {
  try {
    const res = await api.get<{ keys: ApiKey[] }>('/api/admin/keys')
    keys.value = res.keys ?? []
    loadError.value = ''
    if (detail.value) detail.value = keys.value.find((k) => k.id === detail.value?.id) ?? null
  } catch (e) {
    loadError.value = errorMessage(e)
  }
}
onMounted(load)

function create() {
  editing.value = null
  formOpen.value = true
}
function edit(k: ApiKey) {
  editing.value = k
  formOpen.value = true
}

// act 执行单个密钥的操作：失败时 Toast 提示（标题带上密钥名称），成功的看列表变化即可。
async function act(k: ApiKey, what: string, fn: () => Promise<unknown>) {
  busy.value = k.id
  try {
    await fn()
    await load()
  } catch (e) {
    toastError(`${what}失败：${keyTitle(k)}`, e)
  } finally {
    busy.value = null
  }
}
const keyTitle = (k: ApiKey) => k.name || k.prefix + '…'
const toggle = (k: ApiKey) =>
  act(k, k.status === 'enabled' ? '停用' : '启用', () => api.post(`/api/admin/keys/${k.id}/${k.status === 'enabled' ? 'disable' : 'enable'}`))
async function remove(k: ApiKey) {
  const ok = await confirm({
    title: `删除密钥 ${keyTitle(k)}？`,
    description: '使用它的客户端会立即失效，删除后不能恢复。',
    confirmText: '删除',
    destructive: true,
  })
  if (ok) act(k, '删除', () => api.del(`/api/admin/keys/${k.id}`))
}

const badge = (k: ApiKey) => keyStateBadge[keyState(k)]
const usedRatio = (k: ApiKey) => (k.token_limit ? k.period_used / k.token_limit : 0)
const periodLabel = (k: ApiKey) => periodLabels[k.token_period as 'day'] ?? ''
const keyLink =
  'cursor-pointer rounded-sm text-left underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50'
</script>

<template>
  <div class="space-y-6">
    <PageActions>
      <Button size="sm" @click="create"><Plus />新建密钥</Button>
    </PageActions>

    <LoadError v-if="loadError" :message="loadError" />

    <!-- 窄屏：卡片列表 -->
    <div v-if="showList" class="divide-y overflow-hidden rounded-lg border lg:hidden">
      <div v-for="k in keys" :key="k.id" class="flex items-start gap-3 p-4">
        <div class="min-w-0 flex-1 space-y-1.5">
          <div class="flex items-center gap-2">
            <button type="button" :class="['truncate text-sm', keyLink]" @click="detail = k">{{ k.name || '（未命名）' }}</button>
            <DotBadge :tone="badge(k).tone">{{ badge(k).label }}</DotBadge>
          </div>
          <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <span class="font-mono">{{ k.prefix }}…</span>
            <span>{{ k.expires_at ? `到期：${expiryText(k)}` : '不过期' }}</span>
            <span>{{ k.last_used_at ? `${fmtAgo(k.last_used_at)}使用` : '未使用' }}</span>
            <span v-if="k.recent.cost_nanos != null">近 30 天 {{ fmtCost(k.recent.cost_nanos) }}</span>
          </div>
          <div v-if="k.token_limit" class="flex items-center gap-2 text-xs">
            <QuotaMeter :value="usedRatio(k)" used class="w-24" />
            <span class="tabular-nums">{{ fmtCompact(k.period_used) }} / {{ fmtCompact(k.token_limit) }}</span>
            <span class="text-muted-foreground">{{ periodLabel(k) }}</span>
          </div>
          <div v-else class="text-xs text-muted-foreground">近 30 天 {{ fmtTokens(k.recent.tokens) }} Token</div>
        </div>
        <div class="-mr-1 flex shrink-0">
          <IconAction label="详情" @click="detail = k"><FileText /></IconAction>
          <IconAction :label="k.status === 'enabled' ? '停用' : '启用'" :tone="k.status === 'enabled' ? 'warning' : 'default'" :disabled="busy === k.id" @click="toggle(k)">
            <PowerOff v-if="k.status === 'enabled'" /><Power v-else />
          </IconAction>
          <IconAction label="删除" tone="danger" :disabled="busy === k.id" @click="remove(k)">
            <Trash2 />
          </IconAction>
        </div>
      </div>
      <p v-if="!keys.length" class="px-4 py-10 text-center text-sm text-muted-foreground">还没有密钥。</p>
    </div>

    <!-- 宽屏：表格 -->
    <div v-if="showList" class="hidden overflow-x-auto rounded-lg border lg:block">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead class="w-full pl-4">名称</TableHead>
            <TableHead>状态</TableHead>
            <TableHead>Token 用量</TableHead>
            <TableHead class="text-right whitespace-nowrap">近 30 天费用</TableHead>
            <TableHead>到期</TableHead>
            <TableHead>最近使用</TableHead>
            <TableHead class="px-4 text-center">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow v-for="k in keys" :key="k.id">
            <TableCell class="pl-4">
              <button type="button" :class="['block', keyLink]" @click="detail = k">{{ k.name || '（未命名）' }}</button>
              <div class="font-mono text-xs text-muted-foreground">{{ k.prefix }}…</div>
            </TableCell>
            <TableCell><DotBadge :tone="badge(k).tone">{{ badge(k).label }}</DotBadge></TableCell>
            <TableCell>
              <!-- 设了上限：额度条（已用部分）加已用 / 上限；没设：近 30 天用量 -->
              <div v-if="k.token_limit" class="space-y-1 text-xs">
                <QuotaMeter :value="usedRatio(k)" used class="w-32" />
                <div class="flex gap-1.5 tabular-nums">
                  <span>{{ fmtCompact(k.period_used) }} / {{ fmtCompact(k.token_limit) }}</span>
                  <span class="text-muted-foreground">{{ periodLabel(k) }}</span>
                </div>
              </div>
              <span v-else class="text-muted-foreground">近 30 天 {{ fmtTokens(k.recent.tokens) }}</span>
            </TableCell>
            <TableCell :class="['text-right tabular-nums', k.recent.cost_nanos == null && 'text-muted-foreground']">{{ fmtCost(k.recent.cost_nanos) }}</TableCell>
            <TableCell :class="k.expired ? 'text-destructive' : 'text-muted-foreground'" :title="k.expires_at ? fmtTime(k.expires_at) : undefined">
              {{ expiryText(k) }}
            </TableCell>
            <TableCell class="text-muted-foreground">{{ k.last_used_at ? fmtAgo(k.last_used_at) : '未使用' }}</TableCell>
            <TableCell class="px-4">
              <div class="flex justify-center gap-0.5">
                <IconAction label="详情" @click="detail = k"><FileText /></IconAction>
                <IconAction :label="k.status === 'enabled' ? '停用' : '启用'" :tone="k.status === 'enabled' ? 'warning' : 'default'" :disabled="busy === k.id" @click="toggle(k)">
                  <PowerOff v-if="k.status === 'enabled'" /><Power v-else />
                </IconAction>
                <IconAction label="删除" tone="danger" :disabled="busy === k.id" @click="remove(k)">
                  <Trash2 />
                </IconAction>
              </div>
            </TableCell>
          </TableRow>
          <TableEmpty v-if="!keys.length" :colspan="7">还没有密钥。</TableEmpty>
        </TableBody>
      </Table>
    </div>

    <!-- 编辑弹窗会从详情面板里打开，要盖在面板上面：两者都传送到 body、层级相同，后挂载的在上，所以弹窗放后面 -->
    <KeySheet v-model:api-key="detail" @edit="edit" />
    <KeyFormDialog v-model:open="formOpen" :editing="editing" @saved="load" />
  </div>
</template>
