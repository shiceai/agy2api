import { onMounted, onUnmounted, ref } from 'vue'

// useAutoRefresh 定时刷新页面数据：挂载时取一次，之后页面可见时每 intervalMs 刷新；切到后台（隐藏）暂停，
// 回到前台立即刷新并重新计时；手动刷新也重新计时。上一次还没返回时不重复发。
// load 返回 true 表示取到了新数据，更新 updatedAt；失败由 load 自己处理（如显示错误、保留旧数据）。
export function useAutoRefresh(load: () => Promise<boolean>, intervalMs: number) {
  const refreshing = ref(false)
  const updatedAt = ref(0)
  let timer: number | undefined

  async function run() {
    if (refreshing.value) return
    refreshing.value = true
    try {
      if (await load()) updatedAt.value = Date.now()
    } finally {
      refreshing.value = false
    }
  }
  function schedule() {
    stop()
    if (!document.hidden) timer = window.setInterval(run, intervalMs)
  }
  function stop() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }
  // 手动刷新：立即取一次，并从现在起重新计时。
  function refresh() {
    schedule()
    return run()
  }
  function onVisibility() {
    if (document.hidden) stop()
    else refresh()
  }

  onMounted(() => {
    refresh()
    document.addEventListener('visibilitychange', onVisibility)
  })
  onUnmounted(() => {
    stop()
    document.removeEventListener('visibilitychange', onVisibility)
  })

  return { refreshing, updatedAt, refresh }
}
