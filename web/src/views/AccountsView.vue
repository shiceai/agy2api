<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Check, FileText, Minus, Network, RefreshCw, Trash2, Plus, Power, PowerOff } from '@lucide/vue'
import { toast, type ExternalToast } from 'vue-sonner'
import { api, isUnauthorized, errorMessage, ApiError, type Account, type AccountList, type Proxy, type WarmupState } from '@/api/client'
import { fmtAgo } from '@/lib/utils'
import { stickyActionCell, stickyActionHead, useColumnPrefs, type ColumnDef } from '@/lib/columns'
import { useLive } from '@/lib/live'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Table, TableBody, TableCell, TableEmpty, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import ColumnSettings from '@/components/ColumnSettings.vue'
import CooldownBadge from '@/components/CooldownBadge.vue'
import LoadError from '@/components/LoadError.vue'
import PageActions from '@/components/PageActions.vue'
import PoolPauseAlert from '@/components/PoolPauseAlert.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import WarmupBadge from '@/components/WarmupBadge.vue'
import IconAction from '@/components/IconAction.vue'
import PlanBadge from '@/components/PlanBadge.vue'
import QuotaSummary from '@/components/QuotaSummary.vue'
import AddAccountDialog from '@/components/accounts/AddAccountDialog.vue'
import AccountSheet from '@/components/accounts/AccountSheet.vue'
import AccountProxy from '@/components/accounts/AccountProxy.vue'
import AssignProxyDialog from '@/components/accounts/AssignProxyDialog.vue'
import SelectionBar from '@/components/SelectionBar.vue'

const router = useRouter()
const confirm = useConfirm()

// 从 UA（antigravity/hub/<版本> (...)）里取出客户端版本号，取不到时原样显示。
function hubVersion(ua: string): string {
  return /antigravity\/hub\/(\S+)/.exec(ua ?? '')?.[1] ?? (ua || '—')
}
const accounts = ref<Account[]>([])
// 宽屏表格的可选列：显示哪些、什么顺序在"显示列"里设置，存在浏览器本地。勾选框、账号和操作列固定。
// 名称（详情顶部有）、项目、客户端默认隐藏，常见宽度下表格放得下。
const columnDefs: ColumnDef[] = [
  { key: 'name', label: '名称', defaultVisible: false },
  { key: 'plan', label: '套餐', defaultVisible: true },
  { key: 'quota', label: '额度', defaultVisible: true },
  { key: 'project', label: '项目', defaultVisible: false },
  { key: 'client', label: '客户端', defaultVisible: false },
  { key: 'models', label: '模型', defaultVisible: true },
  { key: 'concurrency', label: '并发', defaultVisible: true },
  { key: 'status', label: '状态', defaultVisible: true },
  { key: 'proxy', label: '代理', defaultVisible: true },
  { key: 'synced', label: '最近同步', defaultVisible: true },
]
const columns = useColumnPrefs('agy2api_accounts_columns_v1', columnDefs)
// 实时并发：轮询 /api/admin/live，账号列显示"当前 / 上限"。离开页面自动停。
const live = useLive()
// 账号此刻进行中的生成请求数：/live 取到后以它为准（没有的账号就是 0），还没取到时用列表里带的快照。
function inFlightOf(a: Account): number {
  return live.loaded.value ? (live.inFlight.value[String(a.id)] ?? 0) : (a.in_flight ?? 0)
}
// 全局默认并发上限（设置页 → 账号调度），账号没单独设置时用它。
const defaultConcurrency = ref(0)
// 账号此刻的新号预热情况：/live 取到后以它为准（不在表里就是没在预热），还没取到时用列表里带的快照。
function warmupOf(a: Account): WarmupState | undefined {
  return live.loaded.value ? live.warmup.value[String(a.id)] : a.warmup
}
// 账号此刻生效的并发上限：预热中是压低后的上限。
function limitOf(a: Account): number {
  return warmupOf(a)?.limit ?? (a.max_concurrency || defaultConcurrency.value)
}
// 避让阈值（百分比，设置页 → 账号调度 → 额度），额度弹窗里标出低于它的窗口。
const lowQuota = ref(0)
// 后台刷新额度的间隔（秒），用来提示额度可能已过期；每周额度的全局开关，账号跟随全局时显示。
const quotaRefresh = ref(0)
const weeklyQuota = ref(false)
// 列表加载失败的原因，显示在内容区顶部；操作结果用 Toast。
const loadError = ref('')
const busy = ref<number | null>(null)
// 加载失败又没有数据时只显示失败提示，不显示"还没有账号"。
const showList = computed(() => !loadError.value || accounts.value.length > 0)

const showAdd = ref(false)
// 代理列表：添加账号、分配代理、详情改绑时选。
const proxies = ref<Proxy[]>([])
const showAssign = ref(false)
const addTab = ref<'google' | 'import'>('google')
// refresh token 已失效的账号重新走一遍 Google 登录：邮箱相同会换上新凭据，并重新启用。
function reauthorize() {
  addTab.value = 'google'
  showAdd.value = true
}
// 同步时 refresh token 已失效（账号已被停用），提示里给"重新授权"按钮，多停留一会儿。
function reauthAction(e: unknown): ExternalToast {
  if (!(e instanceof ApiError) || e.code !== 'invalid_grant') return {}
  return { action: { label: '重新授权', onClick: reauthorize }, duration: 15000 }
}
// 详情面板里的账号；列表刷新后换成新数据，账号被删除时关闭。点邮箱打开，邮箱悬停出下划线。
const detail = ref<Account | null>(null)
const emailLink =
  'cursor-pointer rounded-sm underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50'


async function loadProxies() {
  try {
    proxies.value = (await api.get<{ proxies: Proxy[] }>('/api/admin/proxies')).proxies ?? []
  } catch {
    // 代理列表只在选代理时用，取不到不影响账号列表
  }
}
async function load() {
  loadProxies()
  try {
    const res = await api.get<AccountList>('/api/admin/accounts')
    accounts.value = res.accounts ?? []
    defaultConcurrency.value = res.default_concurrency
    lowQuota.value = res.low_quota ?? 0
    quotaRefresh.value = res.quota_refresh ?? 0
    weeklyQuota.value = !!res.weekly_quota
    loadError.value = ''
    // 已经不存在的账号（被删除）从勾选里去掉。
    const ids = new Set(accounts.value.map((a) => a.id))
    selected.value = new Set([...selected.value].filter((id) => ids.has(id)))
    if (detail.value) detail.value = accounts.value.find((a) => a.id === detail.value?.id) ?? null
  } catch (e) {
    loadError.value = errorMessage(e)
  }
}
onMounted(load)

// act 执行单个账号的操作：失败时 Toast 提示（标题带上账号），成功的启停、删除看列表变化即可。
// 失败后也刷新列表：同步遇到 refresh token 失效时后端已停用账号。
async function act(a: Account, what: string, fn: () => Promise<unknown>) {
  busy.value = a.id
  try {
    await fn()
  } catch (e) {
    toastError(`${what}失败：${a.email}`, e, reauthAction(e))
  } finally {
    busy.value = null
  }
  await load()
}

interface SyncResult {
  account: Account
  warning?: string
}
// 同步成功也提示一下：同步不一定改变列表上看得见的内容。
async function sync(id: number) {
  const a = accounts.value.find((x) => x.id === id)
  if (!a) return
  await act(a, '同步', async () => {
    const res = await api.post<SyncResult>(`/api/admin/accounts/${id}/sync`)
    if (res.warning) toast.warning(`已同步 ${a.email}，但模型目录没取到`, { description: res.warning, duration: 8000 })
    else toast.success(`已同步 ${a.email}`, { description: `${res.account.model_count} 个模型` })
  })
}
const toggle = (a: Account) =>
  act(a, a.status === 'enabled' ? '停用' : '启用', () =>
    api.post(`/api/admin/accounts/${a.id}/${a.status === 'enabled' ? 'disable' : 'enable'}`),
  )
async function remove(a: Account) {
  const ok = await confirm({
    title: `删除账号 ${a.email}？`,
    description: '删除后号池不再使用它，调用记录保留。要恢复需要重新添加。',
    confirmText: '删除',
    destructive: true,
  })
  if (ok) act(a, '删除', () => api.del(`/api/admin/accounts/${a.id}`))
}

// --- 批量操作 ---

// 勾选的账号 ID；表头勾选框部分勾选时显示为横杠。
const selected = ref(new Set<number>())
const allChecked = computed<boolean | 'indeterminate'>(() => {
  const n = selected.value.size
  if (!n) return false
  return n === accounts.value.length ? true : 'indeterminate'
})
function checkAll(on: boolean | 'indeterminate') {
  selected.value = on === true ? new Set(accounts.value.map((a) => a.id)) : new Set()
}
function check(id: number, on: boolean | 'indeterminate') {
  const next = new Set(selected.value)
  if (on === true) next.add(id)
  else next.delete(id)
  selected.value = next
}
const clearSelection = () => (selected.value = new Set())

// batchBusy 是正在进行的批量操作，syncProgress 是批量同步的进度（如 "2/5"）。
const batchBusy = ref<'' | 'enable' | 'disable' | 'delete' | 'sync'>('')
const syncProgress = ref('')
async function batch(action: 'enable' | 'disable' | 'delete') {
  const ids = [...selected.value]
  if (action === 'delete') {
    const ok = await confirm({
      title: `删除选中的 ${ids.length} 个账号？`,
      description: '删除后号池不再使用它们，调用记录保留。要恢复需要重新添加。',
      confirmText: '删除',
      destructive: true,
    })
    if (!ok) return
  }
  batchBusy.value = action
  try {
    await api.post('/api/admin/accounts/batch', { action, ids })
    if (action === 'delete') clearSelection()
  } catch (e) {
    toastError(`批量${batchLabels[action]}失败`, e)
  } finally {
    batchBusy.value = ''
  }
  await load()
}
const batchLabels = { enable: '启用', disable: '停用', delete: '删除' } as const

// 批量同步要请求上游：逐个同步，间隔 2 秒，结束后给一条汇总提示。
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
async function batchSync() {
  const list = accounts.value.filter((a) => selected.value.has(a.id))
  const failed: { email: string; error: unknown }[] = []
  batchBusy.value = 'sync'
  try {
    for (const [i, a] of list.entries()) {
      if (i > 0) await sleep(2000)
      syncProgress.value = `${i + 1}/${list.length}`
      busy.value = a.id
      try {
        await api.post(`/api/admin/accounts/${a.id}/sync`)
      } catch (e) {
        if (isUnauthorized(e)) return
        failed.push({ email: a.email, error: e })
      }
    }
  } finally {
    busy.value = null
    batchBusy.value = ''
    syncProgress.value = ''
  }
  await load()
  toastBatchSync(list.length, failed)
}
// 汇总提示：全部成功只报数量；有失败的逐个列出原因（最多 5 个），有 refresh token 失效的给"重新授权"。
function toastBatchSync(total: number, failed: { email: string; error: unknown }[]) {
  if (!failed.length) {
    toast.success(`已同步 ${total} 个账号`)
    return
  }
  const lines = failed.slice(0, 5).map((f) => `${f.email}：${(f.error as Error).message}`)
  if (failed.length > 5) lines.push(`另有 ${failed.length - 5} 个失败`)
  const revoked = failed.find((f) => f.error instanceof ApiError && f.error.code === 'invalid_grant')
  const options: ExternalToast = {
    description: lines.join('\n'),
    duration: 12000,
    classes: { description: 'whitespace-pre-line' },
    ...(revoked ? reauthAction(revoked.error) : {}),
  }
  if (failed.length === total) toast.error(`${total} 个账号同步失败`, options)
  else toast.warning(`同步完成：成功 ${total - failed.length} 个，失败 ${failed.length} 个`, options)
}
</script>

<template>
  <div class="space-y-6">
    <PageActions>
      <!-- 显示列只对表格有用：和表格同一个容器查询，卡片布局时不显示 -->
      <div class="hidden @min-[68rem]/main:block"><ColumnSettings v-model="columns" :defs="columnDefs" /></div>
      <Button size="sm" @click="showAdd = true"><Plus />添加账号</Button>
    </PageActions>

    <LoadError v-if="loadError" :message="loadError" />
    <PoolPauseAlert v-if="live.poolPause.value" :pause="live.poolPause.value" @resumed="live.refresh()" />

    <!-- 内容区放不下默认列时：卡片列表。按内容区宽度（容器查询）切换，侧边栏收起展开都准；
         默认列实测约需 67rem，阈值 68rem（改默认列时重新量） -->
    <div v-if="showList" class="divide-y overflow-hidden rounded-lg border @min-[68rem]/main:hidden">
      <div v-for="a in accounts" :key="a.id" :class="['flex items-start gap-3 p-4', selected.has(a.id) && 'bg-muted/50']">
        <Checkbox
          :model-value="selected.has(a.id)"
          :aria-label="`选择 ${a.email}`"
          class="mt-0.5"
          @update:model-value="(v) => check(a.id, v)"
        />
        <div class="min-w-0 flex-1 space-y-1.5">
          <button type="button" :class="['block max-w-full truncate text-left text-sm', emailLink]" @click="detail = a">{{ a.email }}</button>
          <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <StatusBadge :enabled="a.status === 'enabled'" />
            <WarmupBadge v-if="warmupOf(a)" :state="warmupOf(a)!" />
            <CooldownBadge v-if="a.cooldowns?.length" :cooldowns="a.cooldowns" />
            <span v-if="a.subscription" class="text-foreground"><PlanBadge :subscription="a.subscription" /></span>
            <span>{{ a.model_count }} 个模型</span>
            <span>
              并发
              <span :class="inFlightOf(a) ? 'text-foreground tabular-nums' : 'tabular-nums'">{{ inFlightOf(a) }}</span>
              / {{ warmupOf(a) ? limitOf(a) : a.max_concurrency || `${defaultConcurrency}（默认）` }}
            </span>
            <span>{{ a.synced_at ? `同步于 ${fmtAgo(a.synced_at)}` : '未同步' }}</span>
            <span class="inline-flex items-center gap-1">代理 <AccountProxy :account="a" /></span>
          </div>
          <QuotaSummary :account="a" :low-quota="lowQuota" :quota-refresh="quotaRefresh" row />
          <!-- 手机上没有悬停，停用原因直接显示 -->
          <div v-if="a.status !== 'enabled' && a.disabled_reason" class="text-xs text-muted-foreground">停用原因：{{ a.disabled_reason }}</div>
          <div class="truncate text-xs text-muted-foreground" :title="a.user_agent">
            {{ a.project_id || '—' }} · hub {{ hubVersion(a.user_agent) }}
          </div>
        </div>
        <div class="-mr-1 flex shrink-0">
          <IconAction label="详情" @click="detail = a"><FileText /></IconAction>
          <IconAction label="同步" :disabled="busy === a.id" @click="sync(a.id)">
            <RefreshCw :class="busy === a.id ? 'animate-spin' : ''" />
          </IconAction>
          <IconAction :label="a.status === 'enabled' ? '停用' : '启用'" :tone="a.status === 'enabled' ? 'warning' : 'default'" :disabled="busy === a.id" @click="toggle(a)">
            <PowerOff v-if="a.status === 'enabled'" /><Power v-else />
          </IconAction>
          <IconAction label="删除" tone="danger" :disabled="busy === a.id" @click="remove(a)">
            <Trash2 />
          </IconAction>
        </div>
      </div>
      <p v-if="!accounts.length" class="px-4 py-10 text-center text-sm text-muted-foreground">还没有账号，点击上方"添加账号"。</p>
    </div>

    <!-- 内容区放得下默认列时：表格。列可在"显示列"里设置，开得多放不下时横向滚动，操作列固定在最右 -->
    <div v-if="showList" class="hidden overflow-x-auto rounded-lg border @min-[68rem]/main:block">
      <Table>
        <TableHeader>
          <TableRow>
            <!-- 勾选列（shadcn 数据表格的做法）：表头全选，部分勾选时显示横杠 -->
            <TableHead class="w-px pr-0 pl-4">
              <Checkbox
                :model-value="allChecked"
                aria-label="全选"
                :disabled="!accounts.length"
                class="data-[state=indeterminate]:border-primary data-[state=indeterminate]:bg-primary data-[state=indeterminate]:text-primary-foreground dark:data-[state=indeterminate]:bg-primary"
                @update:model-value="checkAll"
              >
                <Minus v-if="allChecked === 'indeterminate'" /><Check v-else />
              </Checkbox>
            </TableHead>
            <TableHead class="w-full">账号</TableHead>
            <template v-for="key in columns.visible" :key="key">
              <TableHead v-if="key === 'name'">名称</TableHead>
              <TableHead v-else-if="key === 'plan'">套餐</TableHead>
              <TableHead v-else-if="key === 'quota'">额度</TableHead>
              <TableHead v-else-if="key === 'project'">项目</TableHead>
              <TableHead v-else-if="key === 'client'">客户端</TableHead>
              <TableHead v-else-if="key === 'models'" class="text-right">模型</TableHead>
              <TableHead v-else-if="key === 'concurrency'" class="text-right">并发</TableHead>
              <TableHead v-else-if="key === 'status'">状态</TableHead>
              <TableHead v-else-if="key === 'proxy'">代理</TableHead>
              <TableHead v-else-if="key === 'synced'">最近同步</TableHead>
            </template>
            <TableHead :class="['px-4 text-center', stickyActionHead]">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow v-for="a in accounts" :key="a.id" class="group" :data-state="selected.has(a.id) ? 'selected' : undefined">
            <TableCell class="pr-0 pl-4">
              <Checkbox :model-value="selected.has(a.id)" :aria-label="`选择 ${a.email}`" @update:model-value="(v) => check(a.id, v)" />
            </TableCell>
            <TableCell>
              <button type="button" :class="['text-left', emailLink]" @click="detail = a">{{ a.email }}</button>
            </TableCell>
            <template v-for="key in columns.visible" :key="key">
              <TableCell v-if="key === 'name'" class="text-muted-foreground">{{ a.name || '—' }}</TableCell>
              <TableCell v-else-if="key === 'plan'"><PlanBadge :subscription="a.subscription" /></TableCell>
              <TableCell v-else-if="key === 'quota'" class="py-1.5"><QuotaSummary :account="a" :low-quota="lowQuota" :quota-refresh="quotaRefresh" /></TableCell>
              <TableCell v-else-if="key === 'project'" class="text-muted-foreground">{{ a.project_id || '—' }}</TableCell>
              <TableCell v-else-if="key === 'client'" class="text-muted-foreground" :title="a.user_agent">hub {{ hubVersion(a.user_agent) }}</TableCell>
              <TableCell v-else-if="key === 'models'" class="text-right tabular-nums">{{ a.model_count }}</TableCell>
              <!-- 并发：当前进行中 / 生效中的上限（新号预热中是压低后的上限）；当前为 0 时灰色，上限始终灰色。
                   悬停说明，预热中写出正常上限，上限跟随全局默认时提示可单独设置 -->
              <TableCell v-else-if="key === 'concurrency'" class="text-right tabular-nums">
                <Tooltip>
                  <TooltipTrigger as-child>
                    <span class="cursor-default">
                      <span :class="inFlightOf(a) ? undefined : 'text-muted-foreground'">{{ inFlightOf(a) }}</span>
                      <span class="text-muted-foreground"> / {{ limitOf(a) }}</span>
                    </span>
                  </TooltipTrigger>
                  <TooltipContent>
                    进行中 {{ inFlightOf(a) }} 个，并发上限 {{ limitOf(a) }}<template v-if="warmupOf(a)">（新号预热中，正常 {{ warmupOf(a)!.normal }}）</template><template v-else-if="!a.max_concurrency">（跟随全局默认，可在详情里单独设置）</template>
                  </TooltipContent>
                </Tooltip>
              </TableCell>
              <!-- 状态：启用/停用，预热中、冷却中时附加徽章。多个徽章竖着叠放（额度列本来就是两行），不把表格撑宽 -->
              <TableCell v-else-if="key === 'status'">
                <div class="flex flex-col items-start gap-1">
                  <StatusBadge :enabled="a.status === 'enabled'" :reason="a.disabled_reason" />
                  <WarmupBadge v-if="warmupOf(a)" :state="warmupOf(a)!" />
                  <CooldownBadge v-if="a.cooldowns?.length" :cooldowns="a.cooldowns" />
                </div>
              </TableCell>
              <TableCell v-else-if="key === 'proxy'"><AccountProxy :account="a" /></TableCell>
              <TableCell v-else-if="key === 'synced'" class="text-muted-foreground">{{ fmtAgo(a.synced_at) }}</TableCell>
            </template>
            <TableCell :class="['px-4', stickyActionCell]">
              <div class="flex justify-center gap-0.5">
                <IconAction label="详情" @click="detail = a"><FileText /></IconAction>
                <IconAction label="同步" :disabled="busy === a.id" @click="sync(a.id)">
                  <RefreshCw :class="busy === a.id ? 'animate-spin' : ''" />
                </IconAction>
                <IconAction :label="a.status === 'enabled' ? '停用' : '启用'" :tone="a.status === 'enabled' ? 'warning' : 'default'" :disabled="busy === a.id" @click="toggle(a)">
                  <PowerOff v-if="a.status === 'enabled'" /><Power v-else />
                </IconAction>
                <IconAction label="删除" tone="danger" :disabled="busy === a.id" @click="remove(a)">
                  <Trash2 />
                </IconAction>
              </div>
            </TableCell>
          </TableRow>
          <!-- 勾选框、账号、操作三列固定，加上显示的可选列 -->
          <TableEmpty v-if="!accounts.length" :colspan="columns.visible.length + 3">还没有账号，点击右上角"添加账号"。</TableEmpty>
        </TableBody>
      </Table>
    </div>

    <!-- 有勾选时顶栏换成选择栏。卡片列表没有表头勾选框，"全选"只在卡片布局显示（顶栏也是 @container/main，同一个切换点）；
         手机上操作按钮只显示图标（文字留给读屏），一行放得下 -->
    <SelectionBar :count="selected.size" @clear="clearSelection">
      <Button v-if="allChecked !== true" variant="ghost" size="sm" class="@min-[68rem]/main:hidden" :disabled="!!batchBusy" @click="checkAll(true)">
        全选
      </Button>
      <Button variant="ghost" size="sm" class="max-sm:px-2" :disabled="!!batchBusy" @click="batch('enable')">
        <Power /><span class="max-sm:sr-only">启用</span>
      </Button>
      <Button variant="ghost" size="sm" class="max-sm:px-2" :disabled="!!batchBusy" @click="batch('disable')">
        <PowerOff /><span class="max-sm:sr-only">停用</span>
      </Button>
      <Button variant="ghost" size="sm" class="max-sm:px-2" :disabled="!!batchBusy" @click="batchSync">
        <RefreshCw :class="batchBusy === 'sync' && 'animate-spin'" />
        <span v-if="batchBusy === 'sync'" class="tabular-nums"><span class="max-sm:hidden">同步中 </span>{{ syncProgress }}</span>
        <span v-else class="max-sm:sr-only">同步</span>
      </Button>
      <Button variant="ghost" size="sm" class="max-sm:px-2" :disabled="!!batchBusy" @click="showAssign = true">
        <Network /><span class="max-sm:sr-only">分配代理</span>
      </Button>
      <Button
        variant="ghost"
        size="sm"
        class="text-destructive hover:bg-destructive/10 hover:text-destructive max-sm:px-2"
        :disabled="!!batchBusy"
        @click="batch('delete')"
      >
        <Trash2 /><span class="max-sm:sr-only">删除</span>
      </Button>
    </SelectionBar>

    <AddAccountDialog v-model:open="showAdd" v-model:tab="addTab" :proxies="proxies" @added="load" />
    <AssignProxyDialog v-model:open="showAssign" :ids="[...selected]" :proxies="proxies" @done="load" />
    <AccountSheet
      v-model:account="detail"
      :low-quota="lowQuota"
      :quota-refresh="quotaRefresh"
      :weekly-quota="weeklyQuota"
      :default-concurrency="defaultConcurrency"
      :in-flight="detail ? inFlightOf(detail) : 0"
      :syncing="!!detail && busy === detail.id"
      :proxies="proxies"
      @sync="sync"
      @changed="load"
    />
  </div>
</template>
