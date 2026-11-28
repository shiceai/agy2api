<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { Pencil, Plus, Send, Trash2 } from '@lucide/vue'
import { api, errorMessage, type NotifyChannel, type NotifySettings, type NotifySettingsResponse } from '@/api/client'
import { channelTypes, notifyEvents } from '@/lib/notify'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { fmtAgo } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import DotBadge from '@/components/DotBadge.vue'
import IconAction from '@/components/IconAction.vue'
import LoadError from '@/components/LoadError.vue'
import NotifyChannelDialog from './NotifyChannelDialog.vue'
import SettingCard from './SettingCard.vue'
import SettingGrid from './SettingGrid.vue'
import SettingRow from './SettingRow.vue'
import UnsavedBar from './UnsavedBar.vue'

// 设置页"通知"标签：通知渠道（添加、编辑、启停、发送测试、删除，各自立即保存）和通知事件（各事件开关、实例名称、
// 时区，用修改栏保存）。
const confirm = useConfirm()
const channels = ref<NotifyChannel[]>([])
const form = ref<NotifySettings | null>(null)
const saved = ref('') // 库里当前生效的设置（序列化后），用来判断是否有改动
const loadError = ref('')
const saving = ref(false)
const busy = ref(0) // 正在操作（启停、测试、删除）的渠道 ID
const dialogOpen = ref(false)
const editing = ref<NotifyChannel | null>(null)
const browserTz = Intl.DateTimeFormat().resolvedOptions().timeZone

function serialize(s: NotifySettings): string {
  const events = Object.fromEntries(Object.entries(s.events).sort(([a], [b]) => a.localeCompare(b)))
  return JSON.stringify({ events, instance: s.instance.trim(), timezone: s.timezone })
}
// 设置加载完才判断改动（加载前表单为空）。
const dirty = computed(() => !!form.value && serialize(form.value) !== saved.value)
const problem = computed(() => (form.value && [...form.value.instance.trim()].length > 40 ? '实例名称最多 40 个字' : ''))

function applySettings(s: NotifySettings) {
  form.value = { events: { ...s.events }, instance: s.instance ?? '', timezone: s.timezone ?? '' }
  saved.value = serialize(form.value)
}

async function loadChannels() {
  channels.value = (await api.get<{ channels: NotifyChannel[] }>('/api/admin/notify/channels')).channels
}

onMounted(async () => {
  try {
    const [res] = await Promise.all([api.get<NotifySettingsResponse>('/api/admin/settings/notify'), loadChannels()])
    applySettings(res.settings)
  } catch (e) {
    loadError.value = errorMessage(e)
  }
})

async function save() {
  const f = form.value
  if (!f || problem.value) return
  saving.value = true
  try {
    const res = await api.put<NotifySettingsResponse>('/api/admin/settings/notify', { ...f, instance: f.instance.trim() })
    applySettings(res.settings)
  } catch (e) {
    toastError('保存通知设置失败', e)
  } finally {
    saving.value = false
  }
}

function discard() {
  applySettings(JSON.parse(saved.value))
}

function openAdd() {
  editing.value = null
  dialogOpen.value = true
}
function openEdit(c: NotifyChannel) {
  editing.value = c
  dialogOpen.value = true
}
function onSaved(c: NotifyChannel) {
  const i = channels.value.findIndex((x) => x.id === c.id)
  if (i >= 0) channels.value[i] = c
  else channels.value.push(c)
}

async function setEnabled(c: NotifyChannel, on: boolean) {
  busy.value = c.id
  try {
    onSaved((await api.put<{ channel: NotifyChannel }>(`/api/admin/notify/channels/${c.id}`, { enabled: on })).channel)
  } catch (e) {
    toastError(`${on ? '启用' : '停用'}「${c.name}」失败`, e)
  } finally {
    busy.value = 0
  }
}

async function test(c: NotifyChannel) {
  busy.value = c.id
  try {
    await api.post(`/api/admin/notify/channels/${c.id}/test`)
    toast.success(`已向「${c.name}」发送测试消息`)
  } catch (e) {
    toastError(`「${c.name}」发送测试消息失败`, e)
  } finally {
    busy.value = 0
    // 最近一次的结果更新了
    loadChannels().catch(() => {})
  }
}

async function remove(c: NotifyChannel) {
  const ok = await confirm({ title: `删除通知渠道「${c.name}」？`, description: '删除后不再向它发送通知。', confirmText: '删除', destructive: true })
  if (!ok) return
  busy.value = c.id
  try {
    await api.del(`/api/admin/notify/channels/${c.id}`)
    channels.value = channels.value.filter((x) => x.id !== c.id)
  } catch (e) {
    toastError(`删除「${c.name}」失败`, e)
  } finally {
    busy.value = 0
  }
}
</script>

<template>
  <LoadError v-if="loadError" :message="loadError" />
  <div v-else-if="form" class="space-y-6">
    <SettingGrid>
      <SettingCard title="通知渠道" description="可以配多个，每个都会收到" raw content-class="px-0">
        <template #info>
          <p>通知从服务器直接发出（认 HTTPS_PROXY 等环境变量），不走账号绑定的代理。Webhook 地址、密钥和 Bot Token 加密保存，保存后不再显示。</p>
          <p>每条通知都以【AGY2API】开头：钉钉、飞书机器人用关键词作安全设置时，关键词填 AGY2API。</p>
          <p>通用 Webhook 收到 POST JSON：event（事件）、title、text（完整文字）、instance、fields（label、value 列表）、time（Unix 毫秒）。</p>
        </template>
        <template #actions>
          <Button variant="outline" size="sm" :disabled="channels.length >= 10" @click="openAdd"><Plus />添加</Button>
        </template>
        <p v-if="!channels.length" class="border-t px-6 py-8 text-center text-sm text-muted-foreground">还没有通知渠道</p>
        <ul v-else class="divide-y border-t">
          <li v-for="c in channels" :key="c.id" class="flex items-center gap-3 px-6 py-3">
            <div class="min-w-0 flex-1 space-y-1 text-sm">
              <!-- 类型和目标说明只占一行，放不下时截断（手机上右边还有开关和三个按钮） -->
              <div class="flex min-w-0 items-baseline gap-2">
                <span class="shrink-0 font-semibold">{{ c.name }}</span>
                <span class="truncate text-xs text-muted-foreground" :title="c.hint">{{ channelTypes[c.type]?.label ?? c.type }} · {{ c.hint }}</span>
              </div>
              <div class="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                <DotBadge v-if="!c.last_sent_at" tone="muted">还没发送过</DotBadge>
                <template v-else-if="c.last_error">
                  <DotBadge tone="danger" class="shrink-0">发送失败</DotBadge>
                  <span class="truncate" :title="c.last_error">{{ fmtAgo(c.last_sent_at) }} · {{ c.last_error }}</span>
                </template>
                <template v-else>
                  <DotBadge tone="success">发送成功</DotBadge>
                  <span>{{ fmtAgo(c.last_sent_at) }}</span>
                </template>
              </div>
            </div>
            <Switch
              :model-value="c.enabled"
              :disabled="busy === c.id"
              :aria-label="`启用「${c.name}」`"
              @update:model-value="setEnabled(c, Boolean($event))"
            />
            <IconAction label="发送测试消息" :disabled="busy === c.id" @click="test(c)"><Send /></IconAction>
            <IconAction label="编辑" :disabled="busy === c.id" @click="openEdit(c)"><Pencil /></IconAction>
            <IconAction label="删除" tone="danger" :disabled="busy === c.id" @click="remove(c)"><Trash2 /></IconAction>
          </li>
        </ul>
      </SettingCard>

      <SettingCard title="通知事件" description="选哪些事件要通知">
        <template #info>
          <p>账号在通知里写名称和打码的邮箱（如 a***@gmail.com）：通知会经过钉钉、飞书等第三方服务器，不发完整邮箱。</p>
          <p>同一件事在一段时间内只通知一次，间隔见各项说明；服务重启后重新计算。</p>
        </template>
        <SettingRow v-for="ev in notifyEvents" :key="ev.key" :label="ev.label" :description="ev.description" :input-id="`ne-${ev.key}`">
          <Switch :id="`ne-${ev.key}`" v-model="form.events[ev.key]" />
        </SettingRow>
        <SettingRow label="实例名称" description="写进每条通知，区分多套部署" input-id="ne-instance">
          <Input id="ne-instance" v-model="form.instance" class="w-44" maxlength="40" placeholder="不填则不写" autocomplete="off" />
        </SettingRow>
        <SettingRow label="时区" :description="form.timezone ? '通知里的时间按它显示' : '没设置，按服务器的时区显示'">
          <span v-if="form.timezone" class="text-sm">{{ form.timezone }}</span>
          <Button v-if="form.timezone !== browserTz" variant="outline" size="sm" @click="form.timezone = browserTz">改用 {{ browserTz }}</Button>
        </SettingRow>
      </SettingCard>
    </SettingGrid>

    <NotifyChannelDialog v-model:open="dialogOpen" :channel="editing" @saved="onSaved" />
    <UnsavedBar :dirty="dirty" :saving="saving" :problem="problem" @save="save" @discard="discard" />
  </div>
</template>
