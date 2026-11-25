import { createApp } from 'vue'
import '@fontsource-variable/inter'
import './style.css'
import App from './App.vue'
import { router } from './router'
import { onUnauthorized } from './api/client'

// 口令失效（任何管理接口返回 401）：跳到登录页，记下当前地址，登录后回来。已经在登录页时不动（并发的几个请求会各触发一次）。
onUnauthorized(() => {
  const cur = router.currentRoute.value
  if (cur.name !== 'login') router.replace({ name: 'login', query: { redirect: cur.fullPath } })
})

// 等首次导航完成再挂载：否则路由还停在起始位置时 App 会先按"非登录页"渲染一下侧边栏，
// 打开登录页也会去请求版本信息（没口令时得到一次多余的 401）。
const app = createApp(App).use(router)
router.isReady().then(() => app.mount('#app'))
