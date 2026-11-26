<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Check } from '@lucide/vue'
import { api, errorMessage, type Scheduling, type SchedulingResponse } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import SettingGrid from './SettingGrid.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'
import UnitInput from './UnitInput.vue'

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
  sticky: boolean
  stickyMin: number
}
const MIN = 60
const QUOTA_DEFAULT_MIN = 15
const IDLE_DEFAULT_SECS = 120

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
    sticky: s.sticky,
    stickyMin: s.sticky_ttl / MIN,
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
    sticky: f.sticky,
    sticky_ttl: r(f.stickyMin * MIN),
    weekly_quota: f.weekly,
  }
}

const router = useRouter()
const form = ref<Form | null>(null)
const defaults = ref<Scheduling | null>(null)
const saved = ref('') // 库里当前生效的设置（序列化后），用来判断是否有改动
const error = ref('')
const saving = ref(false)
const justSaved = ref(false)

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
  error.value = ''
  try {
    apply(await api.put<SchedulingResponse>('/api/admin/settings/scheduling', toApi(form.value)))
    justSaved.value = true
    setTimeout(() => (justSaved.value = false), 1500)
  } catch (e) {
    handle(e)
  } finally {
    saving.value = false
  }
}

function restoreDefaults() {
  if (defaults.value) form.value = toForm(defaults.value)
}
</script>

<!-- 设置页"账号调度"标签：令牌刷新、并发、会话粘性、代理、流式、额度、重试与冷却。 -->
<template>
  <div v-if="form" class="space-y-6">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <SettingGrid>
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

      <SettingCard title="会话粘性" description="同一对话优先用上次的账号">
        <template #info>
          <p>同一对话指同一密钥、同一模型、同一首条用户消息。上游缓存按账号，换号就命中不了。</p>
          <p>原账号冷却、并发满、额度低或失败时才换号，之后粘到新账号上。记录只在内存里，重启后清空。</p>
        </template>
        <SettingRow label="开启会话粘性" description="关闭后每个请求轮询选号" input-id="sched-sticky">
          <Switch id="sched-sticky" v-model="form.sticky" />
        </SettingRow>
        <SettingRow label="闲置多久后不再粘" description="超过后当作新对话" input-id="sched-sticky-ttl">
          <UnitInput id="sched-sticky-ttl" v-model="form.stickyMin" unit="分钟" :min="5" :max="1440" :disabled="!form.sticky" />
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
    </SettingGrid>

    <div class="flex flex-wrap items-center gap-3">
      <Button :disabled="!dirty || saving || !!problem" @click="save">保存</Button>
      <Button variant="outline" :disabled="isDefault" @click="restoreDefaults">恢复默认值</Button>
      <span v-if="problem" class="text-sm text-destructive">{{ problem }}</span>
      <span v-else-if="justSaved" class="flex items-center gap-1 text-sm text-muted-foreground"><Check class="size-4" />已保存</span>
      <span v-else class="text-sm text-muted-foreground">保存后立即生效，不用重启。</span>
    </div>
  </div>
</template>
