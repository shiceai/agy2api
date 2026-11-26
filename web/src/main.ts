import { createApp } from 'vue'
import '@fontsource-variable/inter'
import './style.css'
import App from './App.vue'
import { router } from './router'
import { clearAdminPassword, fetchSession, getAdminPassword, isUnauthorized, onUnauthorized } from './api/client'
import { versionInfo } from './lib/version'

// 口令失效（任何管理接口返回 401）：跳到登录页，记下当前地址，登录后回来。已经在登录页时不动（并发的几个请求会各触发一次）。
onUnauthorized(() => {
  const cur = router.currentRoute.value
  if (cur.name !== 'login') router.replace({ name: 'login', query: { redirect: cur.fullPath } })
})

// 首次打开先查一次登录状态（顺带拿到版本号）再渲染：没有口令或口令不对直接去登录页，不先闪一下概览；口令有效时
// 版本号在第一次渲染前就有了，侧边栏的名称不会因为版本号后到而跳动。连不上服务时照常进页面，由页面显示加载失败。
// 要在 use(router) 之前注册：首次导航从那里开始。
let sessionChecked = false
router.beforeEach(async (to) => {
  if (sessionChecked) return true
  sessionChecked = true
  if (to.name === 'login') return true // 登录页自己校验口令，登录成功时记下版本号
  try {
    versionInfo.value = await fetchSession(getAdminPassword())
  } catch (e) {
    if (isUnauthorized(e)) {
      clearAdminPassword()
      return { name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
    }
  }
  return true
})

// 等首次导航完成再挂载：否则路由还停在起始位置时 App 会先按"非登录页"渲染一下侧边栏，
// 打开登录页也会去请求版本信息（没口令时得到一次多余的 401）。
const app = createApp(App).use(router)
router.isReady().then(() => app.mount('#app'))
