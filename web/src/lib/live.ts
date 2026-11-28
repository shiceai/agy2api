import { onMounted, onUnmounted, ref } from 'vue'
import { api, type Live, type PoolPause, type WarmupState } from '@/api/client'

// useLive 轮询实时并发（/api/admin/live）：挂载时取一次，之后页面可见时每 intervalMs 刷新，
// 页面隐藏（切后台/锁屏）时暂停、回到前台立即刷新，组件卸载时清理。接口很轻（只读号池内存计数）。
// 拉取失败静默忽略（下次轮询再试），不打断页面；首屏数据由各页自己的接口（账号列表、概览）带着来。
export function useLive(intervalMs = 3000) {
  // inFlight 的键是账号 ID 的字符串形式（JSON 对象键），用 id 查时转成字符串。
  const inFlight = ref<Record<string, number>>({})
  const total = ref(0)
  // warmup 是还在新号预热的账号（键同 inFlight），不在表里的账号没在预热。
  const warmup = ref<Record<string, WarmupState>>({})
  // poolPause 是生效中的封禁后全池暂停，没有时为 null。
  const poolPause = ref<PoolPause | null>(null)
  // loaded 为真表示至少成功取过一次：此后 inFlight 没有的账号就是 0（占用为 0 的不在表里），
  // 在此之前各页回退到自己接口里带的快照值，避免把"此刻 0 并发"误显示成旧值。
  const loaded = ref(false)
  let timer: number | undefined

  async function refresh() {
    try {
      const d = await api.get<Live>('/api/admin/live')
      inFlight.value = d.in_flight ?? {}
      total.value = d.total ?? 0
      warmup.value = d.warmup ?? {}
      poolPause.value = d.pool_pause ?? null
      loaded.value = true
    } catch {
      // 实时数据拉取失败不打断页面，下次轮询再试。
    }
  }
  function start() {
    stop()
    if (!document.hidden) timer = window.setInterval(refresh, intervalMs)
  }
  function stop() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }
  function onVisibility() {
    if (document.hidden) {
      stop()
    } else {
      refresh()
      start()
    }
  }

  onMounted(() => {
    refresh()
    start()
    document.addEventListener('visibilitychange', onVisibility)
  })
  onUnmounted(() => {
    stop()
    document.removeEventListener('visibilitychange', onVisibility)
  })

  return { inFlight, total, warmup, poolPause, loaded, refresh }
}
