<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Activity, CircleDollarSign, Coins, Gauge, KeyRound, RefreshCw, Users } from '@lucide/vue'
import { api, errorMessage, type Overview } from '@/api/client'
import { fmtNum, fmtShortTime, fmtTokens } from '@/lib/utils'
import { useLive } from '@/lib/live'
import { useAutoRefresh } from '@/lib/refresh'
import { fmtCost, sumCost } from '@/lib/cost'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableEmpty, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import HourlyChart from '@/components/HourlyChart.vue'
import LoadError from '@/components/LoadError.vue'
import PageActions from '@/components/PageActions.vue'

const router = useRouter()
const data = ref<Overview | null>(null)
const error = ref('')
// 实时并发：轮询 /api/admin/live，概览卡片显示进行中请求总数。离开页面自动停。
const live = useLive()
// 进行中请求总数：/live 取到后以它为准，还没取到时用概览接口里带的快照。
const inFlight = computed(() => (live.loaded.value ? live.total.value : (data.value?.in_flight ?? 0)))

// 今天零点（浏览器本地时区）：概览按当日统计。服务器可能是 UTC，所以由前端算好传过去；过了零点，下次刷新自然切到新的一天。
function todayStart(): number {
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  return d.getTime()
}

// 失败时保留上次的数据，顶部显示原因，下次成功后消失。
async function load(): Promise<boolean> {
  try {
    data.value = await api.get<Overview>(`/api/admin/overview?since=${todayStart()}`)
    error.value = ''
    return true
  } catch (e) {
    error.value = errorMessage(e)
    return false
  }
}
// 每 30 秒自动刷新（页面可见时），顶栏的按钮可随时手动刷新。
const { refreshing, updatedAt, refresh } = useAutoRefresh(load, 30_000)

// 今日合计，由每小时数据加总。
const totals = computed(() => {
  const t = { errors: 0, prompt: 0, output: 0, total: 0 }
  for (const h of data.value?.hourly ?? []) {
    t.errors += h.errors
    t.prompt += h.prompt_tokens
    t.output += h.output_tokens
    t.total += h.total_tokens
  }
  return t
})

function successRate(reqs: number, errors: number): string {
  const v = ((reqs - errors) / reqs) * 100
  return v === 100 ? '100%' : `${v.toFixed(1)}%`
}

const stats = computed(() => {
  const d = data.value
  const reqs = d?.requests ?? 0
  const keys = d?.api_keys_total ?? 0
  const keysOff = keys - (d?.api_keys_enabled ?? 0)
  const t = totals.value
  const models = d?.usage_by_model ?? []
  // 有 Token 却没算出费用的模型：号池里这个模型还没有价格。
  const unpriced = models.filter((m) => m.cost_nanos == null && m.total_tokens > 0).length
  return [
    {
      label: '可用账号',
      value: `${d?.accounts_ready ?? 0} / ${d?.accounts_total ?? 0}`,
      hint: '启用且已同步 / 全部',
      icon: Users,
    },
    {
      label: '进行中请求',
      value: fmtNum(inFlight.value),
      hint: inFlight.value ? '所有账号合计' : '当前没有进行中的请求',
      icon: Gauge,
    },
    {
      label: '今日请求',
      value: fmtNum(reqs),
      hint: reqs ? `失败 ${fmtNum(t.errors)} · 成功率 ${successRate(reqs, t.errors)}` : '今天还没有请求',
      icon: Activity,
    },
    {
      label: '今日 Token',
      value: fmtTokens(t.total),
      hint: `输入 ${fmtTokens(t.prompt)} · 输出 ${fmtTokens(t.output)}`,
      icon: Coins,
    },
    {
      label: '今日费用',
      value: fmtCost(sumCost(models.map((m) => m.cost_nanos))),
      hint: unpriced ? `估算 · ${unpriced} 个模型无价格` : '按官方价估算，不扣费',
      icon: CircleDollarSign,
    },
    {
      label: '启用的密钥',
      value: fmtNum(d?.api_keys_enabled ?? 0),
      hint: keysOff ? `共 ${keys} 个，${keysOff} 个已停用` : `共 ${keys} 个`,
      icon: KeyRound,
    },
  ]
})
</script>

<template>
  <div class="space-y-6">
    <!-- 顶栏：更新时间（窄屏收起）和刷新。不加 aria-live：每 30 秒自动刷新，读屏会被反复打断 -->
    <PageActions>
      <span v-if="updatedAt" class="hidden text-xs text-muted-foreground tabular-nums @md/main:inline">
        更新于 {{ fmtShortTime(updatedAt) }}
      </span>
      <Tooltip>
        <TooltipTrigger as-child>
          <Button variant="outline" size="icon-sm" aria-label="刷新" :aria-busy="refreshing" @click="refresh">
            <RefreshCw :class="refreshing && 'animate-spin'" />
          </Button>
        </TooltipTrigger>
        <TooltipContent>刷新，每 30 秒自动刷新</TooltipContent>
      </Tooltip>
    </PageActions>

    <LoadError v-if="error" :message="error" />

    <!-- 六张卡片按内容区宽度排：窄时两列（三行），中等三列（两行），够宽时一行六张。
         说明固定一行（超长截断，悬停看全文），卡片等高时不会因为某张折行而全部多出一行空白。 -->
    <div class="grid grid-cols-2 gap-3 sm:gap-4 @3xl/main:grid-cols-3 @6xl/main:grid-cols-6">
      <Card v-for="s in stats" :key="s.label" size="sm" class="min-w-0">
        <CardHeader>
          <CardDescription>{{ s.label }}</CardDescription>
          <CardAction><component :is="s.icon" class="size-4 text-muted-foreground" /></CardAction>
        </CardHeader>
        <CardContent>
          <!-- 大号数字用比例数字，tabular-nums 只用于需要竖向对齐的列 -->
          <div class="text-2xl font-semibold tracking-tight">{{ s.value }}</div>
          <p class="mt-1 truncate text-xs text-muted-foreground" :title="s.hint">{{ s.hint }}</p>
        </CardContent>
      </Card>
    </div>

    <Card>
      <CardHeader>
        <CardTitle>今日每小时请求</CardTitle>
        <CardDescription>按本地时间每小时统计；悬停或点按柱子查看明细。</CardDescription>
      </CardHeader>
      <CardContent>
        <HourlyChart :buckets="data?.hourly ?? []" />
      </CardContent>
    </Card>

    <div class="space-y-3">
      <h2 class="text-base font-semibold">今日按模型用量</h2>
      <!-- 窄屏：卡片列表（表格在手机上放不下，会被横向截断） -->
      <div class="divide-y overflow-hidden rounded-lg border sm:hidden">
        <div v-for="m in data?.usage_by_model ?? []" :key="m.model" class="space-y-1 p-3">
          <div class="text-sm break-all">{{ m.model }}</div>
          <div class="flex flex-wrap gap-x-3 text-xs text-muted-foreground tabular-nums">
            <span>{{ fmtNum(m.requests) }} 次</span>
            <span>{{ fmtTokens(m.total_tokens) }} Token</span>
            <span :class="m.cost_nanos != null && 'text-foreground'">{{ fmtCost(m.cost_nanos) }}</span>
          </div>
        </div>
        <p v-if="!(data?.usage_by_model?.length)" class="px-4 py-8 text-center text-sm text-muted-foreground">暂无用量数据</p>
      </div>
      <div class="hidden overflow-hidden rounded-lg border sm:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead class="w-full pl-4">模型</TableHead>
              <TableHead class="text-right">请求</TableHead>
              <TableHead class="hidden text-right @xl/main:table-cell">输入</TableHead>
              <TableHead class="hidden text-right @xl/main:table-cell">输出</TableHead>
              <TableHead class="text-right">合计 Token</TableHead>
              <TableHead class="pr-4 text-right">费用</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="m in data?.usage_by_model ?? []" :key="m.model">
              <TableCell class="pl-4">{{ m.model }}</TableCell>
              <TableCell class="text-right tabular-nums">{{ fmtNum(m.requests) }}</TableCell>
              <TableCell class="hidden text-right tabular-nums @xl/main:table-cell">{{ fmtTokens(m.prompt_tokens) }}</TableCell>
              <TableCell class="hidden text-right tabular-nums @xl/main:table-cell">{{ fmtTokens(m.output_tokens) }}</TableCell>
              <TableCell class="text-right tabular-nums">{{ fmtTokens(m.total_tokens) }}</TableCell>
              <TableCell :class="['pr-4 text-right tabular-nums', m.cost_nanos == null && 'text-muted-foreground']">{{ fmtCost(m.cost_nanos) }}</TableCell>
            </TableRow>
            <TableEmpty v-if="!(data?.usage_by_model?.length)" :colspan="6">暂无用量数据</TableEmpty>
          </TableBody>
        </Table>
      </div>
    </div>
  </div>
</template>
