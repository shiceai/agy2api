import { LayoutDashboard, Users, Network, KeyRound, BarChart3, Settings } from '@lucide/vue'
import { useRouter } from 'vue-router'
import { clearAdminPassword } from '@/api/client'

// 一级导航：桌面侧边栏与移动端底部标签栏共用。
export const navItems = [
  { name: 'overview', label: '概览', to: '/', icon: LayoutDashboard },
  { name: 'accounts', label: '账号', to: '/accounts', icon: Users },
  { name: 'proxies', label: '代理', to: '/proxies', icon: Network },
  { name: 'keys', label: '密钥', to: '/keys', icon: KeyRound },
  { name: 'usage', label: '用量', to: '/usage', icon: BarChart3 },
  { name: 'settings', label: '设置', to: '/settings', icon: Settings },
]

export function useLogout() {
  const router = useRouter()
  return () => {
    clearAdminPassword()
    router.push('/login')
  }
}
