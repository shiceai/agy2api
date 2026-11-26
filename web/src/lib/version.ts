import { onMounted, ref } from 'vue'
import { useEventListener } from '@vueuse/core'
import { api, fetchSession, type Session, type SystemView } from '@/api/client'

// 版本信息：整个页面只取一次（侧边栏、手机顶栏、设置页共用），没取到时为 null。由侧边栏挂载时触发读取
// （手机上侧边栏组件也会挂载），只读它的地方（手机顶栏）直接用 versionInfo，不另外发请求。
export const versionInfo = ref<Session | null>(null)
let loading = false

export function useVersion() {
  if (!versionInfo.value && !loading) {
    loading = true
    fetchSession()
      .then((s) => (versionInfo.value = s))
      .catch(() => {}) // 取不到只是不显示版本；口令失效由 API 客户端统一跳登录页
      .finally(() => (loading = false))
  }
  return versionInfo
}

// setUpdate 在版本弹窗里查过新版本后更新侧边栏、顶栏的提示。
export function setUpdate(update: string | undefined) {
  if (versionInfo.value) versionInfo.value = { ...versionInfo.value, update }
}

// 自动检查新版本的间隔，和服务端 auto=1 的间隔一致：服务端这段时间内查过就直接返回上次的结果，不连 GitHub。
export const AUTO_CHECK_INTERVAL = 10 * 60_000
let lastAutoCheck = 0

// checkUpdate 让服务端查一次新版本（auto 为 true 时按 AUTO_CHECK_INTERVAL 限流），并更新侧边栏、顶栏的提示。
export async function checkUpdate(auto: boolean): Promise<SystemView> {
  const v = await api.post<SystemView>(auto ? '/api/admin/system/check?auto=1' : '/api/admin/system/check')
  setUpdate(v.update_available ? v.latest?.version : undefined)
  return v
}

// useAutoUpdateCheck 在打开页面和页面从后台切回前台时查一次新版本（每个页面最多 AUTO_CHECK_INTERVAL 一次），
// 服务端每天的后台检查之外，新版本发布后回到管理端就能看到提示。由侧边栏调用：只在登录后的页面里生效，
// 离开（退出到登录页）时移除监听。失败不提示；开发构建、关了在线升级时服务端直接返回不能查。
export function useAutoUpdateCheck() {
  const run = () => {
    if (document.visibilityState !== 'visible' || Date.now() - lastAutoCheck < AUTO_CHECK_INTERVAL) return
    lastAutoCheck = Date.now()
    checkUpdate(true).catch(() => {})
  }
  onMounted(run)
  useEventListener(document, 'visibilitychange', run)
}

// 版本弹窗（VersionDialog，App.vue 里挂一次）：点侧边栏的版本号、手机顶栏的升级图标、设置页底部的版本号打开。
export const versionDialogOpen = ref(false)
export function openVersionDialog() {
  versionDialogOpen.value = true
}
