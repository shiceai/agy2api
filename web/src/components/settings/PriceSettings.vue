<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Pencil, RefreshCw, RotateCcw } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { api, errorMessage, type Price, type PriceRow, type PriceSourceStatus, type PricesView } from '@/api/client'
import { fmtRate } from '@/lib/cost'
import { toastError } from '@/lib/toast'
import { cn, fmtAgo } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import SettingCard from './SettingCard.vue'
import { Table, TableBody, TableCell, TableEmpty, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import DotBadge from '@/components/DotBadge.vue'
import IconAction from '@/components/IconAction.vue'
import LoadError from '@/components/LoadError.vue'
import PriceDialog from './PriceDialog.vue'

// 价格：上面是官方价格页的同步状态（可立即同步），下面是号池里各模型家族的价格，可手动设价或恢复官方价。
// 价格只用来估算用量的费用，不扣费。
const router = useRouter()
const view = ref<PricesView | null>(null)
const loadError = ref('')
const syncing = ref(false)
const busy = ref('')
const editing = ref<PriceRow | null>(null)
const dialogOpen = ref(false)

const vendorLabels: Record<string, string> = { google: 'Google AI Studio', anthropic: 'Anthropic' }
const vendorLabel = (v: string) => vendorLabels[v] ?? v


async function load() {
  try {
    view.value = await api.get<PricesView>('/api/admin/prices')
    loadError.value = ''
  } catch (e) {
    loadError.value = errorMessage(e)
  }
}
onMounted(load)

const sources = computed<PriceSourceStatus[]>(() => view.value?.status.sources ?? [])

async function sync() {
  syncing.value = true
  try {
    view.value = await api.post<PricesView>('/api/admin/prices/sync')
    const failed = sources.value.filter((s) => !s.ok)
    if (!failed.length) {
      toast.success('已同步官方价格', { description: sources.value.map((s) => `${vendorLabel(s.vendor)} ${s.prices} 个`).join('，') })
    } else {
      toast.warning(`${failed.length} 个价格页同步失败，保留原价格`, {
        description: failed.map((s) => `${vendorLabel(s.vendor)}：${s.error}`).join('\n'),
        duration: 10000,
        classes: { description: 'whitespace-pre-line' },
      })
    }
  } catch (e) {
    toastError('同步官方价格失败', e)
  } finally {
    syncing.value = false
  }
}

function edit(row: PriceRow) {
  editing.value = row
  dialogOpen.value = true
}
// 删除手动价：有官方价时恢复官方价，没有时这个模型之后不再计费。
async function revert(row: PriceRow) {
  busy.value = row.model
  try {
    view.value = await api.del<PricesView>(`/api/admin/prices/${encodeURIComponent(row.model)}`)
  } catch (e) {
    toastError(`${row.official ? '恢复官方价' : '删除手动价'}失败：${row.model}`, e)
  } finally {
    busy.value = ''
  }
}

// 有价格的在前，各自按名字排。
const rows = computed(() => {
  const list = [...(view.value?.models ?? [])]
  return list.sort((a, b) => Number(!a.price) - Number(!b.price) || a.model.localeCompare(b.model))
})

const tierOrder = ['low', 'medium', 'high', 'xhigh']
// 号池里属于这个家族的档位，如 low、medium、high；没有档位的模型不显示。
function tiersOf(row: PriceRow): string {
  if (!row.ids.length) return '号池里没有这个模型'
  const tiers = row.ids.filter((id) => id !== row.model).map((id) => id.slice(row.model.length + 1))
  if (!tiers.length) return ''
  return tiers.sort((a, b) => (tierOrder.indexOf(a) + 1 || 99) - (tierOrder.indexOf(b) + 1 || 99)).join('、')
}

const now = Date.now()
// 当前生效的单价：限时价还没到期时用限时价。
function current(p: Price) {
  return p.promo && p.promo.end > now ? p.promo : p
}
const kTokens = (n: number) => `${Math.round(n / 1000)}K`
const md = (ms: number) => `${new Date(ms).getMonth() + 1} 月 ${new Date(ms).getDate()} 日`
// 价格下面的补充说明：用的是别的型号的官方价（别名）、限时价什么时候恢复原价、长提示的分档价。
function note(row: PriceRow): string {
  const e = row.price
  if (!e) return ''
  const p = e.price
  const parts: string[] = []
  if (e.model !== row.model) parts.push(`按 ${e.model} 的官方价`)
  if (p.promo && p.promo.end > now) parts.push(`限时价，${md(p.promo.end)}起输入 ${fmtRate(p.input)}、输出 ${fmtRate(p.output)}`)
  else if (p.tier) parts.push(`提示超过 ${kTokens(p.tier.threshold)} 时输入 ${fmtRate(p.tier.input)}、输出 ${fmtRate(p.tier.output)}`)
  return parts.join('；')
}
function sourceText(row: PriceRow): string {
  if (!row.price) return '没有价格'
  return row.price.source === 'manual' ? '手动' : '官方'
}
</script>

<template>
  <div class="space-y-6">
    <LoadError v-if="loadError" :message="loadError" />

    <SettingCard title="官方价格" description="按官方 API 价格估算费用，只显示，不扣费" raw content-class="space-y-2 text-sm">
      <template #info>
        <p>每天从 Google AI Studio、Anthropic 的官方价格页同步一次。失败或疑似改版时保留原来的价格。</p>
      </template>
      <template #actions>
        <Button variant="outline" size="sm" :disabled="syncing || !view" @click="sync">
          <RefreshCw :class="syncing && 'animate-spin'" />立即同步
        </Button>
      </template>
      <div v-for="s in sources" :key="s.vendor" class="flex flex-wrap items-center gap-x-3 gap-y-1">
        <DotBadge :tone="s.ok ? 'success' : 'danger'">{{ s.ok ? '正常' : '失败' }}</DotBadge>
        <a :href="s.url" target="_blank" rel="noopener noreferrer" class="underline-offset-4 hover:underline">{{ vendorLabel(s.vendor) }}</a>
        <span class="text-muted-foreground">
          <template v-if="s.ok">读出 {{ s.prices }} 个价格</template>
          <template v-else>{{ s.error }}</template>
        </span>
        <span class="text-muted-foreground">{{ s.success_at ? `${fmtAgo(s.success_at)}同步成功` : '还没有成功过' }}</span>
      </div>
      <p v-if="view && !sources.length" class="text-muted-foreground">
        还没有同步过{{ view.auto_sync ? '，服务启动后会自动同步' : '' }}。
      </p>
      <p v-if="view && !view.auto_sync" class="text-xs text-muted-foreground">自动同步已关闭（AGY2API_PRICE_SYNC=off），需要时点"立即同步"。</p>
    </SettingCard>

    <SettingCard title="模型价格" description="单价为美元每百万 token" raw content-class="px-0">
      <template #info>
        <p>号池里的模型按家族列出，各档位同价，推理 Token 按输出价算。</p>
        <p>手动价优先于官方价；没有价格的模型不计费用，设价后之前的调用记录会补算，已算过的不变。</p>
      </template>
      <!-- 窄屏：卡片列表 -->
      <div class="divide-y border-t sm:hidden">
        <div v-for="row in rows" :key="row.model" class="flex items-start gap-3 px-6 py-3">
          <div class="min-w-0 flex-1 space-y-1">
            <div class="flex flex-wrap items-center gap-x-2">
              <span class="text-sm break-all">{{ row.model }}</span>
              <span class="text-xs text-muted-foreground">{{ sourceText(row) }}</span>
            </div>
            <div v-if="row.price" class="text-xs tabular-nums">
              输入 {{ fmtRate(current(row.price.price).input) }} · 缓存 {{ fmtRate(current(row.price.price).cache_read ?? current(row.price.price).input) }} · 输出
              {{ fmtRate(current(row.price.price).output) }}
            </div>
            <div v-if="note(row)" class="text-xs text-muted-foreground">{{ note(row) }}</div>
            <div v-if="tiersOf(row)" class="text-xs text-muted-foreground">{{ tiersOf(row) }}</div>
          </div>
          <div class="-mr-1 flex shrink-0">
            <IconAction label="设置价格" @click="edit(row)"><Pencil /></IconAction>
            <IconAction
              v-if="row.price?.source === 'manual'"
              :label="row.official ? '恢复官方价' : '删除手动价'"
              tone="warning"
              :disabled="busy === row.model"
              @click="revert(row)"
            >
              <RotateCcw />
            </IconAction>
          </div>
        </div>
        <p v-if="view && !rows.length" class="px-4 py-8 text-center text-sm text-muted-foreground">号池里还没有模型</p>
      </div>

      <!-- 宽屏：表格 -->
      <div class="hidden border-t sm:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead class="w-full pl-6">模型</TableHead>
              <TableHead class="text-right">输入</TableHead>
              <TableHead class="text-right whitespace-nowrap">缓存读取</TableHead>
              <TableHead class="text-right">输出</TableHead>
              <TableHead>来源</TableHead>
              <TableHead class="px-6 text-center">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="row in rows" :key="row.model">
              <TableCell class="pl-6">
                <div class="break-all">{{ row.model }}</div>
                <div v-if="note(row)" class="text-xs text-muted-foreground">{{ note(row) }}</div>
                <div v-if="tiersOf(row)" class="text-xs text-muted-foreground">{{ tiersOf(row) }}</div>
              </TableCell>
              <template v-if="row.price">
                <TableCell class="text-right tabular-nums">{{ fmtRate(current(row.price.price).input) }}</TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ fmtRate(current(row.price.price).cache_read ?? current(row.price.price).input) }}
                </TableCell>
                <TableCell class="text-right tabular-nums">{{ fmtRate(current(row.price.price).output) }}</TableCell>
              </template>
              <template v-else>
                <TableCell class="text-right text-muted-foreground">—</TableCell>
                <TableCell class="text-right text-muted-foreground">—</TableCell>
                <TableCell class="text-right text-muted-foreground">—</TableCell>
              </template>
              <TableCell :class="cn('whitespace-nowrap', !row.price && 'text-muted-foreground')">{{ sourceText(row) }}</TableCell>
              <TableCell class="px-6">
                <div class="flex justify-center gap-0.5">
                  <IconAction label="设置价格" @click="edit(row)"><Pencil /></IconAction>
                  <IconAction
                    v-if="row.price?.source === 'manual'"
                    :label="row.official ? '恢复官方价' : '删除手动价'"
                    tone="warning"
                    :disabled="busy === row.model"
                    @click="revert(row)"
                  >
                    <RotateCcw />
                  </IconAction>
                </div>
              </TableCell>
            </TableRow>
            <TableEmpty v-if="view && !rows.length" :colspan="6">号池里还没有模型</TableEmpty>
          </TableBody>
        </Table>
      </div>
    </SettingCard>

    <PriceDialog v-model:open="dialogOpen" :row="editing" @saved="(v) => (view = v)" />
  </div>
</template>
