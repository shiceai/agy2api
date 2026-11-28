<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Check, Copy } from '@lucide/vue'
import { api, type ApiKey, type ApiKeySettings, type TokenPeriod } from '@/api/client'
import { copyText } from '@/lib/clipboard'
import { guideKey } from '@/lib/guide'
import { fmtCompact, parseTokens, periodLabels } from '@/lib/keys'
import { fmtTime } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import BaseDialog from '@/components/BaseDialog.vue'
import ModelTagsInput from './ModelTagsInput.vue'

// 新建、编辑 API Key：名称、到期、Token 上限和周期、允许的模型。新建成功后在同一个弹窗里显示明文（只这一次）。
// editing 为 null 时是新建。
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ editing: ApiKey | null }>()
const emit = defineEmits<{ saved: [] }>()

type ExpiryMode = 'never' | '7' | '30' | '90' | 'date'
const expiryOptions: { value: ExpiryMode; label: string }[] = [
  { value: 'never', label: '不过期' },
  { value: '7', label: '7 天后' },
  { value: '30', label: '30 天后' },
  { value: '90', label: '90 天后' },
  { value: 'date', label: '指定日期' },
]
const day = 86400_000

const name = ref('')
const expiryMode = ref<ExpiryMode>('never')
const expiryDate = ref('') // YYYY-MM-DD，到这一天结束为止有效
const limitInput = ref('')
const period = ref<TokenPeriod>('month')
const models = ref<string[]>([])
const error = ref('')
const busy = ref(false)
const secret = ref('')
const copied = ref(false)
const suggestions = ref<string[]>([])

const pad = (n: number) => String(n).padStart(2, '0')
const toDateInput = (ms: number) => {
  const d = new Date(ms - 1) // 到期时刻是次日 0 点，减 1 毫秒回到有效的最后一天
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

// 打开时按要编辑的 Key 填表，新建时清空；取一次模型名做联想。
watch(open, async (o) => {
  if (!o) {
    // 明文不留在页面里；等淡出动画结束再清，不闪成表单。
    setTimeout(() => !open.value && (secret.value = ''), 300)
    return
  }
  const k = props.editing
  name.value = k?.name ?? ''
  expiryMode.value = k?.expires_at ? 'date' : 'never'
  expiryDate.value = k?.expires_at ? toDateInput(k.expires_at) : ''
  limitInput.value = k?.token_limit ? fmtCompact(k.token_limit) : ''
  period.value = (k?.token_period || 'month') as TokenPeriod
  models.value = [...(k?.allowed_models ?? [])]
  error.value = ''
  secret.value = ''
  try {
    const res = await api.get<{ models: string[]; families: string[] }>('/api/admin/models')
    const wildcards = ['gemini-*', 'claude-*', 'gpt-*']
    suggestions.value = [...new Set([...wildcards, ...res.families, ...res.models])]
  } catch {
    suggestions.value = []
  }
})

const limit = computed(() => parseTokens(limitInput.value))
// 到期时刻：预设按现在加天数；指定日期到那天结束（次日 0 点，本地时间）。编辑时日期没改就保留原值。
function expiresAt(): number | null {
  switch (expiryMode.value) {
    case 'never':
      return 0
    case 'date': {
      if (!expiryDate.value) return null
      const k = props.editing
      if (k?.expires_at && toDateInput(k.expires_at) === expiryDate.value) return k.expires_at
      return new Date(`${expiryDate.value}T00:00:00`).getTime() + day
    }
    default:
      return Date.now() + Number(expiryMode.value) * day
  }
}
const expiryPreview = computed(() => {
  const at = expiresAt()
  return at ? `到 ${fmtTime(at)} 失效` : ''
})

async function save() {
  error.value = ''
  const at = expiresAt()
  if (at == null) {
    error.value = '请选择到期日期'
    return
  }
  if (limit.value == null) {
    error.value = 'Token 上限格式不对，例如 500K、5M、1000000'
    return
  }
  const body: ApiKeySettings = {
    name: name.value.trim(),
    expires_at: at,
    token_limit: limit.value,
    token_period: limit.value > 0 ? period.value : '',
    allowed_models: models.value,
  }
  busy.value = true
  try {
    if (props.editing) {
      await api.put(`/api/admin/keys/${props.editing.id}`, body)
      open.value = false
    } else {
      const res = await api.post<{ secret: string }>('/api/admin/keys', body)
      secret.value = res.secret
    }
    emit('saved')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

// 查看接入示例：把刚创建的 Key 带到接入页（只在内存里，不进地址栏），示例里直接填好。
const router = useRouter()
function openGuide() {
  guideKey.value = secret.value
  open.value = false
  router.push('/guide')
}

async function copy() {
  if (!(await copyText(secret.value))) return
  copied.value = true
  setTimeout(() => (copied.value = false), 1500)
}
</script>

<template>
  <BaseDialog
    v-model:open="open"
    :title="secret ? '密钥已创建' : editing ? '编辑密钥' : '新建密钥'"
    :description="
      secret ? '请立即复制，密钥只显示这一次。在客户端里填到 API Key 处（请求头 x-goog-api-key）。' : '限制都可以留空：不过期、不限 Token、不限模型。修改后立即生效。'
    "
    size="lg"
  >
    <form v-if="!secret" class="grid min-w-0 gap-5" @submit.prevent="save">
      <div class="grid gap-2">
        <Label for="key-name">名称</Label>
        <Input id="key-name" v-model="name" placeholder="例如：claude-code" maxlength="64" />
      </div>

      <div class="grid gap-2">
        <Label for="key-expiry">到期</Label>
        <div class="flex flex-wrap items-center gap-2">
          <Select v-model="expiryMode">
            <SelectTrigger id="key-expiry" class="w-32"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="o in expiryOptions" :key="o.value" :value="o.value">{{ o.label }}</SelectItem>
            </SelectContent>
          </Select>
          <Input v-if="expiryMode === 'date'" v-model="expiryDate" type="date" aria-label="到期日期" class="w-40" />
        </div>
        <p v-if="expiryPreview" class="text-xs text-muted-foreground">{{ expiryPreview }}</p>
      </div>

      <div class="grid gap-2">
        <Label for="key-limit">Token 上限</Label>
        <div class="flex items-center gap-2">
          <Input id="key-limit" v-model="limitInput" placeholder="不限" class="w-40" inputmode="decimal" autocomplete="off" />
          <Select v-model="period" :disabled="!limit">
            <SelectTrigger class="w-24" aria-label="上限周期"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="(label, p) in periodLabels" :key="p" :value="p">{{ label }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <p class="text-xs text-muted-foreground">
          按输入 + 输出 + 推理的 Token 计，可以写 500K、5M；每天、每月按服务器时区划分。用完后从下一个请求开始拒绝。
        </p>
      </div>

      <div class="grid gap-2">
        <Label for="key-models">允许的模型</Label>
        <ModelTagsInput id="key-models" v-model="models" :suggestions="suggestions" placeholder="不限制" />
        <p class="text-xs text-muted-foreground">按客户端请求的模型名匹配，* 匹配任意字符，如 gemini-*。</p>
      </div>

      <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
    </form>
    <div v-else class="flex items-center gap-2 rounded-md border bg-muted py-1 pr-1 pl-3">
      <code class="flex-1 font-mono text-xs break-all">{{ secret }}</code>
      <Button variant="ghost" size="icon-sm" aria-label="复制" @click="copy">
        <Check v-if="copied" class="text-emerald-600" />
        <Copy v-else />
      </Button>
    </div>
    <template #footer>
      <template v-if="!secret">
        <Button variant="outline" @click="open = false">取消</Button>
        <Button :disabled="busy" @click="save">{{ editing ? '保存' : '创建' }}</Button>
      </template>
      <template v-else>
        <Button variant="outline" @click="openGuide">查看接入示例</Button>
        <Button @click="open = false">完成</Button>
      </template>
    </template>
  </BaseDialog>
</template>
