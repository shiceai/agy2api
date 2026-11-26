<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RefreshCw, Stethoscope } from '@lucide/vue'
import { api, type Account, type AccountStats, type Proxy } from '@/api/client'
import { cn, fmtAgo, fmtDuration, fmtNum, fmtRemaining, fmtShortTime, fmtSpan, fmtTime } from '@/lib/utils'
import { quotaStale } from '@/lib/quota'
import { fmtCost } from '@/lib/cost'
import { resultLabel } from '@/lib/usage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import HttpStatus from '@/components/HttpStatus.vue'
import PlanBadge from '@/components/PlanBadge.vue'
import QuotaWindowList from '@/components/QuotaWindowList.vue'
import SegmentedControl from '@/components/SegmentedControl.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import UsageDetailDialog from '@/components/usage/UsageDetailDialog.vue'
import ProbeDialog from './ProbeDialog.vue'
import AccountProxy from './AccountProxy.vue'
import ProxyChoiceSelect from '@/components/proxies/ProxyChoiceSelect.vue'
import { choiceOf, proxyValue } from '@/lib/proxy'

// 账号详情：在账号列表点邮箱打开（右侧面板，手机上占满）。先是当前状态（额度、冷却），再是所选时间范围内的
// 调用情况（按每次上游尝试统计：次数、成功率、平均耗时、各模型明细、最近错误，错误可打开调用详情），最后是账号信息。
const account = defineModel<Account | null>('account', { required: true })
// quotaRefresh 是后台刷新额度的间隔（秒），weeklyQuota 是每周额度的全局开关（账号跟随全局时显示）。
const props = defineProps<{
  lowQuota: number
  quotaRefresh: number
  weeklyQuota: boolean
  defaultConcurrency: number
  inFlight: number
  syncing: boolean
  proxies: Proxy[]
}>()
// changed：检测改变了账号状态（冷却、停用、启用），列表要刷新。
const emit = defineEmits<{ sync: [id: number]; changed: [] }>()

// 关闭时保留最后显示的账号，淡出动画里不变空。
const shown = ref<Account | null>(null)
watch(account, (a) => a && (shown.value = a), { immediate: true })

type RangeKey = '24h' | '7d'
const ranges: { value: RangeKey; label: string; ms: number }[] = [
  { value: '24h', label: '24 小时', ms: 86400_000 },
  { value: '7d', label: '7 天', ms: 7 * 86400_000 },
]
const range = ref<RangeKey>('24h')
const stats = ref<AccountStats | null>(null)
const loading = ref(false)
const error = ref('')
const detailId = ref<number | null>(null)
const probeOpen = ref(false)

// 换账号时清空旧数据；只换时间范围时保留原内容、稍微变淡，取到后替换。
let lastID: number | null = null
watch(
  [() => account.value?.id, range],
  async ([id]) => {
    if (id == null) return
    if (id !== lastID) stats.value = null
    lastID = id
    error.value = ''
    loading.value = true
    const since = Date.now() - (ranges.find((r) => r.value === range.value)?.ms ?? 0)
    try {
      const res = await api.get<AccountStats>(`/api/admin/accounts/${id}/stats?since=${since}`)
      if (account.value?.id === id) stats.value = res
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const summary = computed(() => {
  const models = stats.value?.models ?? []
  const attempts = models.reduce((s, m) => s + m.attempts, 0)
  const success = models.reduce((s, m) => s + m.success, 0)
  const totalMs = models.reduce((s, m) => s + m.avg_ms * m.success, 0)
  return { attempts, success, rate: attempts ? success / attempts : null, avgMs: success ? totalMs / success : null }
})
function fmtRate(success: number, attempts: number): string {
  if (!attempts) return '—'
  const v = (success / attempts) * 100
  return `${v === 100 || v === 0 ? v : v.toFixed(1)}%`
}
const hubVersion = (ua: string) => /antigravity\/hub\/(\S+)/.exec(ua ?? '')?.[1] ?? (ua || '—')
const concurrencyText = computed(() =>
  shown.value?.max_concurrency ? String(shown.value.max_concurrency) : `${props.defaultConcurrency}（跟随全局）`,
)

// 并发上限就地修改：留空表示跟随全局默认值（设置 → 账号调度）。换账号时退出编辑。
// 账号信息里就地改绑代理（直连或某个代理）。
const editingProxy = ref(false)
const proxyChoice = ref('none')
const proxyError = ref('')
const savingProxy = ref(false)
watch(
  () => account.value?.id,
  () => (editingProxy.value = false),
)
function editProxy() {
  proxyChoice.value = shown.value?.proxy ? proxyValue(shown.value.proxy.id) : 'none'
  proxyError.value = ''
  editingProxy.value = true
}
async function saveProxy() {
  const a = shown.value
  if (!a) return
  savingProxy.value = true
  try {
    await api.put(`/api/admin/accounts/${a.id}/proxy`, { proxy_id: choiceOf(proxyChoice.value).proxy_id ?? 0 })
    editingProxy.value = false
    emit('changed')
  } catch (e) {
    proxyError.value = (e as Error).message
  } finally {
    savingProxy.value = false
  }
}

// 每周额度开关就地修改：跟随全局（设置 → 账号调度 → 额度）、开、关。开启后服务端立即拉一次。
type WeeklyMode = '' | 'on' | 'off'
const editingWeekly = ref(false)
const weeklyChoice = ref<WeeklyMode>('')
const weeklyError = ref('')
const savingWeekly = ref(false)
const weeklyOptions = computed(() => [
  { value: '' as WeeklyMode, label: `跟随全局（${props.weeklyQuota ? '开' : '关'}）` },
  { value: 'on' as WeeklyMode, label: '开' },
  { value: 'off' as WeeklyMode, label: '关' },
])
const weeklyText = computed(() => weeklyOptions.value.find((o) => o.value === (shown.value?.weekly_quota ?? ''))?.label ?? '—')
watch(
  () => account.value?.id,
  () => (editingWeekly.value = false),
)
function editWeekly() {
  weeklyChoice.value = shown.value?.weekly_quota ?? ''
  weeklyError.value = ''
  editingWeekly.value = true
}
async function saveWeekly() {
  const a = shown.value
  if (!a) return
  savingWeekly.value = true
  try {
    await api.put(`/api/admin/accounts/${a.id}/settings`, { weekly_quota: weeklyChoice.value })
    editingWeekly.value = false
    emit('changed')
  } catch (e) {
    weeklyError.value = (e as Error).message
  } finally {
    savingWeekly.value = false
  }
}

const editingConcurrency = ref(false)
const concurrencyInput = ref('')
const concurrencyError = ref('')
const savingConcurrency = ref(false)
watch(
  () => account.value?.id,
  () => (editingConcurrency.value = false),
)
function editConcurrency() {
  concurrencyInput.value = shown.value?.max_concurrency ? String(shown.value.max_concurrency) : ''
  concurrencyError.value = ''
  editingConcurrency.value = true
}
async function saveConcurrency() {
  const a = shown.value
  if (!a) return
  const raw = String(concurrencyInput.value).trim()
  const n = raw === '' ? 0 : Number(raw)
  if (!Number.isInteger(n) || n < 0 || n > 64) {
    concurrencyError.value = '并发上限应是 1～64 的整数，留空跟随全局'
    return
  }
  savingConcurrency.value = true
  try {
    await api.put(`/api/admin/accounts/${a.id}/settings`, { max_concurrency: n })
    editingConcurrency.value = false
    emit('changed')
  } catch (e) {
    concurrencyError.value = (e as Error).message
  } finally {
    savingConcurrency.value = false
  }
}
</script>

<template>
  <Sheet :open="!!account" @update:open="(v) => !v && (account = null)">
    <SheetContent class="gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-xl">
      <template v-if="shown">
        <SheetHeader class="gap-2 border-b px-6 py-4">
          <SheetTitle class="truncate pr-8">{{ shown.email }}</SheetTitle>
          <SheetDescription as="div" class="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span v-if="shown.name && shown.name !== shown.email">{{ shown.name }}</span>
            <PlanBadge :subscription="shown.subscription" class="text-foreground" />
            <StatusBadge :enabled="shown.status === 'enabled'" :reason="shown.disabled_reason" />
          </SheetDescription>
          <div class="flex gap-2 pt-1">
            <Button variant="outline" size="sm" @click="probeOpen = true"><Stethoscope />检测</Button>
            <Button variant="outline" size="sm" :disabled="syncing" @click="emit('sync', shown.id)">
              <RefreshCw :class="syncing && 'animate-spin'" />同步
            </Button>
          </div>
        </SheetHeader>

        <ScrollArea class="min-h-0 flex-1">
          <div class="divide-y px-6">
            <!-- 当前额度 -->
            <section class="py-5">
              <div class="mb-2 flex items-baseline justify-between gap-3">
                <h3 class="text-sm font-semibold">额度</h3>
                <span
                  :class="['text-xs', quotaStale(shown, quotaRefresh) ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground']"
                  :title="fmtTime(shown.models_refreshed_at)"
                >
                  更新于 {{ fmtAgo(shown.models_refreshed_at) }}<template v-if="quotaStale(shown, quotaRefresh)"> · 可能已过期</template>
                </span>
              </div>
              <div class="overflow-hidden rounded-lg border">
                <QuotaWindowList :account="shown" :low-quota="lowQuota" />
              </div>
            </section>

            <!-- 冷却中的模型 -->
            <section v-if="shown.cooldowns?.length" class="py-5">
              <h3 class="mb-2 text-sm font-semibold">冷却中</h3>
              <ul class="space-y-1.5 text-sm">
                <li v-for="c in shown.cooldowns" :key="c.model" class="flex items-baseline gap-3">
                  <span class="min-w-0 flex-1 truncate">{{ c.model }}</span>
                  <span class="shrink-0 text-muted-foreground">{{ resultLabel(c.reason) }}</span>
                  <span class="shrink-0 text-right whitespace-nowrap text-muted-foreground tabular-nums" :title="`到 ${fmtTime(c.until)}`">
                    {{ fmtRemaining(c.until) }}后恢复
                  </span>
                </li>
              </ul>
            </section>

            <!-- 调用情况：按每次上游尝试统计 -->
            <section class="py-5">
              <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
                <h3 class="text-sm font-semibold">调用情况</h3>
                <SegmentedControl v-model="range" :options="ranges" label="时间范围" hide-label />
              </div>
              <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
              <div v-else-if="!stats" class="h-32" />
              <div v-else :class="cn('space-y-5 transition-opacity duration-200', loading && 'opacity-60')">
                <dl class="grid grid-cols-2 gap-4 rounded-lg bg-muted/50 px-4 py-3 sm:grid-cols-4">
                  <div>
                    <dt class="text-xs text-muted-foreground">上游尝试</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ fmtNum(summary.attempts) }}</dd>
                  </div>
                  <div>
                    <dt class="text-xs text-muted-foreground">成功率</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ fmtRate(summary.success, summary.attempts) }}</dd>
                  </div>
                  <div>
                    <dt class="text-xs text-muted-foreground">平均耗时</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ summary.avgMs == null ? '—' : fmtDuration(Math.round(summary.avgMs)) }}</dd>
                  </div>
                  <div>
                    <dt class="text-xs text-muted-foreground">费用</dt>
                    <dd class="mt-0.5 text-base font-semibold tabular-nums">{{ fmtCost(stats.cost_nanos) }}</dd>
                  </div>
                </dl>
                <p class="-mt-3 text-xs text-muted-foreground">
                  换号前失败的尝试也算在这个账号上；平均耗时只算成功的尝试，流式算到结束；费用按官方价估算，算最终由这个账号完成的请求。
                </p>

                <div v-if="stats.models.length">
                  <h4 class="mb-1.5 text-xs font-semibold text-muted-foreground">各模型</h4>
                  <table class="w-full text-sm whitespace-nowrap tabular-nums">
                    <thead class="text-xs text-muted-foreground">
                      <tr class="border-b">
                        <th class="w-full py-1.5 pr-3 text-left font-normal">模型</th>
                        <th class="py-1.5 pr-3 text-right font-normal">尝试</th>
                        <th class="py-1.5 pr-3 text-right font-normal">成功率</th>
                        <th class="py-1.5 text-right font-normal">平均耗时</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="m in stats.models" :key="m.model" class="border-b last:border-0">
                        <td class="max-w-0 truncate py-1.5 pr-3" :title="m.model">{{ m.model }}</td>
                        <td class="py-1.5 pr-3 text-right">{{ fmtNum(m.attempts) }}</td>
                        <td :class="['py-1.5 pr-3 text-right', m.success < m.attempts && 'text-amber-700 dark:text-amber-400']">
                          {{ fmtRate(m.success, m.attempts) }}
                        </td>
                        <td class="py-1.5 text-right">{{ m.success ? fmtDuration(Math.round(m.avg_ms)) : '—' }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <p v-else class="py-4 text-center text-sm text-muted-foreground">这段时间没有经过这个账号的调用</p>

                <div v-if="stats.recent_errors.length">
                  <h4 class="mb-1.5 text-xs font-semibold text-muted-foreground">最近错误</h4>
                  <ul class="divide-y text-sm">
                    <li v-for="(e, i) in stats.recent_errors" :key="i" class="py-2">
                      <div class="flex items-center gap-2">
                        <span class="shrink-0 text-muted-foreground tabular-nums" :title="fmtTime(e.created_at)">{{ fmtShortTime(e.created_at) }}</span>
                        <span class="min-w-0 truncate">{{ e.model }}</span>
                        <HttpStatus v-if="e.status" :code="e.status" class="shrink-0" />
                        <span class="shrink-0 whitespace-nowrap text-destructive">{{ resultLabel(e.result) }}</span>
                        <Button variant="link" size="xs" class="ml-auto h-auto shrink-0 px-0" @click="detailId = e.usage_id">详情</Button>
                      </div>
                      <p v-if="e.error || e.cooldown_ms" class="mt-0.5 truncate text-xs text-muted-foreground" :title="e.error">
                        <template v-if="e.cooldown_ms">冷却 {{ fmtSpan(e.cooldown_ms) }}<template v-if="e.error"> · </template></template>{{ e.error }}
                      </p>
                    </li>
                  </ul>
                </div>
              </div>
            </section>

            <!-- 账号信息 -->
            <section class="py-5">
              <h3 class="mb-2.5 text-sm font-semibold">账号信息</h3>
              <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
                <dt class="text-muted-foreground">项目</dt>
                <dd class="wrap-anywhere">{{ shown.project_id || '—' }}</dd>
                <template v-if="shown.user_project_id">
                  <dt class="text-muted-foreground">用户项目</dt>
                  <dd class="wrap-anywhere">{{ shown.user_project_id }}</dd>
                </template>
                <dt class="text-muted-foreground">客户端</dt>
                <dd :title="shown.user_agent">hub {{ hubVersion(shown.user_agent) }}</dd>
                <dt :class="['text-muted-foreground', editingConcurrency && 'pt-1.5']">并发</dt>
                <dd>
                  <!-- 当前进行中 / 上限；当前为 0 时灰色 -->
                  <div v-if="!editingConcurrency" class="flex items-center gap-3">
                    <span>
                      <span :class="['tabular-nums', props.inFlight ? undefined : 'text-muted-foreground']">{{ props.inFlight }}</span>
                      <span class="text-muted-foreground"> / </span>{{ concurrencyText }}
                    </span>
                    <Button variant="link" size="xs" class="h-auto px-0" @click="editConcurrency">修改</Button>
                  </div>
                  <form v-else class="space-y-1.5" @submit.prevent="saveConcurrency">
                    <div class="flex flex-wrap items-center gap-2">
                      <Input
                        v-model="concurrencyInput"
                        type="number"
                        inputmode="numeric"
                        min="1"
                        max="64"
                        step="1"
                        aria-label="并发上限"
                        :placeholder="`跟随全局（${defaultConcurrency}）`"
                        class="h-8 w-36"
                      />
                      <Button type="submit" size="sm" :disabled="savingConcurrency">保存</Button>
                      <Button type="button" variant="ghost" size="sm" @click="editingConcurrency = false">取消</Button>
                    </div>
                    <p class="text-xs text-muted-foreground">这个账号同时处理的请求数，1～64；留空跟随"设置 → 账号调度"里的全局默认值。</p>
                    <p v-if="concurrencyError" class="text-xs text-destructive">{{ concurrencyError }}</p>
                  </form>
                </dd>
                <dt :class="['text-muted-foreground', editingProxy && 'pt-1.5']">代理</dt>
                <dd>
                  <div v-if="!editingProxy" class="flex items-center gap-3">
                    <AccountProxy :account="shown" />
                    <span v-if="shown.proxy" class="truncate text-muted-foreground">{{ shown.proxy.name }}</span>
                    <Button variant="link" size="xs" class="h-auto px-0" @click="editProxy">修改</Button>
                  </div>
                  <form v-else class="space-y-1.5" @submit.prevent="saveProxy">
                    <ProxyChoiceSelect v-model="proxyChoice" :proxies="proxies" />
                    <div class="flex items-center gap-2">
                      <Button type="submit" size="sm" :disabled="savingProxy">保存</Button>
                      <Button type="button" variant="ghost" size="sm" @click="editingProxy = false">取消</Button>
                    </div>
                    <p class="text-xs text-muted-foreground">这个账号的所有请求都经它出网，代理连不上时不会改成直连。</p>
                    <p v-if="proxyError" class="text-xs text-destructive">{{ proxyError }}</p>
                  </form>
                </dd>
                <dt :class="['text-muted-foreground', editingWeekly && 'pt-1.5']">每周额度</dt>
                <dd>
                  <div v-if="!editingWeekly" class="flex items-center gap-3">
                    <span>{{ weeklyText }}</span>
                    <Button variant="link" size="xs" class="h-auto px-0" @click="editWeekly">修改</Button>
                  </div>
                  <form v-else class="space-y-1.5" @submit.prevent="saveWeekly">
                    <SegmentedControl v-model="weeklyChoice" :options="weeklyOptions" label="每周额度" hide-label />
                    <div class="flex items-center gap-2">
                      <Button type="submit" size="sm" :disabled="savingWeekly">保存</Button>
                      <Button type="button" variant="ghost" size="sm" @click="editingWeekly = false">取消</Button>
                    </div>
                    <p class="text-xs text-muted-foreground">额度里多显示每周额度；官方抓包里没有这个请求。</p>
                    <p v-if="weeklyError" class="text-xs text-destructive">{{ weeklyError }}</p>
                  </form>
                </dd>
                <dt class="text-muted-foreground">模型</dt>
                <dd>{{ shown.model_count }} 个</dd>
                <template v-if="shown.status !== 'enabled' && shown.disabled_reason">
                  <dt class="text-muted-foreground">停用原因</dt>
                  <dd class="wrap-anywhere">{{ shown.disabled_reason }}</dd>
                </template>
                <dt class="text-muted-foreground">添加于</dt>
                <dd class="tabular-nums">{{ fmtTime(shown.created_at) }}</dd>
                <dt class="text-muted-foreground">最近同步</dt>
                <dd class="tabular-nums">{{ shown.synced_at ? fmtTime(shown.synced_at) : '未同步' }}</dd>
                <template v-if="shown.subscription">
                  <dt class="text-muted-foreground">订阅检查</dt>
                  <dd class="tabular-nums">{{ fmtTime(shown.subscription.checked_at) }}</dd>
                </template>
              </dl>
            </section>
          </div>
        </ScrollArea>
      </template>
    </SheetContent>
  </Sheet>
  <UsageDetailDialog v-model="detailId" />
  <ProbeDialog v-if="shown" v-model:open="probeOpen" :account="shown" @changed="emit('changed')" />
</template>
