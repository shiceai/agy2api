<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, errorMessage, type NotifyChannel, type NotifyChannelType } from '@/api/client'
import { channelTypeOrder, channelTypes } from '@/lib/notify'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import BaseDialog from '@/components/BaseDialog.vue'

// 添加或编辑通知渠道。编辑时类型不能改；Webhook 地址、密钥、Bot Token 保存后不再显示，编辑时留空就是不改。
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ channel: NotifyChannel | null }>()
const emit = defineEmits<{ saved: [channel: NotifyChannel] }>()

const type = ref<NotifyChannelType>('webhook')
const name = ref('')
const url = ref('')
const secret = ref('')
const token = ref('')
const chatId = ref('')
const busy = ref(false)
const error = ref('')

watch(open, (o) => {
  if (!o) return
  const c = props.channel
  type.value = c?.type ?? 'webhook'
  name.value = c?.name ?? ''
  url.value = ''
  secret.value = ''
  token.value = ''
  chatId.value = c?.chat_id ?? ''
  error.value = ''
})

const editing = computed(() => !!props.channel)
const info = computed(() => channelTypes[type.value])
const isTelegram = computed(() => type.value === 'telegram')
// 编辑时已经设了的凭据：输入框留空表示不改。
const urlPlaceholder = computed(() => (editing.value ? `已设置（${props.channel?.hint}），留空不改` : (info.value.urlExample ?? '')))
const secretPlaceholder = computed(() => (editing.value && props.channel?.has_secret ? '已设置，留空不改' : '可不填'))
const tokenPlaceholder = computed(() => (editing.value ? '已设置，留空不改' : '123456789:AA…'))

const canSave = computed(() => {
  if (!name.value.trim() || busy.value) return false
  if (editing.value) return !isTelegram.value || !!chatId.value.trim()
  return isTelegram.value ? !!token.value.trim() && !!chatId.value.trim() : !!url.value.trim()
})

async function save() {
  if (!canSave.value) return
  const body: Record<string, unknown> = { name: name.value.trim() }
  const set = (key: string, v: string) => {
    if (v.trim() || !editing.value) body[key] = v.trim()
  }
  if (isTelegram.value) {
    set('token', token.value)
    body.chat_id = chatId.value.trim()
  } else {
    set('url', url.value)
    if (info.value.secretLabel && secret.value.trim()) body.secret = secret.value.trim()
  }
  busy.value = true
  error.value = ''
  try {
    const res = editing.value
      ? await api.put<{ channel: NotifyChannel }>(`/api/admin/notify/channels/${props.channel!.id}`, body)
      : await api.post<{ channel: NotifyChannel }>('/api/admin/notify/channels', { ...body, type: type.value })
    emit('saved', res.channel)
    open.value = false
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <BaseDialog v-model:open="open" :title="editing ? '编辑通知渠道' : '添加通知渠道'" :description="info.hint">
    <form class="grid min-w-0 gap-4" @submit.prevent="save">
      <div class="grid gap-2">
        <Label for="nc-type">类型</Label>
        <Select v-model="type" :disabled="editing">
          <SelectTrigger id="nc-type" class="w-full"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="t in channelTypeOrder" :key="t" :value="t">{{ channelTypes[t].label }}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="grid gap-2">
        <Label for="nc-name">名称</Label>
        <Input id="nc-name" v-model="name" maxlength="40" autocomplete="off" :placeholder="`如：运维${info.label}群`" />
      </div>
      <template v-if="isTelegram">
        <div class="grid gap-2">
          <Label for="nc-token">Bot Token</Label>
          <Input id="nc-token" v-model="token" type="password" class="font-mono" autocomplete="off" :placeholder="tokenPlaceholder" />
        </div>
        <div class="grid gap-2">
          <Label for="nc-chat">Chat ID</Label>
          <Input id="nc-chat" v-model="chatId" class="font-mono" autocomplete="off" placeholder="数字 ID 或 @频道名" />
        </div>
      </template>
      <template v-else>
        <div class="grid gap-2">
          <Label for="nc-url">Webhook 地址</Label>
          <Input id="nc-url" v-model="url" class="font-mono text-xs" autocomplete="off" spellcheck="false" :placeholder="urlPlaceholder" />
        </div>
        <div v-if="info.secretLabel" class="grid gap-2">
          <Label for="nc-secret">{{ info.secretLabel }}</Label>
          <Input id="nc-secret" v-model="secret" type="password" class="font-mono" autocomplete="off" :placeholder="secretPlaceholder" />
        </div>
      </template>
      <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
      <!-- 让回车能提交表单 -->
      <button type="submit" class="hidden" aria-hidden="true" tabindex="-1" />
    </form>

    <template #footer>
      <Button variant="outline" @click="open = false">取消</Button>
      <Button :disabled="!canSave" @click="save">{{ busy ? '保存中…' : '保存' }}</Button>
    </template>
  </BaseDialog>
</template>
