<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ArrowUpCircle, Download, LoaderCircle, RefreshCw, TriangleAlert, Undo2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { api, errorMessage, fetchSession, type SystemView, type UpgradeState } from '@/api/client'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { setUpdate } from '@/lib/version'
import { fmtAgo, fmtTime } from '@/lib/utils'
import { Alert, AlertAction, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { CardFooter } from '@/components/ui/card'
import IconAction from '@/components/IconAction.vue'
import LoadError from '@/components/LoadError.vue'
import ReleaseNotes from './ReleaseNotes.vue'
import SettingCard from './SettingCard.vue'

// 设置页"版本"：当前版本、GitHub 上最新的正式版、一键在线升级和撤销。升级在服务端后台进行，这里轮询进度；
// 服务重启后轮询到新版本就刷新页面（前端打包在程序里，刷新才是新界面）。
const confirm = useConfirm()
const view = ref<SystemView | null>(null)
const loadError = ref('')
const checking = ref(false)
const acting = ref(false)
// waiting 是重启后等待的版本，非空时在轮询新进程。
const waiting = ref('')
let timer: ReturnType<typeof setTimeout> | undefined
let alive = true
onBeforeUnmount(() => {
  alive = false
  clearTimeout(timer)
})

const HOUR = 3_600_000
const RESTART_TIMEOUT = 90_000
const working: (UpgradeState | undefined)[] = ['downloading', 'verifying', 'backing_up']
const busy = computed(() => !!waiting.value || working.includes(view.value?.progress.state) || view.value?.progress.state === 'restarting')

function apply(v: SystemView) {
  view.value = v
  setUpdate(v.update_available ? v.latest?.version : undefined)
}

onMounted(async () => {
  try {
    apply(await api.get<SystemView>('/api/admin/system'))
  } catch (e) {
    loadError.value = errorMessage(e)
    return
  }
  const v = view.value!
  if (working.includes(v.progress.state)) poll()
  else if (v.progress.state === 'restarting') waitFor(v.progress.target ?? '', `已运行 ${v.progress.target}`)
  else if (v.can_check && (!v.checked_at || Date.now() - v.checked_at > HOUR)) check(false)
})

async function check(manual = true) {
  checking.value = true
  try {
    const v = await api.post<SystemView>('/api/admin/system/check')
    apply(v)
    if (manual && v.check_error) toastError('检查新版本失败', new Error(v.check_error))
  } catch (e) {
    if (manual) toastError('检查新版本失败', e)
  } finally {
    checking.value = false
  }
}

// poll 轮询升级进度，到重启阶段转去等新版本。
function poll() {
  clearTimeout(timer)
  timer = setTimeout(async () => {
    if (!alive) return
    const target = view.value?.progress.target ?? ''
    try {
      const v = await api.get<SystemView>('/api/admin/system')
      apply(v)
      const p = v.progress
      if (working.includes(p.state)) poll()
      else if (p.state === 'restarting') waitFor(target, `已升级到 ${target}`)
      else if (p.state === 'failed') toastError(`升级到 ${target} 失败`, new Error(p.error))
    } catch {
      waitFor(target, `已升级到 ${target}`) // 连不上：两次轮询之间已经开始重启
    }
  }, 800)
}

// waitFor 等服务重启后运行的版本变成 target，成功后刷新页面。
function waitFor(target: string, done: string) {
  clearTimeout(timer)
  waiting.value = target
  const deadline = Date.now() + RESTART_TIMEOUT
  let seen = ''
  const tick = async () => {
    if (!alive) return
    try {
      seen = (await fetchSession()).version
      if (seen === target) {
        toast.success(done, { description: '页面即将刷新' })
        setTimeout(() => location.reload(), 1500)
        return
      }
    } catch {
      // 还在重启
    }
    if (Date.now() < deadline) {
      timer = setTimeout(tick, 1000)
      return
    }
    waiting.value = ''
    toastError(`${target} 没有在 90 秒内启动`, new Error(seen ? `现在运行的是 ${seen}，请查看服务日志` : '服务还没有响应，请查看服务日志'))
    api.get<SystemView>('/api/admin/system').then(apply, () => {})
  }
  timer = setTimeout(tick, 1000)
}

async function upgrade() {
  const target = view.value?.latest?.version
  if (!target) return
  const ok = await confirm({
    title: `升级到 ${target}？`,
    description: '下载并校验新版本、备份数据库，然后重启服务。重启时服务中断几秒，进行中的请求会先完成。',
    confirmText: '升级',
  })
  if (!ok) return
  acting.value = true
  try {
    apply(await api.post<SystemView>('/api/admin/system/upgrade', { version: target }))
    poll()
  } catch (e) {
    toastError(`升级到 ${target} 失败`, e)
  } finally {
    acting.value = false
  }
}

// revert 撤销在线升级：运行的正是它时重启回到自带的版本；只是停用的残留时直接清掉。
async function revert() {
  const v = view.value
  if (!v) return
  const running = !!v.installed_at
  if (running) {
    const ok = await confirm({
      title: `撤销在线升级，回到 ${v.base_version}？`,
      description: v.new_migrations
        ? `${v.version} 改过数据库结构，${v.base_version} 可能不兼容。要完全回退，请恢复升级前的数据库备份（数据目录的 backups/）。`
        : `删除在线升级的 ${v.version}，重启后运行${baseLabel.value.noun}里的 ${v.base_version}。重启时服务中断几秒。`,
      confirmText: '撤销',
      destructive: !!v.new_migrations,
    })
    if (!ok) return
  }
  acting.value = true
  try {
    apply(await api.post<SystemView>('/api/admin/system/revert'))
    if (running) waitFor(v.base_version ?? '', `已回到 ${v.base_version}`)
  } catch (e) {
    toastError('撤销在线升级失败', e)
  } finally {
    acting.value = false
  }
}

// 自带的程序在不同部署方式下的叫法，以及怎样把它也换成新版本（换上后在线升级的程序自动清理）。
const baseLabel = computed(() => {
  switch (view.value?.deployment) {
    case 'docker':
      return {
        label: '镜像版本',
        noun: '镜像',
        hint: '想让镜像也是新版本：把 .env 里的 AGY2API_IMAGE 改成新版本，再执行 docker compose pull 和 docker compose up -d；或重新运行安装脚本加 --docker。',
      }
    case 'systemd':
      return { label: '安装的版本', noun: '安装的程序', hint: '想让安装的程序也是新版本：重新运行一次安装脚本。' }
    default:
      return { label: '安装的版本', noun: '安装的程序', hint: '想让安装的程序也是新版本：把程序文件换成新版本。' }
  }
})

const headline = computed(() => {
  const v = view.value
  if (!v) return ''
  if (!v.can_check) return v.unsupported_reason
  if (v.update_available) return `有新版本 ${v.latest?.version}`
  return v.checked_at && !v.check_error ? '已是最新版本' : ''
})

const progressText = computed(() => {
  const p = view.value?.progress
  if (waiting.value) return `重启中，等待 ${waiting.value} 启动`
  switch (p?.state) {
    case 'downloading':
      return `下载中 ${fmtMB(p.received ?? 0)} / ${fmtMB(p.total ?? 0)}`
    case 'verifying':
      return '校验中'
    case 'backing_up':
      return '备份数据库'
    case 'restarting':
      return `重启中，等待 ${p.target} 启动`
  }
  return ''
})
const percent = computed(() => {
  const p = view.value?.progress
  return p?.state === 'downloading' && p.total ? Math.min(100, Math.round(((p.received ?? 0) / p.total) * 100)) : 0
})

function fmtMB(n: number): string {
  return `${(n / (1 << 20)).toFixed(1)} MB`
}
function fmtDate(ms: number): string {
  return new Date(ms).toLocaleDateString()
}
</script>

<template>
  <LoadError v-if="loadError" :message="loadError" />
  <SettingCard v-else-if="view" title="版本" :description="headline" raw content-class="space-y-4 text-sm">
    <template #info>
      <p>每天查一次 GitHub Releases 上的正式版。在线升级下载当前平台的发布包，按 SHA256SUMS.txt 校验，升级前把数据库备份到数据目录的 backups/。</p>
      <p>新程序放在数据目录里，重启后生效，容器重建后也照样运行；连续 3 次起不来会自动退回原来的版本。换上同版本或更新的镜像、安装包后，在线升级的程序自动清理。</p>
      <p v-if="view.base_version">{{ baseLabel.hint }}</p>
      <p>AGY2API_UPDATE=off 可以关闭。</p>
    </template>
    <template #actions>
      <div class="flex items-center gap-1">
        <IconAction v-if="view.can_check" label="检查更新" :disabled="checking || busy" @click="check()">
          <RefreshCw :class="checking && 'animate-spin'" />
        </IconAction>
        <Button v-if="view.update_available && view.can_upgrade" size="sm" :disabled="busy || acting" @click="upgrade">
          <ArrowUpCircle />升级到 {{ view.latest?.version }}
        </Button>
        <Button v-else-if="view.update_available && view.latest?.url" size="sm" variant="outline" as-child>
          <a :href="view.latest.url" target="_blank" rel="noopener noreferrer"><Download />去下载</a>
        </Button>
      </div>
    </template>

    <Alert v-if="view.unknown_migrations?.length">
      <TriangleAlert class="text-amber-600 dark:text-amber-400" />
      <AlertTitle>数据库比当前程序新</AlertTitle>
      <AlertDescription>库被更新的版本改过结构，当前程序可能不兼容。请升级到新版本，或恢复升级前的备份。</AlertDescription>
    </Alert>
    <Alert v-if="view.failed">
      <TriangleAlert class="text-amber-600 dark:text-amber-400" />
      <AlertTitle>在线升级的 {{ view.failed.version }} 已停用</AlertTitle>
      <AlertDescription>{{ view.failed.reason }}，现在运行的是 {{ view.version }}。</AlertDescription>
      <AlertAction><Button size="xs" variant="outline" :disabled="acting || busy" @click="revert">清除</Button></AlertAction>
    </Alert>

    <dl class="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-3 gap-y-2">
      <dt class="text-muted-foreground">当前版本</dt>
      <dd>
        {{ view.version }}<span v-if="view.installed_at" class="text-muted-foreground" :title="fmtTime(view.installed_at)"
          >，{{ fmtAgo(view.installed_at) }}在线升级</span
        >
      </dd>
      <template v-if="view.base_version">
        <dt class="text-muted-foreground">{{ baseLabel.label }}</dt>
        <dd>
          {{ view.base_version
          }}<span class="text-muted-foreground">（{{ view.deployment === 'docker' ? '重建容器' : '重启' }}后仍运行 {{ view.version }}）</span>
        </dd>
      </template>
      <template v-if="view.can_check">
        <dt class="text-muted-foreground">最新版本</dt>
        <dd v-if="view.latest">
          {{ view.latest.version }}<span v-if="view.latest.published_at" class="text-muted-foreground">，{{ fmtDate(view.latest.published_at) }} 发布</span>
          <a v-if="view.latest.url" :href="view.latest.url" target="_blank" rel="noopener noreferrer" class="ml-2 whitespace-nowrap text-muted-foreground underline-offset-4 hover:underline"
            >发布页</a
          >
        </dd>
        <dd v-else class="text-muted-foreground">—</dd>
        <dt class="text-muted-foreground">上次检查</dt>
        <dd v-if="view.check_error" class="text-destructive">{{ fmtAgo(view.checked_at ?? 0) }}失败：{{ view.check_error }}</dd>
        <dd v-else-if="view.checked_at" :title="fmtTime(view.checked_at)">{{ fmtAgo(view.checked_at) }}</dd>
        <dd v-else class="text-muted-foreground">{{ checking ? '检查中…' : '还没检查过' }}</dd>
      </template>
      <template v-if="view.can_check && !view.can_upgrade">
        <dt class="text-muted-foreground">在线升级</dt>
        <dd class="text-muted-foreground">{{ view.unsupported_reason }}</dd>
      </template>
    </dl>

    <div v-if="progressText" class="space-y-2 rounded-lg border p-3">
      <div class="flex items-center gap-2"><LoaderCircle class="size-4 animate-spin text-muted-foreground" />{{ progressText }}</div>
      <div v-if="view.progress.state === 'downloading'" class="h-1.5 overflow-hidden rounded-full bg-muted">
        <div class="h-full rounded-full bg-primary transition-[width]" :style="{ width: `${percent}%` }" />
      </div>
    </div>
    <p v-else-if="view.progress.state === 'failed'" class="text-destructive">升级到 {{ view.progress.target }} 失败：{{ view.progress.error }}</p>

    <div v-if="view.update_available && view.latest?.notes" class="space-y-2">
      <div class="font-semibold">{{ view.latest.version }} 更新内容</div>
      <div class="max-h-80 overflow-y-auto rounded-lg border p-3"><ReleaseNotes :notes="view.latest.notes" /></div>
    </div>

    <template v-if="view.installed_at" #footer>
      <CardFooter class="border-t">
        <Button
          variant="outline"
          class="hover:bg-amber-500/10 hover:text-amber-600 dark:hover:bg-amber-500/20 dark:hover:text-amber-400"
          :disabled="acting || busy"
          @click="revert"
        >
          <Undo2 />撤销在线升级
        </Button>
      </CardFooter>
    </template>
  </SettingCard>
</template>
