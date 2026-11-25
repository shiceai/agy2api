import { toast, type ExternalToast } from 'vue-sonner'
import { isUnauthorized } from '@/api/client'

// 操作结果（同步、启停、删除等）用 Toast 提示，页面数据加载失败才用内容区顶部的 Alert（LoadError）。
// 失败提示停留 8 秒（成功的默认 4 秒），留时间看清原因。
// 口令失效（401）不提示：API 客户端已经在跳登录页。
export function toastError(title: string, e: unknown, options: ExternalToast = {}) {
  if (isUnauthorized(e)) return
  toast.error(title, { description: (e as Error).message, duration: 8000, ...options })
}
