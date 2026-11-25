import { ref } from 'vue'
import { fetchSession, type Session } from '@/api/client'

// 版本信息：整个页面只取一次（侧边栏、设置页共用），没取到时为 null。
const info = ref<Session | null>(null)
let loading = false

export function useVersion() {
  if (!info.value && !loading) {
    loading = true
    fetchSession()
      .then((s) => (info.value = s))
      .catch(() => {}) // 取不到只是不显示版本；口令失效由 API 客户端统一跳登录页
      .finally(() => (loading = false))
  }
  return info
}
