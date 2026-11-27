// 表格列设置：显示哪些列、按什么顺序，存在浏览器本地（个人显示偏好，不存服务端）。
import { ref, watch, type Ref } from 'vue'

export interface ColumnDef {
  key: string
  label: string
  // defaultVisible 为 false 的列默认隐藏，用户可在列设置里打开。
  defaultVisible: boolean
}

export interface ColumnPrefs {
  order: string[] // 全部列的顺序（含隐藏的）
  visible: string[] // 显示的列
}

export function defaultPrefs(defs: ColumnDef[]): ColumnPrefs {
  return {
    order: defs.map((d) => d.key),
    visible: defs.filter((d) => d.defaultVisible).map((d) => d.key),
  }
}

// mergePrefs 把保存的设置和当前的列定义对齐：已删除的列忽略；保存之后新增的列按默认可见性，
// 插到它在默认顺序里前一列的后面（前一列也是新列时继续往前找）。
export function mergePrefs(defs: ColumnDef[], saved: Partial<ColumnPrefs> | null): ColumnPrefs {
  if (!saved || !Array.isArray(saved.order) || !Array.isArray(saved.visible)) return defaultPrefs(defs)
  const known = new Set(defs.map((d) => d.key))
  const savedKeys = new Set(saved.order.filter((k) => known.has(k)))
  const order = saved.order.filter((k) => known.has(k))
  const visible = new Set(saved.visible.filter((k) => known.has(k)))
  defs.forEach((d, i) => {
    if (savedKeys.has(d.key)) return
    let at = 0
    for (let j = i - 1; j >= 0; j--) {
      const idx = order.indexOf(defs[j].key)
      if (idx >= 0) {
        at = idx + 1
        break
      }
    }
    order.splice(at, 0, d.key)
    if (d.defaultVisible) visible.add(d.key)
  })
  if (visible.size === 0) return defaultPrefs(defs)
  return { order, visible: order.filter((k) => visible.has(k)) }
}

// 浏览器存储可能不可用（隐私模式、被禁用），读写都不能让页面出错。
export function loadPrefs(storageKey: string, defs: ColumnDef[]): ColumnPrefs {
  try {
    const raw = localStorage.getItem(storageKey)
    return mergePrefs(defs, raw ? JSON.parse(raw) : null)
  } catch {
    return defaultPrefs(defs)
  }
}

export function savePrefs(storageKey: string, prefs: ColumnPrefs) {
  try {
    localStorage.setItem(storageKey, JSON.stringify(prefs))
  } catch {
    // 存不了就只在本次页面内生效
  }
}

// useColumnPrefs 读取一个表格的列设置，改动后自动存回浏览器本地。
export function useColumnPrefs(storageKey: string, defs: ColumnDef[]): Ref<ColumnPrefs> {
  const prefs = ref(loadPrefs(storageKey, defs))
  watch(prefs, (p) => savePrefs(storageKey, p))
  return prefs
}

// 固定在表格最右侧的操作列：列多时表格横向滚动，操作按钮始终看得到。单元格的底色要跟着行变（悬停、行内弹层打开、
// 勾选），所以表格行要加 group；底色用和行底色相同效果的不透明混色，滚动时不透出下面的列。
const stickyEdge = 'sticky right-0 bg-background shadow-[-8px_0_8px_-8px_rgb(0_0_0/0.15)]'
export const stickyActionHead = stickyEdge
export const stickyActionCell = `${stickyEdge} transition-colors group-hover:bg-[color-mix(in_oklab,var(--muted)_50%,var(--background))] group-has-aria-expanded:bg-[color-mix(in_oklab,var(--muted)_50%,var(--background))] group-data-[state=selected]:bg-muted`
