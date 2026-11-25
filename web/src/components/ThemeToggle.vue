<script setup lang="ts">
import { computed } from 'vue'
import { Monitor, Moon, Sun } from '@lucide/vue'
import { setTheme, themeOptions, useTheme, type ThemeChoice } from '@/lib/theme'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import IconAction from './IconAction.vue'

// 主题切换（shadcn 的 Mode Toggle）：图标按钮，点开下拉选浅色、深色、跟随系统。图标跟实际生效的主题走：
// 浅色显示太阳，深色显示月亮。placement 决定样式：sidebar 用在桌面侧边栏底部（和退出按钮一样的 IconAction，
// 悬停出说明，菜单向右弹出，收起成图标栏时也不挡）；header 用在手机顶栏（和顶栏其它按钮一样大，菜单向下弹出）。
// 悬停说明不能直接用 Tooltip 包住 DropdownMenuTrigger：reka 的弹层按最近的 Popper 根找锚点，
// 菜单会锚到 Tooltip 上、弹到屏幕外。IconAction 把 Tooltip 放在触发器里面，两者各自找对根。
const props = withDefaults(defineProps<{ placement?: 'sidebar' | 'header' }>(), { placement: 'header' })

// useColorMode 返回的是 ref，放进模板会被解包成字符串，所以把实际生效的主题（state）单独拿出来。
const theme = useTheme()
const resolved = theme.state
const choice = computed(() => theme.store.value as ThemeChoice)
const icons = { light: Sun, dark: Moon, auto: Monitor }

// 选项的选中走原生点击而不是单选组的 v-model：reka 的 select 事件不带坐标，切换动画要知道从哪里展开。
// 鼠标、触摸从点击处展开；键盘选择（Enter、空格）触发的是没有坐标的合成点击（detail 为 0），
// 改从主题按钮中心展开，菜单的 aria-labelledby 指向触发按钮。
function pick(value: ThemeChoice, e: MouseEvent) {
  const item = e.currentTarget as HTMLElement
  let origin = { x: e.clientX, y: e.clientY }
  if (e.detail === 0) {
    const id = item.closest('[role=menu]')?.getAttribute('aria-labelledby')
    const r = ((id && document.getElementById(id)) || item).getBoundingClientRect()
    origin = { x: r.left + r.width / 2, y: r.top + r.height / 2 }
  }
  setTheme(value, origin)
}
const label = computed(() => `主题：${themeOptions.find((o) => o.value === choice.value)?.label ?? ''}`)
const sidebar = computed(() => props.placement === 'sidebar')
</script>

<template>
  <DropdownMenu>
    <DropdownMenuTrigger as-child>
      <IconAction v-if="sidebar" :label="label">
        <Moon v-if="resolved === 'dark'" />
        <Sun v-else />
      </IconAction>
      <Button v-else variant="ghost" size="icon" :aria-label="label">
        <Moon v-if="resolved === 'dark'" />
        <Sun v-else />
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent :side="sidebar ? 'right' : 'bottom'" align="end" class="min-w-36">
      <DropdownMenuRadioGroup :model-value="choice">
        <DropdownMenuRadioItem v-for="o in themeOptions" :key="o.value" :value="o.value" @click="pick(o.value, $event)">
          <component :is="icons[o.value]" />
          {{ o.label }}
        </DropdownMenuRadioItem>
      </DropdownMenuRadioGroup>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
