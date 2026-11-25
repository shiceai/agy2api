<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { computed } from 'vue'
import { ArrowUp, ChevronsLeft, ChevronsRight, LogOut } from '@lucide/vue'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { navItems, useLogout } from '@/lib/nav'
import { appName } from '@/lib/brand'
import { openVersionDialog, useVersion } from '@/lib/version'
import GitHubIcon from './GitHubIcon.vue'
import IconAction from './IconAction.vue'
import LogoMark from './LogoMark.vue'
import ThemeToggle from './ThemeToggle.vue'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'

const route = useRoute()
const logout = useLogout()
const version = useVersion()
const { state, toggleSidebar } = useSidebar()
// 收起按钮的说明带上快捷键（Mac 上是 ⌘B）。
const shortcut = /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘B' : 'Ctrl+B'
const toggleLabel = computed(() => `${state.value === 'expanded' ? '收起' : '展开'}侧边栏（${shortcut}）`)

// 菜单项层级靠颜色拉开（Windows 中文字体没有 500 字重，不能像 shadcn 那样靠加粗区分选中）：
// 平时文字和图标是浅一档的灰，悬停出浅灰底、文字变深，选中是深一档的灰底；选中项悬停不变色（shadcn 的做法）。
// data-active 变体用 :where()，优先级比 hover 低，所以要单独写选中项的悬停色。
const itemClass =
  'text-sidebar-foreground/70 hover:text-sidebar-foreground data-active:bg-sidebar-border/70 data-active:text-sidebar-foreground data-active:hover:bg-sidebar-border/70'
</script>

<!-- 桌面侧边栏（md 及以上），可收起成图标栏（顶栏按钮、底部按钮或 Ctrl/⌘ + B）；移动端改用顶栏 + 底部标签栏。 -->
<template>
  <Sidebar variant="inset" collapsible="icon">
    <SidebarHeader>
      <!-- 顶部：标志，右边两行是名称和版本号徽章（参考图的布局）；只显示、不可点，版本号徽章是按钮，点开版本弹窗（lib/version.ts）。
           展开时标志 40px，和右边两行（20px + 2px + 18px）等高；收起成图标栏时缩回 32px，和下面的菜单图标对齐。 -->
      <div class="flex h-14 items-center gap-2.5 p-2 transition-[width,height,padding] group-data-[collapsible=icon]:size-8 group-data-[collapsible=icon]:p-0">
        <div class="relative shrink-0">
          <LogoMark class="size-10 transition-[width,height] group-data-[collapsible=icon]:size-8" />
          <!-- 收起成图标栏时放不下版本号：有新版本就在标志右上角出一个绿点，点开版本弹窗 -->
          <Tooltip v-if="version?.update">
            <TooltipTrigger as-child>
              <button
                type="button"
                :aria-label="`有新版本 ${version.update}`"
                class="absolute -top-1 -right-1 hidden size-3 rounded-full bg-emerald-500 ring-2 ring-sidebar outline-none group-data-[collapsible=icon]:block focus-visible:ring-sidebar-ring"
                @click="openVersionDialog"
              />
            </TooltipTrigger>
            <TooltipContent side="right">有新版本 {{ version.update }}</TooltipContent>
          </Tooltip>
        </div>
        <div class="grid min-w-0 flex-1 justify-items-start gap-0.5 group-data-[collapsible=icon]:hidden">
          <span :title="appName" class="max-w-full truncate text-base leading-5 font-semibold tracking-tight">{{ appName }}</span>
          <!-- 版本号徽章（18px 高、11px 字）：平时灰色描边，有新版本时浅绿底绿字加向上箭头。
               开发构建的版本号很长（v0.1.0-3-gabc1234-dirty），放不下时截断，悬停看全 -->
          <Tooltip v-if="version">
            <TooltipTrigger as-child>
              <button
                type="button"
                :class="[
                  'inline-flex h-4.5 max-w-full items-center gap-0.5 rounded-md border px-1 text-[11px] tabular-nums outline-none transition-colors focus-visible:ring-2 focus-visible:ring-sidebar-ring',
                  version.update
                    ? 'border-emerald-500/25 bg-emerald-500/10 text-emerald-700 hover:bg-emerald-500/20 dark:text-emerald-400'
                    : 'border-sidebar-border text-sidebar-foreground/60 hover:bg-sidebar-accent hover:text-sidebar-foreground',
                ]"
                @click="openVersionDialog"
              >
                <span class="truncate">{{ version.version }}</span><ArrowUp v-if="version.update" class="size-3 shrink-0" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="right">{{ version.update ? `有新版本 ${version.update}` : `版本 ${version.version}` }}</TooltipContent>
          </Tooltip>
        </div>
      </div>
    </SidebarHeader>
    <SidebarContent>
      <SidebarGroup>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem v-for="item in navItems" :key="item.name">
              <SidebarMenuButton as-child :is-active="route.name === item.name" :tooltip="item.label" :class="itemClass">
                <RouterLink :to="item.to" :aria-current="route.name === item.name ? 'page' : undefined">
                  <component :is="item.icon" />
                  <span>{{ item.label }}</span>
                </RouterLink>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>
    </SidebarContent>
    <SidebarFooter>
      <!-- 主题、GitHub、退出做成图标按钮排一行，收起按钮靠右（顶栏左侧也有一个，这里离侧边栏近、容易发现），悬停出说明；
           收起成图标栏时放不下，竖着排，展开按钮在最下面。收起、展开用双尖角（« »），不用带方框的侧边栏图标，免得和退出的门框图标
           混淆。图标中心和上面菜单的图标对齐。 -->
      <div class="flex items-center gap-1 group-data-[collapsible=icon]:flex-col">
        <ThemeToggle placement="sidebar" />
        <IconAction label="GitHub" as="a" href="https://github.com/shiceai/agy2api" target="_blank" rel="noopener noreferrer"><GitHubIcon class="size-3.5" /></IconAction>
        <IconAction label="退出" @click="logout"><LogOut /></IconAction>
        <IconAction :label="toggleLabel" class="ml-auto group-data-[collapsible=icon]:ml-0" @click="toggleSidebar">
          <ChevronsLeft v-if="state === 'expanded'" /><ChevronsRight v-else />
        </IconAction>
      </div>
    </SidebarFooter>
    <!-- 不放 SidebarRail：它在边缘显示左右调整光标却只能点击，和顶栏的收起按钮重复 -->
  </Sidebar>
</template>
