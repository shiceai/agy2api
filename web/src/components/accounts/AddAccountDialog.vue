<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, Circle, CircleAlert, CircleCheck, CircleX, Copy, ExternalLink, LoaderCircle } from '@lucide/vue'
import { api, type ImportResult, type OAuthStart, type Proxy } from '@/api/client'
import { choiceOf, freeProxies } from '@/lib/proxy'
import ProxyChoiceSelect from '@/components/proxies/ProxyChoiceSelect.vue'
import { copyText } from '@/lib/clipboard'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import BaseDialog from '@/components/BaseDialog.vue'

// 添加账号：Google 登录（打开授权页 → 粘贴回调地址），或粘贴凭据批量导入。已有的账号换上新凭据。
// 每添加成功一个就通知列表刷新。
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ added: [] }>()
const props = defineProps<{ proxies: Proxy[] }>()

// 新账号绑定的代理：有空闲代理时默认自动分配，否则直连。换授权码、导入时的第一个请求就经它出网。
const proxyChoice = ref('none')
watch(open, (o) => {
  if (o) proxyChoice.value = freeProxies(props.proxies) ? 'auto' : 'none'
})
// 当前标签页；账号页提示 refresh token 失效时直接切到 Google 登录。
const tab = defineModel<'google' | 'import'>('tab', { default: 'google' })

function outcome(res: ImportResult): string {
  if (res.created) return '已添加'
  return res.reenabled ? '已更新凭据并重新启用' : '已更新凭据'
}
function outcomeDetail(res: ImportResult): string {
  const models = `${res.account.model_count} 个模型`
  return res.warning ? `${models}，模型目录没取到，稍后在账号页同步` : models
}

// --- Google 登录 ---

const session = ref<OAuthStart | null>(null)
const starting = ref(false)
const callback = ref('')
const oauthBusy = ref(false)
const oauthError = ref('')
const oauthDone = ref<ImportResult | null>(null)
const now = ref(Date.now())
// 含 // 的属性值写在脚本里再绑定：写在组件的静态属性上，vue-tsc 会把 // 当注释改写，类型检查出错（见 AGENTS.md 前端约定）。
const callbackPlaceholder = 'http://127.0.0.1:…/oauth-callback?state=…&code=…'
let timer: number | undefined
const expired = computed(() => !!session.value && now.value >= session.value.expires_at)
const minutesLeft = computed(() => (session.value ? Math.max(0, Math.ceil((session.value.expires_at - now.value) / 60_000)) : 0))

async function startSession() {
  starting.value = true
  oauthError.value = ''
  try {
    session.value = await api.post<OAuthStart>('/api/admin/accounts/oauth/start')
    now.value = Date.now()
  } catch (e) {
    oauthError.value = (e as Error).message
  } finally {
    starting.value = false
  }
}

const linkCopied = ref(false)
async function copyLink() {
  if (!session.value || !(await copyText(session.value.auth_url))) return
  linkCopied.value = true
  setTimeout(() => (linkCopied.value = false), 1500)
}

async function completeOAuth() {
  if (!session.value) return
  oauthBusy.value = true
  oauthError.value = ''
  oauthDone.value = null
  try {
    oauthDone.value = await api.post<ImportResult>('/api/admin/accounts/oauth/complete', {
      session_id: session.value.session_id,
      callback_url: callback.value.trim(),
      proxy: choiceOf(proxyChoice.value),
    })
    callback.value = ''
    emit('added')
    // 授权链接只能用一次，备好下一个，方便接着添加别的账号。
    await startSession()
  } catch (e) {
    oauthError.value = (e as Error).message
  } finally {
    oauthBusy.value = false
  }
}

// --- 导入凭据 ---

interface Item {
  label: string
  body: Record<string, unknown>
  state: 'pending' | 'running' | 'ok' | 'error'
  status: string
  detail: string
}
const importText = ref('')
const importPlaceholder = '1//0e…（每行一个）或 {"refresh_token":"…"}'
const importError = ref('')
const items = ref<Item[]>([])
const importing = ref(false)
const doneCount = computed(() => items.value.filter((i) => i.state === 'ok' || i.state === 'error').length)

// 每行一个 refresh token；以 { 或 [ 开头的按 JSON 解析（codex2api 导出的单个对象或数组），只取导入用得到的字段。
function parse(text: string): Item[] {
  const t = text.trim()
  const item = (label: string, body: Record<string, unknown>): Item => ({ label, body, state: 'pending', status: '等待', detail: '' })
  if (t.startsWith('{') || t.startsWith('[')) {
    const v: unknown = JSON.parse(t)
    const list = (Array.isArray(v) ? v : [v]) as Record<string, unknown>[]
    return list.map((o, i) => {
      const pick: Record<string, unknown> = {}
      for (const k of ['refresh_token', 'client_id', 'client_secret', 'project_id', 'user_project_id']) {
        if (o?.[k] != null) pick[k] = o[k]
      }
      return item(typeof o?.email === 'string' && o.email ? o.email : `第 ${i + 1} 个`, pick)
    })
  }
  return t
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((rt, i) => item(`第 ${i + 1} 行`, { refresh_token: rt }))
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// 逐个导入，间隔 2 秒，不一起打到上游。
async function runImport() {
  importError.value = ''
  let list: Item[]
  try {
    list = parse(importText.value)
  } catch {
    importError.value = 'JSON 格式不对'
    return
  }
  if (!list.length) {
    importError.value = '没有可导入的内容'
    return
  }
  items.value = list
  importing.value = true
  for (const [i, it] of items.value.entries()) {
    if (i > 0) await sleep(2000)
    it.state = 'running'
    it.status = '导入中'
    try {
      const res = await api.post<ImportResult>('/api/admin/accounts/import', { ...it.body, proxy: choiceOf(proxyChoice.value) })
      it.state = 'ok'
      it.label = res.account.email
      it.status = outcome(res)
      it.detail = outcomeDetail(res)
      emit('added')
    } catch (e) {
      it.state = 'error'
      it.status = '失败'
      it.detail = (e as Error).message
    }
  }
  importing.value = false
  importText.value = ''
}

// 打开弹窗、切到 Google 登录时备好授权链接：链接是真正的 <a>，点击直接打开，不会被拦截弹窗。
watch(
  [open, tab],
  ([o, t]) => {
    if (o) now.value = Date.now()
    if (o && t === 'google' && !starting.value && (!session.value || expired.value)) startSession()
    if (o && timer === undefined) timer = window.setInterval(() => (now.value = Date.now()), 5000)
    if (!o) {
      window.clearInterval(timer)
      timer = undefined
      callback.value = ''
      oauthError.value = ''
      oauthDone.value = null
      if (!importing.value) items.value = []
    }
  },
  { immediate: true },
)
onBeforeUnmount(() => window.clearInterval(timer))
</script>

<template>
  <BaseDialog v-model:open="open" title="添加账号" description="添加后立即同步身份、项目和模型目录；已有的账号会换上新凭据。" size="lg">
    <div class="grid min-w-0 gap-2">
      <Label for="add-proxy">代理</Label>
      <ProxyChoiceSelect id="add-proxy" v-model="proxyChoice" :proxies="proxies" allow-auto :disabled="importing" />
      <p class="text-xs text-muted-foreground">
        新账号从添加这一步起所有请求都经这个代理；已有的账号重新添加时保留原来的代理。浏览器登录 Google 那一步走的是你自己的网络。
      </p>
    </div>

    <Tabs v-model="tab" class="min-w-0 gap-4">
      <TabsList class="w-full">
        <TabsTrigger value="google">Google 登录</TabsTrigger>
        <TabsTrigger value="import">导入凭据</TabsTrigger>
      </TabsList>

      <TabsContent value="google" class="min-w-0 space-y-5">
        <section class="space-y-2.5">
          <h3 class="flex items-start gap-2 text-sm font-semibold">
            <span class="flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-xs">1</span>
            打开授权页，用要添加的 Google 账号登录并同意授权
          </h3>
          <div class="flex flex-wrap items-center gap-2 pl-7">
            <Button v-if="expired" size="sm" :disabled="starting" @click="startSession">重新生成链接</Button>
            <Button v-else-if="session" as="a" size="sm" :href="session.auth_url" target="_blank" rel="noopener noreferrer">
              打开授权页<ExternalLink />
            </Button>
            <Button v-else size="sm" disabled>{{ starting ? '正在生成链接…' : '打开授权页' }}</Button>
            <Button variant="outline" size="sm" :disabled="!session || expired" @click="copyLink">
              <Check v-if="linkCopied" /><Copy v-else />{{ linkCopied ? '已复制' : '复制链接' }}
            </Button>
            <span v-if="session && !expired" class="text-xs text-muted-foreground">{{ minutesLeft }} 分钟内有效</span>
            <span v-else-if="expired" class="text-xs text-muted-foreground">链接已过期</span>
          </div>
          <p class="pl-7 text-xs text-muted-foreground">要添加的不是浏览器当前登录的账号时，可以复制链接到无痕窗口打开。</p>
        </section>

        <section class="space-y-2.5">
          <h3 class="flex items-start gap-2 text-sm font-semibold">
            <span class="flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-xs">2</span>
            <Label for="oauth-callback" class="font-semibold">粘贴授权后的地址</Label>
          </h3>
          <div class="space-y-2 pl-7">
            <p class="text-xs text-muted-foreground">授权后浏览器会跳到一个打不开的 127.0.0.1 页面，这是正常的，把地址栏里的完整地址复制过来。</p>
            <Input
              id="oauth-callback"
              v-model="callback"
              class="font-mono text-xs"
              :placeholder="callbackPlaceholder"
              autocomplete="off"
              @keydown.enter="callback.trim() && !oauthBusy && completeOAuth()"
            />
          </div>
        </section>

        <Alert v-if="oauthError" variant="destructive">
          <CircleAlert />
          <AlertDescription class="wrap-anywhere">{{ oauthError }}</AlertDescription>
        </Alert>
        <Alert v-else-if="oauthDone">
          <!-- Alert 会把图标设成 text-current，用 ! 保留绿色 -->
          <CircleCheck class="text-emerald-600! dark:text-emerald-400!" />
          <AlertTitle>{{ outcome(oauthDone) }} {{ oauthDone.account.email }}</AlertTitle>
          <AlertDescription>{{ outcomeDetail(oauthDone) }}。要继续添加，用新的授权链接再走一遍。</AlertDescription>
        </Alert>
      </TabsContent>

      <TabsContent value="import" class="min-w-0 space-y-2">
        <Label for="import-credential">凭据</Label>
        <!-- 随内容长高，从约 6 行到约 16 行，再多在框内滚动，粘贴大段 JSON 时弹窗不会超出屏幕 -->
        <Textarea
          id="import-credential"
          v-model="importText"
          :disabled="importing"
          class="max-h-72 min-h-28 overflow-y-auto font-mono text-xs break-all"
          :placeholder="importPlaceholder"
        />
        <p class="text-xs text-muted-foreground">每行一个 refresh token，或粘贴 codex2api 导出的 JSON（单个或数组）。多个账号逐个导入，间隔 2 秒。</p>
        <p v-if="importError" class="text-sm text-destructive">{{ importError }}</p>
        <ul v-if="items.length" class="divide-y rounded-md border text-sm" aria-live="polite">
          <li v-for="(it, i) in items" :key="i" class="flex gap-2 px-3 py-2">
            <LoaderCircle v-if="it.state === 'running'" class="mt-0.5 size-4 shrink-0 animate-spin text-muted-foreground" />
            <CircleCheck v-else-if="it.state === 'ok'" class="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
            <CircleX v-else-if="it.state === 'error'" class="mt-0.5 size-4 shrink-0 text-destructive" />
            <Circle v-else class="mt-0.5 size-4 shrink-0 text-muted-foreground/50" />
            <div class="min-w-0 flex-1">
              <div class="flex items-baseline justify-between gap-3">
                <span class="truncate">{{ it.label }}</span>
                <span :class="['shrink-0 text-xs', it.state === 'error' ? 'text-destructive' : 'text-muted-foreground']">{{ it.status }}</span>
              </div>
              <p v-if="it.detail" :class="['text-xs wrap-anywhere', it.state === 'error' ? 'text-destructive' : 'text-muted-foreground']">
                {{ it.detail }}
              </p>
            </div>
          </li>
        </ul>
      </TabsContent>
    </Tabs>

    <template #footer>
      <Button variant="outline" @click="open = false">关闭</Button>
      <Button v-if="tab === 'google'" :disabled="!session || !callback.trim() || oauthBusy" @click="completeOAuth">
        {{ oauthBusy ? '添加中…' : '完成添加' }}
      </Button>
      <Button v-else :disabled="importing || !importText.trim()" @click="runImport">
        {{ importing ? `导入中 ${doneCount}/${items.length}` : '导入' }}
      </Button>
    </template>
  </BaseDialog>
</template>
