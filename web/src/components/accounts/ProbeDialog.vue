<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CircleAlert, CircleCheck, LoaderCircle } from '@lucide/vue'
import { api, type Account, type ProbeResult } from '@/api/client'
import { fmtDuration, fmtSpan } from '@/lib/utils'
import { resultLabel } from '@/lib/usage'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import HttpStatus from '@/components/HttpStatus.vue'

// 账号检测：用这个账号发一条 "Hi"（非流式），和正常请求一样经过上游、消耗少量真实额度；失败时和正常请求
// 一样冷却或停用。停用的账号也能检测，通过后可以直接启用。只能手动、一次一个。
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ account: Account }>()
const emit = defineEmits<{ changed: [] }>()

// 补全用的 tab_* 模型不是对话模型，不列出。默认 gemini-2.5-flash，没有就选第一个 Gemini flash。
const models = computed(() =>
  (props.account.models ?? [])
    .map((m) => m.id)
    .filter((id) => !id.startsWith('tab_'))
    .sort(),
)
function defaultModel(): string {
  const list = models.value
  if (list.includes('gemini-2.5-flash')) return 'gemini-2.5-flash'
  return list.find((id) => id.startsWith('gemini') && id.includes('flash')) ?? list[0] ?? ''
}

const model = ref('')
const busy = ref(false)
const result = ref<ProbeResult | null>(null)
const error = ref('')
const enabling = ref(false)

// 每次打开都回到默认模型，不沿用上次（可能是别的账号、更贵的模型）。
watch(open, (o) => {
  if (!o) return
  model.value = defaultModel()
  result.value = null
  error.value = ''
})

async function run() {
  busy.value = true
  result.value = null
  error.value = ''
  try {
    result.value = await api.post<ProbeResult>(`/api/admin/accounts/${props.account.id}/test`, { model: model.value })
    emit('changed')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

async function enable() {
  enabling.value = true
  try {
    await api.post(`/api/admin/accounts/${props.account.id}/enable`)
    emit('changed')
    open.value = false
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    enabling.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>检测账号</DialogTitle>
        <DialogDescription>
          用 {{ account.email }} 发一条 "Hi"，和正常请求一样经过上游、记进用量，会消耗少量额度；失败时同样会冷却或停用。
        </DialogDescription>
      </DialogHeader>

      <div class="grid min-w-0 gap-2">
        <Label for="probe-model">模型</Label>
        <Select v-model="model" :disabled="busy">
          <SelectTrigger id="probe-model" class="w-full"><SelectValue placeholder="选择模型" /></SelectTrigger>
          <SelectContent class="max-h-72">
            <SelectItem v-for="id in models" :key="id" :value="id">{{ id }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
      <Alert v-else-if="result?.ok">
        <!-- Alert 会把图标设成 text-current，用 ! 保留绿色 -->
        <CircleCheck class="text-emerald-600! dark:text-emerald-400!" />
        <AlertTitle class="flex flex-wrap items-center gap-x-2 gap-y-1">
          检测通过 <HttpStatus v-if="result.status" :code="result.status" />
          <span class="text-xs font-normal text-muted-foreground tabular-nums">{{ fmtDuration(result.latency_ms) }}</span>
        </AlertTitle>
        <AlertDescription class="space-y-2">
          <p class="wrap-anywhere">回复：{{ result.reply || '（没有正文）' }}</p>
          <div v-if="account.status !== 'enabled'" class="flex flex-wrap items-center gap-2">
            <span>账号目前是停用状态，可以重新启用。</span>
            <Button size="xs" variant="outline" :disabled="enabling" @click="enable">启用账号</Button>
          </div>
        </AlertDescription>
      </Alert>
      <Alert v-else-if="result" variant="destructive">
        <CircleAlert />
        <AlertTitle class="flex flex-wrap items-center gap-x-2 gap-y-1">
          {{ resultLabel(result.kind ?? 'error') }} <HttpStatus v-if="result.status" :code="result.status" />
          <span class="text-xs font-normal tabular-nums opacity-80">{{ fmtDuration(result.latency_ms) }}</span>
        </AlertTitle>
        <AlertDescription class="space-y-1">
          <p v-if="result.message" class="wrap-anywhere">{{ result.message }}</p>
          <p v-if="result.disabled">已停用账号：{{ result.disabled }}</p>
          <p v-else-if="result.cooldown_ms">已进入冷却，{{ fmtSpan(result.cooldown_ms) }}后恢复</p>
        </AlertDescription>
      </Alert>

      <DialogFooter>
        <Button variant="outline" @click="open = false">关闭</Button>
        <Button :disabled="busy || !model" @click="run">
          <LoaderCircle v-if="busy" class="animate-spin" />{{ busy ? '检测中…' : result ? '再检测一次' : '开始检测' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
