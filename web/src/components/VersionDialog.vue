<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowUpCircle, Check, Download, ExternalLink, Info, LoaderCircle, RefreshCw, TriangleAlert, Undo2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { api, errorMessage, fetchSession, type SystemView, type UpgradeState } from '@/api/client'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { AUTO_CHECK_INTERVAL, checkUpdate, setUpdate, versionDialogOpen } from '@/lib/version'
import { fmtAgo, fmtTime } from '@/lib/utils'
import { Alert, AlertAction, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import BaseDialog from './BaseDialog.vue'
import ReleaseNotes from './ReleaseNotes.vue'

// 版本弹窗：当前版本、GitHub 上最新的正式版和更新内容、一键在线升级和撤销。App.vue 里挂一次，点侧边栏的版本号、
// 手机顶栏的升级图标、设置页底部的版本号打开（lib/version.ts）。升级在服务端后台进行，这里轮询进度；关掉弹窗也继续，
// 服务重启后轮询到新版本就刷新页面（前端打包在程序里，刷新才是新界面）。
const confirm = useConfirm()
const view = ref<SystemView | null>(null)
const loadError = ref('')
const checking = ref(false)
const acting = ref(false)
// waiting 是重启后等待的版本，非空时在轮询新进程。
const waiting = ref('')
let timer: ReturnType<typeof setTimeout> | undefined

const RESTART_TIMEOUT = 90_000
const working: (UpgradeState | undefined)[] = ['downloading', 'verifying', 'backing_up']
const busy = computed(() => !!waiting.value || working.includes(view.value?.progress.state) || view.value?.progress.state === 'restarting')

function apply(v: SystemView) {
  view.value = v
  setUpdate(v.update_available ? v.latest?.version : undefined)
}

// 每次打开都重新读一次；升级进行中时接着轮询，距上次检查超过自动检查的间隔就查一次。
watch(versionDialogOpen, async (open) => {
  if (!open || busy.value) return
  loadError.value = ''
  try {
    apply(await api.get<SystemView>('/api/admin/system'))
  } catch (e) {
    loadError.value = errorMessage(e)
    return
  }
  const v = view.value!
  if (working.includes(v.progress.state)) poll()
  else if (v.progress.state === 'restarting') waitFor(v.progress.target ?? '', `已运行 ${v.progress.target}`)
  else if (v.can_check && (!v.checked_at || Date.now() - v.checked_at > AUTO_CHECK_INTERVAL)) check(false)
})

async function check(manual = true) {
  checking.value = true
  try {
    const v = await checkUpdate(!manual)
    apply(v)
    if (manual && v.check_error) toastError('检查新版本失败', new Error(v.check_error))
  } catch (e) {
    if (manual) toastError('检查新版本失败', e)
  } finally {
    checking.value = false
  }
}

// poll 轮询升级进度，到重启阶段转去等新版本。服务原地重启很快（没有进行中的请求时不到 1 秒），两次轮询之间可能
// 已经重启完：这时读到的是新进程，进度已清空，也按重启处理，等版本对上再刷新页面。
function poll() {
  clearTimeout(timer)
  timer = setTimeout(async () => {
    const target = view.value?.progress.target ?? ''
    try {
      const v = await api.get<SystemView>('/api/admin/system')
      apply(v)
      const p = v.progress
      if (working.includes(p.state)) poll()
      else if (p.state === 'failed') toastError(`升级到 ${target} 失败`, new Error(p.error))
      else waitFor(target, `已升级到 ${target}`) // 重启中，或已经重启完（进度清空）
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
        : `删除在线升级的 ${v.version}，重启后运行${base.value.noun}里的 ${v.base_version}。重启时服务中断几秒。`,
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
const base = computed(() => {
  switch (view.value?.deployment) {
    case 'docker':
      return {
        short: '镜像',
        noun: '镜像',
        hint: '想让镜像也是新版本：把 .env 里的 AGY2API_IMAGE 改成新版本，再执行 docker compose pull 和 docker compose up -d；或重新运行安装脚本加 --docker。',
      }
    case 'systemd':
      return { short: '原程序', noun: '安装的程序', hint: '想让安装的程序也是新版本：重新运行一次安装脚本。' }
    default:
      return { short: '原程序', noun: '安装的程序', hint: '想让安装的程序也是新版本：把程序文件换成新版本。' }
  }
})
const deployments = { docker: 'Docker', systemd: 'systemd 服务', binary: '直接运行' }

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
  <!-- 状态都在正文的两格里，不用副标题重复；说明只给屏幕阅读器。更新内容很长时只有正文滚动 -->
  <BaseDialog v-model:open="versionDialogOpen" title="版本" description="当前版本、最新版本和在线升级" description-sr-only size="xl">
    <div class="space-y-5">
      <p v-if="loadError" class="text-destructive">加载失败：{{ loadError }}</p>
      <template v-else-if="view">
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
        <Alert v-if="view.antigravity?.outdated">
          <Info class="text-amber-600 dark:text-amber-400" />
          <AlertTitle>声明的客户端版本还没跟上官方</AlertTitle>
          <AlertDescription>
            官方 Antigravity 已更新到 {{ view.antigravity.latest }}，请求对上游声明的是 {{ view.antigravity.shipped }}。构建表跟进后自动切换，通常不超过一天；设了
            AGY2API_UA_BUILDS=off 时需升级程序。
          </AlertDescription>
        </Alert>

        <!-- 两格摘要，每格固定三行（标签、版本、补充说明），高度一致 -->
        <div class="grid gap-2 sm:grid-cols-2">
          <div class="rounded-lg bg-muted/50 px-4 py-3">
            <div class="text-xs text-muted-foreground">当前版本</div>
            <div class="mt-1 text-base font-semibold">{{ view.version }}</div>
            <div v-if="view.base_version" class="mt-0.5 flex items-center gap-1 text-xs text-muted-foreground">
              <span :title="view.installed_at ? `${fmtTime(view.installed_at)} 在线升级` : undefined">在线升级 · {{ base.short }} {{ view.base_version }}</span>
              <Popover>
                <PopoverTrigger as-child>
                  <button
                    type="button"
                    aria-label="在线升级：说明"
                    class="inline-flex size-4 items-center justify-center rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
                  >
                    <Info class="size-3.5" />
                  </button>
                </PopoverTrigger>
                <PopoverContent align="start" class="w-72 space-y-2 text-sm leading-relaxed">
                  <p>
                    {{ view.installed_at ? fmtAgo(view.installed_at) : '' }}在线升级。新程序放在数据目录里，{{ view.deployment === 'docker' ? '重建容器' : '重启' }}后仍运行
                    {{ view.version }}。
                  </p>
                  <p>{{ base.hint }}换上后在线升级的程序自动清理。</p>
                </PopoverContent>
              </Popover>
            </div>
            <div v-else class="mt-0.5 text-xs text-muted-foreground">{{ deployments[view.deployment] }}</div>
          </div>

          <div class="rounded-lg bg-muted/50 px-4 py-3">
            <template v-if="!view.can_check">
              <div class="text-xs text-muted-foreground">在线升级</div>
              <div class="mt-1 text-base font-semibold">不可用</div>
              <div class="mt-0.5 text-xs text-muted-foreground">{{ view.unsupported_reason }}</div>
            </template>
            <template v-else-if="view.update_available && view.latest">
              <div class="text-xs text-muted-foreground">新版本</div>
              <div class="mt-1 text-base font-semibold text-emerald-700 dark:text-emerald-400">{{ view.latest.version }}</div>
              <div class="mt-0.5 flex items-center gap-1 text-xs text-muted-foreground">
                <span v-if="view.latest.published_at">{{ fmtDate(view.latest.published_at) }} 发布</span>
                <template v-if="view.latest.url">
                  <span v-if="view.latest.published_at">·</span>
                  <a :href="view.latest.url" target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-0.5 hover:text-foreground"
                    >发布页<ExternalLink class="size-3"
                  /></a>
                </template>
              </div>
            </template>
            <template v-else>
              <div class="text-xs text-muted-foreground">最新版本</div>
              <div v-if="view.checked_at && !view.check_error" class="mt-1 flex items-center gap-1 text-base font-semibold">
                已是最新<Check class="size-4 text-emerald-600 dark:text-emerald-400" />
              </div>
              <div v-else class="mt-1 text-base font-semibold">{{ view.latest?.version ?? '—' }}</div>
              <div v-if="view.check_error" class="mt-0.5 text-xs text-destructive">检查失败：{{ view.check_error }}</div>
              <div v-else class="mt-0.5 text-xs text-muted-foreground" title="每天和打开管理端时自动检查">
                {{ checking ? '检查中…' : view.checked_at ? `${fmtAgo(view.checked_at)}检查` : '还没检查' }}
              </div>
            </template>
          </div>
        </div>

        <p v-if="view.can_check && !view.can_upgrade" class="text-muted-foreground">{{ view.unsupported_reason }}</p>

        <div v-if="progressText" class="space-y-2 rounded-lg border px-4 py-3">
          <div class="flex items-center gap-2"><LoaderCircle class="size-4 animate-spin text-muted-foreground" />{{ progressText }}</div>
          <div v-if="view.progress.state === 'downloading'" class="h-1.5 overflow-hidden rounded-full bg-muted">
            <div class="h-full rounded-full bg-primary transition-[width]" :style="{ width: `${percent}%` }" />
          </div>
        </div>
        <p v-else-if="view.progress.state === 'failed'" class="text-destructive">升级到 {{ view.progress.target }} 失败：{{ view.progress.error }}</p>

        <section v-if="view.update_available && view.latest?.notes">
          <h3 class="mb-2 font-semibold">更新内容</h3>
          <ReleaseNotes :notes="view.latest.notes" />
        </section>
      </template>
    </div>

    <template v-if="view" #footer>
      <Button
        v-if="view.installed_at"
        variant="outline"
        class="hover:bg-amber-500/10 hover:text-amber-600 sm:mr-auto dark:hover:bg-amber-500/20 dark:hover:text-amber-400"
        :disabled="acting || busy"
        @click="revert"
      >
        <Undo2 />撤销在线升级
      </Button>
      <Button v-if="view.can_check" variant="outline" :disabled="checking || busy" @click="check()">
        <RefreshCw :class="checking && 'animate-spin'" />检查更新
      </Button>
      <Button v-if="view.update_available && view.can_upgrade" :disabled="busy || acting" @click="upgrade">
        <ArrowUpCircle />升级到 {{ view.latest?.version }}
      </Button>
      <Button v-else-if="view.update_available && view.latest?.url" as-child>
        <a :href="view.latest.url" target="_blank" rel="noopener noreferrer"><Download />去下载</a>
      </Button>
    </template>
  </BaseDialog>
</template>
