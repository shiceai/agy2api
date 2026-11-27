<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Check, Lock, Minus, Pencil, Plus, Power, PowerOff, Trash2, Users, Wifi } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { api, isUnauthorized, errorMessage, type Proxy } from '@/api/client'
import { stickyActionCell, stickyActionHead, useColumnPrefs, type ColumnDef } from '@/lib/columns'
import { useConfirm } from '@/lib/confirm'
import { latencyTone, proxyAddress, proxyIssue, regionText, sameEgress } from '@/lib/proxy'
import { toastError } from '@/lib/toast'
import { cn, fmtAgo, fmtTime } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Table, TableBody, TableCell, TableEmpty, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import ColumnSettings from '@/components/ColumnSettings.vue'
import DotBadge from '@/components/DotBadge.vue'
import IconAction from '@/components/IconAction.vue'
import LoadError from '@/components/LoadError.vue'
import PageActions from '@/components/PageActions.vue'
import SelectionBar from '@/components/SelectionBar.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import ProxyAddDialog from '@/components/proxies/ProxyAddDialog.vue'
import ProxyEditDialog from '@/components/proxies/ProxyEditDialog.vue'

// 代理：账号绑定代理后，它的所有请求都经代理出网。这里添加、测试（服务器发起：出口、到代理和到 Google 的延迟）、
// 改名换地址、启停、删除（还有账号绑定的不能删）。停用的代理不参与自动分配，绑定它的账号暂停调度。
const router = useRouter()
const confirm = useConfirm()
const proxies = ref<Proxy[]>([])
// 宽屏表格的可选列：显示哪些、什么顺序在"显示列"里设置，存在浏览器本地。勾选框、名称和操作列固定。
const columnDefs: ColumnDef[] = [
  { key: 'address', label: '地址', defaultVisible: true },
  { key: 'status', label: '状态', defaultVisible: true },
  { key: 'egressIp', label: '出口 IP', defaultVisible: true },
  { key: 'region', label: '地区', defaultVisible: true },
  { key: 'connect', label: '到代理', defaultVisible: true },
  { key: 'latency', label: '到 Google', defaultVisible: true },
  { key: 'accounts', label: '账号', defaultVisible: true },
  { key: 'checkedAt', label: '测试时间', defaultVisible: false },
]
const columns = useColumnPrefs('agy2api_proxies_columns_v1', columnDefs)
const loadError = ref('')
// testing 是正在测试的代理 ID。
const testing = ref(new Set<number>())
const busy = ref(false)

const showAdd = ref(false)
const editing = ref<Proxy | null>(null)
const showEdit = ref(false)


async function load() {
  try {
    const res = await api.get<{ proxies: Proxy[] }>('/api/admin/proxies')
    proxies.value = res.proxies ?? []
    loadError.value = ''
    const ids = new Set(proxies.value.map((p) => p.id))
    selected.value = new Set([...selected.value].filter((id) => ids.has(id)))
  } catch (e) {
    loadError.value = errorMessage(e)
  }
}
onMounted(load)
const showList = computed(() => !loadError.value || proxies.value.length > 0)

const groups = computed(() => sameEgress(proxies.value))
const issueOf = (p: Proxy) => proxyIssue(p, groups.value)
// msOf 取延迟列的值：connect 是到代理，latency 是到 Google。
function msOf(p: Proxy, key: string): number {
  return key === 'connect' ? p.connect_ms : p.latency_ms
}

// 测试：出口信息和延迟，结果直接反映在列表里；失败的原因悬停状态看。
async function test(ids: number[]) {
  testing.value = new Set([...testing.value, ...ids])
  try {
    const res = await api.post<{ proxies: Proxy[] }>('/api/admin/proxies/check', { ids })
    proxies.value = res.proxies ?? []
  } catch (e) {
    toastError('测试代理失败', e)
  } finally {
    const next = new Set(testing.value)
    for (const id of ids) next.delete(id)
    testing.value = next
  }
}
function onAdded(ids: number[]) {
  load().then(() => test(ids))
}
function edit(p: Proxy) {
  editing.value = p
  showEdit.value = true
}
function onSaved(list: Proxy[], addressChanged: boolean) {
  proxies.value = list
  if (addressChanged && editing.value) test([editing.value.id])
}

// 启停：停用还有账号绑定的代理前确认，这些账号会暂停调度。
async function setStatus(list: Proxy[], enable: boolean) {
  if (!enable) {
    const bound = list.reduce((n, p) => n + p.accounts.length, 0)
    if (bound) {
      const ok = await confirm({
        title: list.length === 1 ? `停用代理 ${list[0].name}？` : `停用选中的 ${list.length} 个代理？`,
        description: `绑定的 ${bound} 个账号会暂停调度（不会改成直连），启用后恢复。`,
        confirmText: '停用',
      })
      if (!ok) return
    }
  }
  busy.value = true
  try {
    const res = await api.post<{ proxies: Proxy[] }>(`/api/admin/proxies/${enable ? 'enable' : 'disable'}`, { ids: list.map((p) => p.id) })
    proxies.value = res.proxies ?? []
  } catch (e) {
    toastError(`${enable ? '启用' : '停用'}失败`, e)
  } finally {
    busy.value = false
  }
}

async function remove(p: Proxy) {
  const ok = await confirm({
    title: `删除代理 ${p.name}？`,
    description: '删除后不能恢复，要用需要重新添加。',
    confirmText: '删除',
    destructive: true,
  })
  if (!ok) return
  try {
    const res = await api.del<{ proxies: Proxy[] }>(`/api/admin/proxies/${p.id}`)
    proxies.value = res.proxies ?? []
  } catch (e) {
    toastError(`删除失败：${p.name}`, e)
  }
}

// --- 批量操作 ---

const selected = ref(new Set<number>())
const selectedList = computed(() => proxies.value.filter((p) => selected.value.has(p.id)))
const allChecked = computed<boolean | 'indeterminate'>(() => {
  const n = selected.value.size
  if (!n) return false
  return n === proxies.value.length ? true : 'indeterminate'
})
function checkAll(on: boolean | 'indeterminate') {
  selected.value = on === true ? new Set(proxies.value.map((p) => p.id)) : new Set()
}
function pick(id: number, on: boolean | 'indeterminate') {
  const next = new Set(selected.value)
  if (on === true) next.add(id)
  else next.delete(id)
  selected.value = next
}
const clearSelection = () => (selected.value = new Set())

// 批量删除：还有账号绑定的删不掉，结束后汇总提示。
async function batchDelete() {
  const list = selectedList.value
  const ok = await confirm({
    title: `删除选中的 ${list.length} 个代理？`,
    description: '还有账号绑定的代理会跳过。删除后不能恢复。',
    confirmText: '删除',
    destructive: true,
  })
  if (!ok) return
  busy.value = true
  const failed: string[] = []
  try {
    for (const p of list) {
      try {
        await api.del(`/api/admin/proxies/${p.id}`)
      } catch (e) {
        if (isUnauthorized(e)) return
        failed.push(`${p.name}：${(e as Error).message}`)
      }
    }
  } finally {
    busy.value = false
  }
  await load()
  if (failed.length) {
    toast.warning(`删除了 ${list.length - failed.length} 个，${failed.length} 个没删`, {
      description: failed.slice(0, 5).join('\n'),
      duration: 10000,
      classes: { description: 'whitespace-pre-line' },
    })
  }
}
</script>

<template>
  <div class="space-y-6">
    <PageActions>
      <!-- 显示列只对表格有用：和表格同一个容器查询，卡片布局时不显示 -->
      <div class="hidden @min-[74rem]/main:block"><ColumnSettings v-model="columns" :defs="columnDefs" /></div>
      <Button size="sm" @click="showAdd = true"><Plus />添加代理</Button>
    </PageActions>

    <LoadError v-if="loadError" :message="loadError" />

    <!-- 内容区放不下默认列时：卡片列表。按内容区宽度（容器查询）切换，侧边栏收起展开都准；
         默认列实测约需 74rem（测试时间默认隐藏才放得下），阈值 74rem（改默认列时重新量） -->
    <div v-if="showList" class="divide-y overflow-hidden rounded-lg border @min-[74rem]/main:hidden">
      <div v-for="p in proxies" :key="p.id" :class="['flex items-start gap-3 p-4', selected.has(p.id) && 'bg-muted/50']">
        <Checkbox :model-value="selected.has(p.id)" :aria-label="`选择 ${p.name}`" class="mt-0.5" @update:model-value="(v) => pick(p.id, v)" />
        <div class="min-w-0 flex-1 space-y-1">
          <div class="flex flex-wrap items-center gap-1.5">
            <span class="mr-0.5 text-sm break-all">{{ p.name }}</span>
            <StatusBadge :enabled="p.status === 'enabled'" />
            <DotBadge v-if="issueOf(p)" :tone="issueOf(p)!.tone">{{ issueOf(p)!.label }}</DotBadge>
          </div>
          <div class="flex items-center gap-1 text-xs break-all text-muted-foreground">
            <Lock v-if="p.username" class="size-3 shrink-0" />{{ proxyAddress(p) }}
          </div>
          <div class="flex flex-wrap gap-x-3 gap-y-1 text-xs tabular-nums">
            <span>{{ p.egress_ip || '—' }}</span>
            <span v-if="regionText(p)">{{ regionText(p) }}</span>
            <span v-if="p.connect_ms"><span class="text-muted-foreground">到代理 </span><span :class="latencyTone(p.connect_ms)">{{ p.connect_ms }} ms</span></span>
            <span v-if="p.latency_ms"><span class="text-muted-foreground">到 Google </span><span :class="latencyTone(p.latency_ms)">{{ p.latency_ms }} ms</span></span>
            <span class="text-muted-foreground">{{ p.checked_at ? `${fmtAgo(p.checked_at)}测试` : '未测试' }}</span>
          </div>
          <div v-if="issueOf(p)?.tip" class="text-xs text-muted-foreground">{{ issueOf(p)!.tip }}</div>
          <div class="text-xs break-all text-muted-foreground">
            {{ p.accounts.length ? p.accounts.map((a) => a.email).join('、') : '没有绑定账号' }}
          </div>
        </div>
        <div class="-mr-1 grid shrink-0 grid-cols-2">
          <IconAction label="测试" :disabled="testing.has(p.id)" @click="test([p.id])">
            <Wifi :class="testing.has(p.id) && 'animate-pulse'" />
          </IconAction>
          <IconAction label="编辑" @click="edit(p)"><Pencil /></IconAction>
          <IconAction
            :label="p.status === 'enabled' ? '停用' : '启用'"
            :tone="p.status === 'enabled' ? 'warning' : 'default'"
            :disabled="busy"
            @click="setStatus([p], p.status !== 'enabled')"
          >
            <PowerOff v-if="p.status === 'enabled'" /><Power v-else />
          </IconAction>
          <IconAction label="删除" tone="danger" @click="remove(p)"><Trash2 /></IconAction>
        </div>
      </div>
      <p v-if="!proxies.length" class="px-4 py-10 text-center text-sm text-muted-foreground">还没有代理，点击上方"添加代理"。</p>
    </div>

    <!-- 内容区放得下默认列时：表格。列可在"显示列"里设置，开得多放不下时横向滚动，操作列固定在最右 -->
    <div v-if="showList" class="hidden overflow-x-auto rounded-lg border @min-[74rem]/main:block">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead class="w-px pr-0 pl-4">
              <Checkbox
                :model-value="allChecked"
                aria-label="全选"
                :disabled="!proxies.length"
                class="data-[state=indeterminate]:border-primary data-[state=indeterminate]:bg-primary data-[state=indeterminate]:text-primary-foreground dark:data-[state=indeterminate]:bg-primary"
                @update:model-value="checkAll"
              >
                <Minus v-if="allChecked === 'indeterminate'" /><Check v-else />
              </Checkbox>
            </TableHead>
            <TableHead class="w-full">名称</TableHead>
            <template v-for="key in columns.visible" :key="key">
              <TableHead v-if="key === 'address'">地址</TableHead>
              <TableHead v-else-if="key === 'status'">状态</TableHead>
              <TableHead v-else-if="key === 'egressIp'">出口 IP</TableHead>
              <TableHead v-else-if="key === 'region'">地区</TableHead>
              <TableHead v-else-if="key === 'connect' || key === 'latency'" class="text-right whitespace-nowrap">
                <Tooltip>
                  <TooltipTrigger as-child>
                    <span class="cursor-help border-b border-dashed border-muted-foreground/40">{{ key === 'connect' ? '到代理' : '到 Google' }}</span>
                  </TooltipTrigger>
                  <TooltipContent>{{ key === 'connect' ? '服务器和代理建立 TCP 连接的耗时' : '服务器经代理请求 Google 的往返，不含建连和握手' }}</TooltipContent>
                </Tooltip>
              </TableHead>
              <TableHead v-else-if="key === 'accounts'" class="text-right">账号</TableHead>
              <TableHead v-else-if="key === 'checkedAt'">测试时间</TableHead>
            </template>
            <TableHead :class="['px-4 text-center', stickyActionHead]">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow v-for="p in proxies" :key="p.id" class="group" :data-state="selected.has(p.id) ? 'selected' : undefined">
            <TableCell class="pr-0 pl-4">
              <Checkbox :model-value="selected.has(p.id)" :aria-label="`选择 ${p.name}`" @update:model-value="(v) => pick(p.id, v)" />
            </TableCell>
            <TableCell>{{ p.name }}</TableCell>
            <template v-for="key in columns.visible" :key="key">
              <!-- 图标加文字用块级 flex 在单元格里居中，leading-none 让文字的框贴着字形，和图标中心对齐；
                   行内 flex 会拿图标底边对齐基线，整组偏高 -->
              <TableCell v-if="key === 'address'" class="whitespace-nowrap text-muted-foreground">
                <div class="flex items-center gap-1 leading-none">
                  <Tooltip v-if="p.username">
                    <TooltipTrigger as-child>
                      <Lock class="size-3.5 shrink-0" tabindex="0" aria-label="带用户名密码" />
                    </TooltipTrigger>
                    <TooltipContent>用户名 {{ p.username }}，密码不显示</TooltipContent>
                  </Tooltip>
                  {{ proxyAddress(p) }}
                </div>
              </TableCell>
              <TableCell v-else-if="key === 'status'">
                <div class="flex items-center gap-1.5">
                  <StatusBadge :enabled="p.status === 'enabled'" />
                  <Tooltip v-if="issueOf(p)?.tip">
                    <TooltipTrigger as-child>
                      <span class="cursor-default"><DotBadge :tone="issueOf(p)!.tone">{{ issueOf(p)!.label }}</DotBadge></span>
                    </TooltipTrigger>
                    <TooltipContent class="max-w-80">{{ issueOf(p)!.tip }}</TooltipContent>
                  </Tooltip>
                  <DotBadge v-else-if="issueOf(p)" :tone="issueOf(p)!.tone">{{ issueOf(p)!.label }}</DotBadge>
                </div>
              </TableCell>
              <TableCell v-else-if="key === 'egressIp'" class="whitespace-nowrap tabular-nums">{{ p.egress_ip || '—' }}</TableCell>
              <TableCell v-else-if="key === 'region'" class="whitespace-nowrap">
                <Tooltip v-if="p.egress_org">
                  <TooltipTrigger as-child>
                    <span class="cursor-default">{{ regionText(p) || '—' }}</span>
                  </TooltipTrigger>
                  <TooltipContent class="block">
                    <div>运营商：{{ p.egress_org }}</div>
                    <div v-if="p.egress_region">地区：{{ p.egress_region }}</div>
                  </TooltipContent>
                </Tooltip>
                <span v-else :class="!regionText(p) && 'text-muted-foreground'">{{ regionText(p) || '—' }}</span>
              </TableCell>
              <TableCell
                v-else-if="key === 'connect' || key === 'latency'"
                :class="cn('text-right whitespace-nowrap tabular-nums', msOf(p, key) ? latencyTone(msOf(p, key)) : 'text-muted-foreground')"
              >
                {{ msOf(p, key) ? `${msOf(p, key)} ms` : '—' }}
              </TableCell>
              <TableCell v-else-if="key === 'accounts'" class="text-right">
                <Tooltip v-if="p.accounts.length">
                  <TooltipTrigger as-child>
                    <div class="ml-auto flex w-fit cursor-default items-center gap-1 leading-none tabular-nums">
                      <Users class="size-3.5 text-muted-foreground" />{{ p.accounts.length }}
                    </div>
                  </TooltipTrigger>
                  <TooltipContent class="max-w-80">{{ p.accounts.map((a) => a.email).join('、') }}</TooltipContent>
                </Tooltip>
                <span v-else class="text-muted-foreground">—</span>
              </TableCell>
              <TableCell v-else-if="key === 'checkedAt'" class="whitespace-nowrap text-muted-foreground" :title="p.checked_at ? fmtTime(p.checked_at) : undefined">
                {{ p.checked_at ? fmtAgo(p.checked_at) : '—' }}
              </TableCell>
            </template>
            <TableCell :class="['px-4', stickyActionCell]">
              <div class="flex justify-center gap-0.5">
                <IconAction label="测试" :disabled="testing.has(p.id)" @click="test([p.id])">
                  <Wifi :class="testing.has(p.id) && 'animate-pulse'" />
                </IconAction>
                <IconAction label="编辑" @click="edit(p)"><Pencil /></IconAction>
                <IconAction
                  :label="p.status === 'enabled' ? '停用' : '启用'"
                  :tone="p.status === 'enabled' ? 'warning' : 'default'"
                  :disabled="busy"
                  @click="setStatus([p], p.status !== 'enabled')"
                >
                  <PowerOff v-if="p.status === 'enabled'" /><Power v-else />
                </IconAction>
                <IconAction label="删除" tone="danger" @click="remove(p)"><Trash2 /></IconAction>
              </div>
            </TableCell>
          </TableRow>
          <!-- 勾选框、名称、操作三列固定，加上显示的可选列 -->
          <TableEmpty v-if="!proxies.length" :colspan="columns.visible.length + 3">还没有代理，点击右上角"添加代理"。</TableEmpty>
        </TableBody>
      </Table>
    </div>

    <SelectionBar :count="selected.size" @clear="clearSelection">
      <!-- 卡片列表没有表头勾选框，"全选"只在卡片布局显示（顶栏也是 @container/main，同一个切换点） -->
      <Button v-if="allChecked !== true" variant="ghost" size="sm" class="@min-[74rem]/main:hidden" :disabled="busy" @click="checkAll(true)">全选</Button>
      <Button variant="ghost" size="sm" class="max-sm:px-2" :disabled="busy" @click="test([...selected])">
        <Wifi :class="[...selected].some((id) => testing.has(id)) && 'animate-pulse'" /><span class="max-sm:sr-only">测试</span>
      </Button>
      <Button variant="ghost" size="sm" class="max-sm:px-2" :disabled="busy" @click="setStatus(selectedList, true)">
        <Power /><span class="max-sm:sr-only">启用</span>
      </Button>
      <Button variant="ghost" size="sm" class="max-sm:px-2" :disabled="busy" @click="setStatus(selectedList, false)">
        <PowerOff /><span class="max-sm:sr-only">停用</span>
      </Button>
      <Button
        variant="ghost"
        size="sm"
        class="text-destructive hover:bg-destructive/10 hover:text-destructive max-sm:px-2"
        :disabled="busy"
        @click="batchDelete"
      >
        <Trash2 /><span class="max-sm:sr-only">删除</span>
      </Button>
    </SelectionBar>

    <ProxyAddDialog v-model:open="showAdd" @added="onAdded" />
    <ProxyEditDialog v-model:open="showEdit" :proxy="editing" @saved="onSaved" />
  </div>
</template>
