<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Trash2 } from '@lucide/vue'
import { api, errorMessage, type RequestCaptureView } from '@/api/client'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { fmtBytes, fmtNum } from '@/lib/utils'
import { CardFooter } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import LoadError from '@/components/LoadError.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'

// 设置页"数据"标签的失败请求正文：开关（切换即保存，默认关）、已存的条数和占用、清空。开着时上游返回了错误的请求
// 把发给上游的请求体存下，在用量详情里下载，排查上游为什么拒绝；含用户的对话内容。
const confirm = useConfirm()
const view = ref<RequestCaptureView | null>(null)
const loadError = ref('')
const saving = ref(false)
const clearing = ref(false)

onMounted(async () => {
  try {
    view.value = await api.get<RequestCaptureView>('/api/admin/usage/request-bodies')
  } catch (e) {
    loadError.value = errorMessage(e)
  }
})

async function setEnabled(on: boolean) {
  saving.value = true
  try {
    view.value = await api.put<RequestCaptureView>('/api/admin/usage/request-bodies', { enabled: on })
  } catch (e) {
    toastError(on ? '开启保存正文失败' : '关闭保存正文失败', e)
  } finally {
    saving.value = false
  }
}

// 清空不能恢复，先确认；开关不变。
async function clear() {
  const v = view.value
  if (!v) return
  const ok = await confirm({
    title: '清空已保存的失败请求正文？',
    description: `将删除 ${fmtNum(v.count)} 条正文，删除后不能恢复。用量记录和开关不变。`,
    confirmText: '清空',
    destructive: true,
  })
  if (!ok) return
  clearing.value = true
  try {
    view.value = await api.del<RequestCaptureView>('/api/admin/usage/request-bodies')
  } catch (e) {
    toastError('清空失败请求的正文失败', e)
  } finally {
    clearing.value = false
  }
}
</script>

<template>
  <LoadError v-if="loadError" :message="loadError" />
  <SettingCard v-else-if="view" title="失败请求的正文" description="存下上游拒绝的请求，排查被拒的原因">
    <template #info>
      <p>只存上游返回了错误的请求，成功的请求不存。压缩和写入都在后台，不影响响应速度。</p>
      <p>
        每分钟最多存 {{ view.per_minute }} 条，超出的跳过；最多保留最近 {{ fmtNum(view.max_count) }} 条、共 {{ fmtBytes(view.max_bytes) }}，
        超过用量保留期的随用量记录一起删。
      </p>
      <p>在用量详情里下载，内容是实际发给上游的请求（已做格式转换和系统提示词处理）。</p>
    </template>
    <SettingRow label="保存正文" description="会保存用户的对话内容" input-id="capture-on">
      <Switch id="capture-on" :model-value="view.settings.enabled" :disabled="saving" @update:model-value="setEnabled(Boolean($event))" />
    </SettingRow>
    <div class="py-4 last:pb-0">
      <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-sm">
        <dt class="text-muted-foreground">已保存</dt>
        <dd class="tabular-nums">
          {{ fmtNum(view.count) }} 条<span v-if="view.count" class="text-muted-foreground">，共 {{ fmtBytes(view.bytes) }}</span>
        </dd>
      </dl>
    </div>
    <template #footer>
      <CardFooter class="flex justify-end border-t">
        <Button variant="outline" :disabled="clearing || !view.count" @click="clear"><Trash2 />{{ clearing ? '清空中…' : '清空' }}</Button>
      </CardFooter>
    </template>
  </SettingCard>
</template>
