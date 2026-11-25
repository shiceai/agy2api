import { nextTick } from 'vue'
import { useColorMode } from '@vueuse/core'

// 亮暗模式：浅色、深色、跟随系统（默认），选择存在浏览器本地（localStorage 的 vueuse-color-scheme）。
// index.html 里的内联脚本在渲染前读同一个键设好 dark 类，刷新时不会先白后黑闪一下。跟随系统时系统切换实时跟上。
export type ThemeChoice = 'light' | 'dark' | 'auto'

export const themeOptions: { value: ThemeChoice; label: string }[] = [
  { value: 'light', label: '浅色' },
  { value: 'dark', label: '深色' },
  { value: 'auto', label: '跟随系统' },
]

let mode: ReturnType<typeof useColorMode> | null = null

// useTheme 返回整页共用的一个实例：store 是选择（light / dark / auto），state 是实际生效的（light / dark）。
export function useTheme() {
  mode ??= useColorMode({ emitAuto: true })
  return mode
}

// setTheme 改主题选择。实际生效的主题变了时，新主题从 origin（视口坐标，一般是点击处）以圆形展开、0.4 秒盖满页面：
// View Transition 先截下旧画面，换好主题后对新画面做 clip-path 动画。
// 看起来没变（如浅色改成跟随系统、系统也是浅色）、浏览器不支持时直接切换。不看"减少动态效果"：
// Windows 关掉"动画效果"就算开了它，很多人是为了性能关的，判断它会让这些人都看不到动画。
// 跟随系统时系统自己切换深浅色不经过这里，也是直接切换。
export function setTheme(choice: ThemeChoice, origin: { x: number; y: number }) {
  const theme = useTheme()
  const next = choice === 'auto' ? theme.system.value : choice
  // useColorMode 在 post 时机的 watch 里改 dark 类，nextTick 之后才算换好。它换类时临时关掉了 CSS 过渡，
  // 新画面里的颜色直接是终值，不会再渐变一遍。
  const apply = () => {
    theme.value = choice
    return nextTick()
  }
  if (next === theme.state.value || !('startViewTransition' in document)) {
    void apply()
    return
  }
  const { x, y } = origin
  const radius = Math.hypot(Math.max(x, innerWidth - x), Math.max(y, innerHeight - y))
  document
    .startViewTransition(apply)
    .ready.then(() => {
      document.documentElement.animate(
        { clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${radius}px at ${x}px ${y}px)`] },
        { duration: 400, easing: 'ease-out', pseudoElement: '::view-transition-new(root)' },
      )
    })
    .catch(() => {}) // 过渡被跳过（如上一次还没播完又切）时主题照样换好，只是没有动画
}
