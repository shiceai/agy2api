<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Trash2 } from '@lucide/vue'
import { api, errorMessage, type RetentionView } from '@/api/client'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { fmtAgo, fmtBytes, fmtDuration, fmtNum, fmtTime } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { CardFooter } from '@/components/ui/card'
import SettingCard from './SettingCard.vue'
import { Switch } from '@/components/ui/switch'
import LoadError from '@/components/LoadError.vue'
import SettingRow from './SettingRow.vue'
import UnitInput from './UnitInput.vue'
import SettingGrid from './SettingGrid.vue'
import RequestCaptureCard from './RequestCaptureCard.vue'
import UnsavedBar from './UnsavedBar.vue'

// 设置页"数据"标签：用量记录的保留期和清理。超过保留期的记录（含详情）每天自动删一次，也可以立即清理；
// 删之前把 Token 归档到 Key，总量上限不受影响。旁边是失败请求的正文（见 RequestCaptureCard）。
const router = useRouter()
const confirm = useConfirm()
const view = ref<RetentionView | null>(null)
const loadError = ref('')
const enabled = ref(true)
const days = ref(90)
const saving = ref(false)
const purging = ref(false)

function apply(v: RetentionView) {
  view.value = v
  enabled.value = v.settings.enabled
  days.value = v.settings.days
}
onMounted(async () => {
  try {
    apply(await api.get<RetentionView>('/api/admin/usage/retention'))
  } catch (e) {
    loadError.value = errorMessage(e)
  }
})

const dirty = computed(() => !!view.value && (enabled.value !== view.value.settings.enabled || days.value !== view.value.settings.days))
const problem = computed(() => (Number.isInteger(days.value) && days.value >= 32 && days.value <= 3650 ? '' : '保留天数应是 32～3650 的整数'))

async function save() {
  if (problem.value) return
  saving.value = true
  try {
    apply(await api.put<RetentionView>('/api/admin/usage/retention', { enabled: enabled.value, days: days.value }))
  } catch (e) {
    toastError('保存用量保留设置失败', e)
  } finally {
    saving.value = false
  }
}

function discard() {
  if (view.value) apply(view.value)
}

// 立即清理按已保存的天数删，不能恢复，先确认。
async function purge() {
  const v = view.value
  if (!v) return
  const ok = await confirm({
    title: `清理 ${v.settings.days} 天前的用量记录？`,
    description: `将删除 ${fmtNum(v.expired)} 条记录（含详情），删除后不能恢复。密钥的总量上限不受影响。`,
    confirmText: '清理',
    destructive: true,
  })
  if (!ok) return
  purging.value = true
  try {
    apply(await api.post<RetentionView>('/api/admin/usage/retention/run'))
  } catch (e) {
    toastError('清理失败', e)
  } finally {
    purging.value = false
  }
}
</script>

<template>
  <LoadError v-if="loadError" :message="loadError" />
  <SettingGrid v-else>
    <SettingCard v-if="view" title="用量记录" description="超过保留期的记录会被删除（含详情）">
      <template #info>
        <p>删除前把它们的 Token 归档到对应的密钥，总量上限不受影响。保留期至少 32 天：每月上限要看完整的本月记录，用量页和密钥详情最长看 30 天。</p>
        <p>SQLite 删数据后文件不会变小，空出来的空间会被新记录复用。</p>
      </template>
      <SettingRow label="自动清理" description="每天一次，关闭后一直保留" input-id="retention-on">
        <Switch id="retention-on" v-model="enabled" />
      </SettingRow>
      <SettingRow label="保留天数" description="自动和立即清理都按这个天数" input-id="retention-days">
        <UnitInput id="retention-days" v-model="days" unit="天" :min="32" :max="3650" />
      </SettingRow>
      <div class="py-4 last:pb-0">
        <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
          <dt class="text-muted-foreground">现有记录</dt>
          <dd class="tabular-nums">
            {{ fmtNum(view.records) }} 条<span v-if="view.oldest_at" class="text-muted-foreground">，最早一条 {{ fmtTime(view.oldest_at) }}</span>
          </dd>
          <dt class="text-muted-foreground">待清理</dt>
          <dd class="tabular-nums">{{ fmtNum(view.expired) }} 条<span class="text-muted-foreground">（{{ view.settings.days }} 天前的）</span></dd>
          <dt class="text-muted-foreground">数据库文件</dt>
          <dd class="tabular-nums">
            {{ fmtBytes(view.db_bytes) }}
          </dd>
          <dt class="text-muted-foreground">上次清理</dt>
          <dd v-if="!view.last" class="text-muted-foreground">还没清理过</dd>
          <dd v-else-if="view.last.error" class="text-destructive">
            {{ fmtAgo(view.last.at) }}失败：{{ view.last.error
            }}<span v-if="view.last.deleted" class="text-muted-foreground">（已删 {{ fmtNum(view.last.deleted) }} 条）</span>
          </dd>
          <dd v-else class="tabular-nums" :title="fmtTime(view.last.at)">
            {{ fmtAgo(view.last.at) }}{{ view.last.manual ? '手动' : '自动' }}清理，删除 {{ fmtNum(view.last.deleted) }} 条<span
              class="text-muted-foreground"
              >，用时 {{ fmtDuration(view.last.duration_ms) }}</span
            >
          </dd>
        </dl>
      </div>
      <template #footer>
        <CardFooter class="flex flex-wrap items-center justify-end gap-3 border-t">
          <span v-if="dirty" class="text-sm text-muted-foreground">保存后再清理</span>
          <Button variant="outline" :disabled="dirty || purging || view.running || !view.expired" @click="purge">
            <Trash2 />{{ purging || view.running ? '清理中…' : '立即清理' }}
          </Button>
        </CardFooter>
      </template>
    </SettingCard>
    <RequestCaptureCard />
  </SettingGrid>
  <UnsavedBar v-if="view" :dirty="dirty" :saving="saving" :problem="problem" @save="save" @discard="discard" />
</template>
