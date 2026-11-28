<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, errorMessage, type Scheduling, type SchedulingResponse, type StickyMode, type Strategy } from '@/api/client'
import { Switch } from '@/components/ui/switch'
import SegmentedControl from '@/components/SegmentedControl.vue'
import SettingGrid from './SettingGrid.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'
import UnitInput from './UnitInput.vue'
import UnsavedBar from './UnsavedBar.vue'
import { toastError } from '@/lib/toast'
import { Button } from '@/components/ui/button'

// 表单按界面上的单位存：提前量、两种 403 冷却和额度刷新间隔以分钟显示，其余以秒；接口统一用秒。
// 额度刷新间隔 0 表示关闭，表单拆成开关和分钟数，关闭时分钟数保留默认的 15。
interface Form {
  early: boolean
  minLead: number
  maxLead: number
  concurrency: number
  slotWait: number
  maxAttempts: number
  cooldownWait: number
  rlBase: number
  rlMax: number
  serverError: number
  validation: number
  modelForbidden: number
  quotaOn: boolean
  quotaEvery: number
  lowQuota: number
  weekly: boolean
  idleOn: boolean
  idleSecs: number
  requireProxy: boolean
  strategy: Strategy
  stickyMode: StickyMode
  stickyMin: number
  tosPause: boolean
  tosHours: number
}
const MIN = 60
const QUOTA_DEFAULT_MIN = 15
const IDLE_DEFAULT_SECS = 120

// 选号方式、会话粘性的选项和各自的一行说明。
const strategyOptions: { value: Strategy; label: string }[] = [
  { value: 'round_robin', label: '轮询' },
  { value: 'smart', label: '智能' },
  { value: 'reset_first', label: '重置优先' },
]
const strategyHints: Record<Strategy, string> = {
  round_robin: '能用的账号轮流用',
  smart: '按负载、额度、失败率和延迟打分',
  reset_first: '先用额度最快重置的账号',
}
const stickyOptions: { value: StickyMode; label: string }[] = [
  { value: 'off', label: '关闭' },
  { value: 'preferred', label: '优先' },
  { value: 'strict', label: '严格' },
]
const stickyHints: Record<StickyMode, string> = {
  off: '每个请求按选号方式选',
  preferred: '原账号暂时接不了时临时借号',
  strict: '原账号并发满时先等它，再借号',
}

function toForm(s: Scheduling): Form {
  return {
    early: s.token_refresh_early,
    minLead: s.token_refresh_min_lead / MIN,
    maxLead: s.token_refresh_max_lead / MIN,
    concurrency: s.concurrency,
    slotWait: s.slot_wait,
    maxAttempts: s.max_attempts,
    cooldownWait: s.cooldown_wait,
    rlBase: s.rate_limit_base,
    rlMax: s.rate_limit_max,
    serverError: s.server_error_cooldown,
    validation: s.validation_cooldown / MIN,
    modelForbidden: s.model_forbidden_cooldown / MIN,
    quotaOn: s.quota_refresh > 0,
    quotaEvery: s.quota_refresh > 0 ? s.quota_refresh / MIN : QUOTA_DEFAULT_MIN,
    lowQuota: s.low_quota,
    weekly: s.weekly_quota,
    idleOn: s.stream_idle > 0,
    idleSecs: s.stream_idle > 0 ? s.stream_idle : IDLE_DEFAULT_SECS,
    requireProxy: s.require_proxy,
    strategy: s.strategy,
    stickyMode: s.sticky_mode,
    stickyMin: s.sticky_ttl / MIN,
    tosPause: s.tos_pause,
    tosHours: s.tos_pause_hours,
  }
}
function toApi(f: Form): Scheduling {
  const r = Math.round
  return {
    token_refresh_early: f.early,
    token_refresh_min_lead: r(f.minLead * MIN),
    token_refresh_max_lead: r(f.maxLead * MIN),
    concurrency: r(f.concurrency),
    slot_wait: r(f.slotWait),
    max_attempts: r(f.maxAttempts),
    cooldown_wait: r(f.cooldownWait),
    rate_limit_base: r(f.rlBase),
    rate_limit_max: r(f.rlMax),
    server_error_cooldown: r(f.serverError),
    validation_cooldown: r(f.validation * MIN),
    model_forbidden_cooldown: r(f.modelForbidden * MIN),
    quota_refresh: f.quotaOn ? r(f.quotaEvery * MIN) : 0,
    low_quota: r(f.lowQuota),
    stream_idle: f.idleOn ? r(f.idleSecs) : 0,
    require_proxy: f.requireProxy,
    strategy: f.strategy,
    sticky: f.stickyMode !== 'off',
    sticky_mode: f.stickyMode,
    sticky_ttl: r(f.stickyMin * MIN),
    weekly_quota: f.weekly,
    tos_pause: f.tosPause,
    tos_pause_hours: r(f.tosHours),
  }
}

const router = useRouter()
const form = ref<Form | null>(null)
const defaults = ref<Scheduling | null>(null)
const saved = ref('') // 库里当前生效的设置（序列化后），用来判断是否有改动
const error = ref('')
const saving = ref(false)

const dirty = computed(() => !!form.value && JSON.stringify(toApi(form.value)) !== saved.value)
const isDefault = computed(() => !!form.value && !!defaults.value && JSON.stringify(toApi(form.value)) === JSON.stringify(defaults.value))

function inRange(v: number, lo: number, hi: number) {
  return Number.isFinite(v) && v >= lo && v <= hi
}
// 和服务端的取值范围一致，先在界面上提示。
const problem = computed(() => {
  const f = form.value
  if (!f) return ''
  const checks: [boolean, string][] = [
    [inRange(f.minLead, 1, 30), '提前量的下限应在 1～30 分钟'],
    [inRange(f.maxLead, f.minLead, 50), '提前量的上限不能小于下限，最多 50 分钟'],
    [inRange(f.concurrency, 1, 64), '并发上限应在 1～64'],
    [inRange(f.slotWait, 0, 120), '等待时间应在 0～120 秒'],
    [!f.idleOn || inRange(f.idleSecs, 30, 600), '流式空闲超时应在 30～600 秒'],
    [!f.quotaOn || inRange(f.quotaEvery, 5, 120), '额度刷新间隔应在 5～120 分钟'],
    [inRange(f.lowQuota, 0, 50), '避让阈值应在 0～50%'],
    [inRange(f.stickyMin, 5, 1440), '会话粘性的闲置失效应在 5～1440 分钟'],
    [inRange(f.maxAttempts, 1, 10), '尝试账号数应在 1～10'],
    [inRange(f.cooldownWait, 0, 120), '冷却等待时间应在 0～120 秒'],
    [inRange(f.rlBase, 1, 3600), '瞬时限流的起始冷却应在 1～3600 秒'],
    [inRange(f.rlMax, f.rlBase, 86400), '瞬时限流的冷却上限不能小于起始值，最多 86400 秒'],
    [inRange(f.serverError, 0, 3600), '5xx 休息时间应在 0～3600 秒'],
    [inRange(f.validation, 0, 1440), '需要验证的冷却应在 0～1440 分钟'],
    [inRange(f.modelForbidden, 0, 1440), '无权使用模型的冷却应在 0～1440 分钟'],
    [inRange(f.tosHours, 1, 72), '封禁后暂停时长应在 1～72 小时'],
  ]
  return checks.find(([ok]) => !ok)?.[1] ?? ''
})

function apply(res: SchedulingResponse) {
  form.value = toForm(res.settings)
  defaults.value = res.defaults
  saved.value = JSON.stringify(res.settings)
}

function handle(e: unknown) {
  error.value = errorMessage(e)
}

onMounted(async () => {
  try {
    apply(await api.get<SchedulingResponse>('/api/admin/settings/scheduling'))
  } catch (e) {
    handle(e)
  }
})

async function save() {
  if (!form.value || problem.value) return
  saving.value = true
  try {
    apply(await api.put<SchedulingResponse>('/api/admin/settings/scheduling', toApi(form.value)))
  } catch (e) {
    toastError('保存账号调度失败', e)
  } finally {
    saving.value = false
  }
}

function discard() {
  form.value = toForm(JSON.parse(saved.value))
}

function restoreDefaults() {
  if (defaults.value) form.value = toForm(defaults.value)
}
</script>

<!-- 设置页"账号调度"标签：选号（选号方式、会话粘性）、令牌刷新、并发、代理、流式、额度、重试与冷却、封禁保护。 -->
<template>
  <div v-if="form" class="space-y-6">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <SettingGrid>
      <SettingCard title="选号">
        <template #info>
          <p>先排除冷却中、并发已满的账号，额度低于避让阈值的先不选。同一对话优先用绑定的账号，没有或它接不了时按选号方式选。</p>
          <p>轮询：能用的账号按顺序轮流。</p>
          <p>智能：按负载、这个模型的剩余额度、近期失败率和流式首字延迟打分，分数接近的轮流用。失败率和延迟只在内存里统计，重启后清零。</p>
          <p>重置优先：先用这个模型额度最快重置的账号，没用过的额度窗口排在后面；开了每周额度的账号也看每周额度的重置时间。</p>
          <p>会话粘性按同一密钥、同一模型、同一会话标识认对话，没有标识时按首条用户消息。上游缓存按账号，换号就命中不了。</p>
          <p>优先：原账号 5 分钟内能恢复时只临时借号，下个请求回到它；停用、额度耗尽、冷却较长或额度低时才改绑。</p>
          <p>
            严格：原账号并发满、或在新号预热的请求间隔内时先等它（最多等"并发满时最多等待"），等不到再临时借号；429、5xx
            等短冷却时同优先，临时借号、绑定不变。绑定只在内存里，重启后清空。
          </p>
        </template>
        <SettingRow label="选号方式" :description="strategyHints[form.strategy]">
          <SegmentedControl v-model="form.strategy" :options="strategyOptions" label="选号方式" hide-label />
        </SettingRow>
        <SettingRow label="会话粘性" :description="stickyHints[form.stickyMode]">
          <SegmentedControl v-model="form.stickyMode" :options="stickyOptions" label="会话粘性" hide-label />
        </SettingRow>
        <SettingRow label="闲置多久后不再粘" description="超过后当作新对话" input-id="sched-sticky-ttl">
          <UnitInput id="sched-sticky-ttl" v-model="form.stickyMin" unit="分钟" :min="5" :max="1440" :disabled="form.stickyMode === 'off'" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="令牌刷新">
        <template #info>
          <p>access token 有效期 1 小时。提前刷新时每个账号在提前量范围里取一个固定值，同一批导入的账号不会同时刷新。</p>
          <p>关闭后只在令牌过期或上游返回 401 时刷新。</p>
        </template>
        <SettingRow label="到期前提前刷新" description="关闭后过期才刷新" input-id="sched-early">
          <Switch id="sched-early" v-model="form.early" />
        </SettingRow>
        <SettingRow label="提前量" description="按账号错开取值">
          <UnitInput v-model="form.minLead" unit="" :min="1" :max="30" :disabled="!form.early" label="提前量下限（分钟）" />
          <span class="text-muted-foreground">～</span>
          <UnitInput v-model="form.maxLead" unit="分钟" :min="1" :max="50" :disabled="!form.early" label="提前量上限（分钟）" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="并发">
        <template #info>
          <p>一个账号同时处理的请求数，流式请求读完才释放名额。账号列表里可以给单个账号另设上限，没设的用这里的值。</p>
        </template>
        <SettingRow label="每个账号的并发上限" description="账号可以单独设置" input-id="sched-concurrency">
          <UnitInput id="sched-concurrency" v-model="form.concurrency" unit="个" :min="1" :max="64" />
        </SettingRow>
        <SettingRow label="并发满时最多等待" description="超时返回 429，0 表示不等" input-id="sched-wait">
          <UnitInput id="sched-wait" v-model="form.slotWait" unit="秒" :min="0" :max="120" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="代理">
        <template #info>
          <p>账号绑定代理后，它的所有请求都经代理出网，代理连不上时不会改成直连。代理在"代理"页管理。</p>
        </template>
        <SettingRow label="只用绑定了代理的账号" description="没绑代理的账号不参与调度" input-id="sched-require-proxy">
          <Switch id="sched-require-proxy" v-model="form.requireProxy" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="流式">
        <template #info>
          <p>流式响应不限总时长，但中途上游太久没有数据就断开，释放账号的并发名额。断开时客户端会收到一个错误事件，用量里记为失败。</p>
          <p>超时太短会误断正常的长时间思考。</p>
        </template>
        <SettingRow label="空闲时断开" description="关闭后一直等上游" input-id="sched-idle-on">
          <Switch id="sched-idle-on" v-model="form.idleOn" />
        </SettingRow>
        <SettingRow label="空闲超时" description="两次收到数据的最长间隔" input-id="sched-idle">
          <UnitInput id="sched-idle" v-model="form.idleSecs" unit="秒" :min="30" :max="600" :disabled="!form.idleOn" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="额度">
        <template #info>
          <p>后台定期拉取各账号每个模型的剩余额度（账号列表里显示），同时每 6 小时检查一次订阅档位。各账号错开时间刷新，不会同时请求上游。</p>
          <p>选号时先避开请求的模型额度低于阈值的账号，都低于时照常选。</p>
          <p>
            每周额度来自上游的另一个接口（retrieveUserQuotaSummary），按组给出 5 小时和每周额度。参考项目在用，官方客户端的本地服务也有这个接口，
            但官方客户端的抓包里没见过它发这个请求，所以默认关。开启后随额度刷新一起拉，账号也可以在详情里单独开关。拉到的每周额度用于选号：用完的账号在重置前不选，
            避让按 5 小时和每周额度的较低值算，每周额度没恢复时不提前解除额度耗尽冷却。
          </p>
        </template>
        <SettingRow label="后台刷新额度" description="关闭后只在同步账号时更新" input-id="sched-quota-on">
          <Switch id="sched-quota-on" v-model="form.quotaOn" />
        </SettingRow>
        <SettingRow label="刷新间隔" input-id="sched-quota-every">
          <UnitInput id="sched-quota-every" v-model="form.quotaEvery" unit="分钟" :min="5" :max="120" :disabled="!form.quotaOn" />
        </SettingRow>
        <SettingRow label="额度低于此值时避让" description="先选别的账号，0 表示不避让" input-id="sched-low-quota">
          <UnitInput id="sched-low-quota" v-model="form.lowQuota" unit="%" :min="0" :max="50" />
        </SettingRow>
        <SettingRow label="拉取每周额度" description="选号时也看每周额度；官方抓包里没有这个请求" input-id="sched-weekly">
          <Switch id="sched-weekly" v-model="form.weekly" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="重试与冷却">
        <template #info>
          <p>上游给了恢复时间的限流和额度耗尽，按上游时间冷却；条款封禁和 refresh token 失效直接停用账号。这些规则固定，不在这里调。</p>
          <p>瞬时限流（上游没给恢复时间）从起始值开始冷却，同一账号连续触发就翻倍，到上限为止。</p>
          <p>账号都在冷却、最早的在等待时间内恢复时，先等它恢复再试，不立即返回 429；等完之后的尝试仍算在最多尝试的账号数里。</p>
        </template>
        <SettingRow label="单次请求最多尝试" description="限流、5xx、连不上时换号" input-id="sched-attempts">
          <UnitInput id="sched-attempts" v-model="form.maxAttempts" unit="个账号" :min="1" :max="10" />
        </SettingRow>
        <SettingRow label="冷却时最多等待" description="快恢复时先等再试，0 表示不等" input-id="sched-cooldown-wait">
          <UnitInput id="sched-cooldown-wait" v-model="form.cooldownWait" unit="秒" :min="0" :max="120" />
        </SettingRow>
        <SettingRow label="瞬时限流冷却" description="连续触发翻倍，到上限为止">
          <UnitInput v-model="form.rlBase" unit="" :min="1" :max="3600" label="瞬时限流起始冷却（秒）" />
          <span class="text-muted-foreground">～</span>
          <UnitInput v-model="form.rlMax" unit="秒" :min="1" :max="86400" label="瞬时限流冷却上限（秒）" />
        </SettingRow>
        <SettingRow label="5xx 后休息" description="这个模型暂停接请求，0 表示不休息" input-id="sched-5xx">
          <UnitInput id="sched-5xx" v-model="form.serverError" unit="秒" :min="0" :max="3600" />
        </SettingRow>
        <SettingRow label="需要验证时冷却" description="403 VALIDATION_REQUIRED" input-id="sched-validation">
          <UnitInput id="sched-validation" v-model="form.validation" unit="分钟" :min="0" :max="1440" />
        </SettingRow>
        <SettingRow label="无权使用模型时冷却" description="403，账号没有这个模型的权限" input-id="sched-model">
          <UnitInput id="sched-model" v-model="form.modelForbidden" unit="分钟" :min="0" :max="1440" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="封禁保护">
        <template #info>
          <p>账号被上游以违反服务条款（TOS_VIOLATION）封禁时，这个账号总会自动停用。开启后整个号池再暂停一段时间：期间不选任何账号，请求直接返回 503，后台也不刷新额度，避免同一批账号接着被连带。</p>
          <p>暂停到时自动恢复，也可以在概览或账号页提前恢复。没有证据表明暂停能避免连带封号，所以默认关。</p>
        </template>
        <SettingRow label="封禁后暂停号池" description="暂停期间所有请求返回 503" input-id="sched-tos-pause">
          <Switch id="sched-tos-pause" v-model="form.tosPause" />
        </SettingRow>
        <SettingRow label="暂停时长" input-id="sched-tos-hours">
          <UnitInput id="sched-tos-hours" v-model="form.tosHours" unit="小时" :min="1" :max="72" :disabled="!form.tosPause" />
        </SettingRow>
      </SettingCard>
    </SettingGrid>

    <!-- 恢复默认值只改表单，和其他修改一样要保存才生效 -->
    <div>
      <Button variant="outline" :disabled="isDefault" @click="restoreDefaults">恢复默认值</Button>
    </div>

    <UnsavedBar :dirty="dirty" :saving="saving" :problem="problem" @save="save" @discard="discard" />
  </div>
</template>
