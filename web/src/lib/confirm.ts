import { reactive } from 'vue'

// 全局确认框：useConfirm() 返回一个 Promise，用户点确认为 true，取消或关闭为 false。
// 界面由 App.vue 里挂一次的 ConfirmDialog 渲染，代替浏览器原生 confirm()。

export interface ConfirmOptions {
  title: string
  description?: string
  confirmText?: string
  // cancelText 是取消按钮的文字，默认"取消"。
  cancelText?: string
  // destructive 为 true 时确认按钮用危险样式（删除等不可恢复操作）。
  destructive?: boolean
}

export const confirmState = reactive({
  open: false,
  options: { title: '' } as ConfirmOptions,
  resolve: null as ((ok: boolean) => void) | null,
})

export function useConfirm() {
  return (options: ConfirmOptions) =>
    new Promise<boolean>((resolve) => {
      confirmState.resolve?.(false)
      confirmState.options = options
      confirmState.resolve = resolve
      confirmState.open = true
    })
}

export function settleConfirm(ok: boolean) {
  confirmState.resolve?.(ok)
  confirmState.resolve = null
  confirmState.open = false
}
