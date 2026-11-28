<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, errorMessage, type Warmup, type WarmupResponse } from '@/api/client'
import { Switch } from '@/components/ui/switch'
import SettingGrid from './SettingGrid.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'
import UnitInput from './UnitInput.vue'
import UnsavedBar from './UnsavedBar.vue'
import { toastError } from '@/lib/toast'

const form = ref<Warmup | null>(null)
const saved = ref('')
const error = ref('')
const saving = ref(false)

const dirty = computed(() => !!form.value && JSON.stringify(form.value) !== saved.value)

function inRange(v: number, lo: number, hi: number) {
  return Number.isFinite(v) && v >= lo && v <= hi
}
// 和服务端取值范围一致，先在界面上提示。
const problem = computed(() => {
  const f = form.value
  if (!f) return ''
  const checks: [boolean, string][] = [
    [inRange(f.initial_concurrency, 1, 1000), '初始并发应在 1～1000'],
    [inRange(f.successes, 0, 100000), '成功数应在 0～100000'],
    [inRange(f.age_minutes, 0, 10080), '号龄应在 0～10080 分钟'],
    [inRange(f.initial_interval_ms, 0, 600000), '初始间隔应在 0～600000ms'],
    [inRange(f.min_interval_ms, 0, f.initial_interval_ms), '稳态间隔应在 0 到初始间隔之间'],
  ]
  return checks.find(([ok]) => !ok)?.[1] ?? ''
})

function apply(res: WarmupResponse) {
  form.value = { ...res.settings }
  saved.value = JSON.stringify(res.settings)
}

onMounted(async () => {
  try {
    apply(await api.get<WarmupResponse>('/api/admin/settings/warmup'))
  } catch (e) {
    error.value = errorMessage(e)
  }
})

async function save() {
  if (!form.value || problem.value) return
  saving.value = true
  error.value = ''
  try {
    apply(await api.put<WarmupResponse>('/api/admin/settings/warmup', form.value))
  } catch (e) {
    toastError('保存预热节奏失败', e)
  } finally {
    saving.value = false
  }
}

function discard() {
  form.value = JSON.parse(saved.value)
}
</script>

<!-- 设置页"预热节奏"标签：新号逐步放量、隔开同一账号的请求，降低新账号被风控的概率。 -->
<template>
  <div v-if="form" class="space-y-6">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <SettingGrid>
      <SettingCard title="新号预热" description="新号起步的并发，攒够成功数或够老就放开">
        <template #info>
          <p>刚导入的账号不立刻承接全部流量，而是从低并发、较大请求间隔起步，随号龄和累计成功数逐步放开到正常水平，让上游看到渐进的使用曲线。整套由这里的开关控制，默认关。</p>
          <p>预热进度按累计成功数和号龄各算一份、取较大者：任一达标即视为预热完成。用较大者是为了重启后丢失成功计数时，老账号靠号龄仍是满进度、不会被重新限流。</p>
        </template>
        <SettingRow label="启用" description="关闭时所有账号都按正常并发、不限间隔" input-id="wu-enabled">
          <Switch id="wu-enabled" v-model="form.enabled" />
        </SettingRow>
        <SettingRow label="初始并发" description="预热初期的并发上限">
          <UnitInput v-model="form.initial_concurrency" unit="个" :min="1" :max="64" :disabled="!form.enabled" label="初始并发" />
        </SettingRow>
        <SettingRow label="成功数" description="攒够这么多次成功即放开，0 表示不按成功数">
          <UnitInput v-model="form.successes" unit="次" :min="0" :max="100000" :disabled="!form.enabled" label="成功数" />
        </SettingRow>
        <SettingRow label="号龄" description="号龄达到即放开，0 表示不按号龄">
          <UnitInput v-model="form.age_minutes" unit="分钟" :min="0" :max="10080" :disabled="!form.enabled" label="号龄" />
        </SettingRow>
      </SettingCard>
      <SettingCard title="请求节奏" description="同一账号两次请求的最小间隔，从初期降到稳态">
        <SettingRow label="初始间隔" description="预热初期的最小间隔">
          <UnitInput v-model="form.initial_interval_ms" unit="ms" :min="0" :max="600000" :disabled="!form.enabled" label="初始间隔" />
        </SettingRow>
        <SettingRow label="稳态间隔" description="预热完成后的最小间隔，0 表示不限">
          <UnitInput v-model="form.min_interval_ms" unit="ms" :min="0" :max="600000" :disabled="!form.enabled" label="稳态间隔" />
        </SettingRow>
      </SettingCard>
    </SettingGrid>

    <UnsavedBar :dirty="dirty" :saving="saving" :problem="problem" @save="save" @discard="discard" />
  </div>
</template>
