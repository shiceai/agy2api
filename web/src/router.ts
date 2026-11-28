import { createRouter, createWebHistory } from 'vue-router'
import OverviewView from './views/OverviewView.vue'
import AccountsView from './views/AccountsView.vue'
import ProxiesView from './views/ProxiesView.vue'
import KeysView from './views/KeysView.vue'
import GuideView from './views/GuideView.vue'
import UsageView from './views/UsageView.vue'
import SettingsView from './views/SettingsView.vue'
import LoginView from './views/LoginView.vue'

// History 模式：地址是 /accounts 这样的普通路径。服务端对前端路径返回 index.html（server/web.go 的兜底）。
export const router = createRouter({
  history: createWebHistory(),
  // 点菜单换页回到顶部；浏览器前进、后退回到离开时的位置。页面数据是异步加载的，
  // 等内容把页面撑到够高再恢复，最多等 1 秒。
  scrollBehavior: (_to, _from, saved) => {
    if (!saved) return { top: 0 }
    const start = Date.now()
    return new Promise((resolve) => {
      const check = () => {
        const tall = document.documentElement.scrollHeight - window.innerHeight >= saved.top
        if (tall || Date.now() - start > 1000) resolve(saved)
        else requestAnimationFrame(check)
      }
      check()
    })
  },
  routes: [
    { path: '/', name: 'overview', component: OverviewView },
    { path: '/accounts', name: 'accounts', component: AccountsView },
    { path: '/proxies', name: 'proxies', component: ProxiesView },
    { path: '/keys', name: 'keys', component: KeysView },
    { path: '/guide', name: 'guide', component: GuideView },
    { path: '/usage', name: 'usage', component: UsageView },
    { path: '/settings', name: 'settings', component: SettingsView },
    { path: '/login', name: 'login', component: LoginView },
  ],
})
