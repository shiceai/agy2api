import { onBeforeUnmount, onMounted, toValue, type MaybeRefOrGetter } from 'vue'
import { useConfirm } from './confirm'

// 设置页的未保存修改：各标签页的修改栏（UnsavedBar）登记自己的"有没有改动"，设置页在切换标签、离开页面前问一句，
// 关闭或刷新浏览器时触发浏览器自带的离开提示。同一时间只有一个标签页挂着，登记的是当前标签页。

const forms = new Set<() => boolean>()

// hasUnsavedChanges 报告当前有没有登记的表单还没保存。
export function hasUnsavedChanges(): boolean {
  for (const dirty of forms) if (dirty()) return true
  return false
}

function onBeforeUnload(e: BeforeUnloadEvent) {
  if (!hasUnsavedChanges()) return
  e.preventDefault()
  e.returnValue = '' // 旧版浏览器要设这个才会提示
}

// useUnsavedGuard 在组件挂载期间登记一个表单的 dirty。
export function useUnsavedGuard(dirty: MaybeRefOrGetter<boolean>) {
  const check = () => toValue(dirty)
  onMounted(() => {
    if (forms.size === 0) window.addEventListener('beforeunload', onBeforeUnload)
    forms.add(check)
  })
  onBeforeUnmount(() => {
    forms.delete(check)
    if (forms.size === 0) window.removeEventListener('beforeunload', onBeforeUnload)
  })
}

// useLeaveConfirm 返回一个函数：没有未保存的修改时直接放行；有时弹确认框，选"放弃修改并离开"才放行。
export function useLeaveConfirm() {
  const confirm = useConfirm()
  return async (): Promise<boolean> => {
    if (!hasUnsavedChanges()) return true
    return confirm({
      title: '有未保存的修改',
      description: '离开后这些修改会丢失。',
      confirmText: '放弃修改并离开',
      cancelText: '留下继续编辑',
      destructive: true,
    })
  }
}
