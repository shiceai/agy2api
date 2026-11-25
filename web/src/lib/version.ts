import { ref } from 'vue'
import { fetchSession, type Session } from '@/api/client'

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

// 版本弹窗（VersionDialog，App.vue 里挂一次）：点侧边栏的版本号、手机顶栏的升级图标、设置页底部的版本号打开。
export const versionDialogOpen = ref(false)
export function openVersionDialog() {
  versionDialogOpen.value = true
}
