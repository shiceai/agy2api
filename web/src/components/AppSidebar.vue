<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { ArrowUpCircle, LogOut } from '@lucide/vue'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { navItems, useLogout } from '@/lib/nav'
import { openVersionDialog, useVersion } from '@/lib/version'
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
} from '@/components/ui/sidebar'

const route = useRoute()
const logout = useLogout()
const version = useVersion()

// 菜单项层级靠颜色拉开（Windows 中文字体没有 500 字重，不能像 shadcn 那样靠加粗区分选中）：
// 平时文字和图标是浅一档的灰，悬停出浅灰底、文字变深，选中是深一档的灰底；选中项悬停不变色（shadcn 的做法）。
// data-active 变体用 :where()，优先级比 hover 低，所以要单独写选中项的悬停色。
const itemClass =
  'text-sidebar-foreground/70 hover:text-sidebar-foreground data-active:bg-sidebar-border/70 data-active:text-sidebar-foreground data-active:hover:bg-sidebar-border/70'
</script>

<!-- 桌面侧边栏（md 及以上），可收起成图标栏（顶栏按钮或 Ctrl/⌘ + B）；移动端改用顶栏 + 底部标签栏。 -->
<template>
  <Sidebar variant="inset" collapsible="icon">
    <SidebarHeader>
      <!-- 顶部：标志、名称和下面一行版本号（shadcn sidebar-07 的布局），只显示、不可点；版本号是按钮，点开版本弹窗。
           尺寸照 SidebarMenuButton size="lg"：48px 高、8px 内边距，收起成图标栏时只剩 32px 的标志，和下面的图标对齐。 -->
      <div class="flex h-12 items-center gap-2 p-2 transition-[width,height,padding] group-data-[collapsible=icon]:size-8 group-data-[collapsible=icon]:p-0">
        <div class="relative shrink-0">
          <LogoMark class="size-8" />
          <!-- 收起时版本号放不下：有新版本就在标志右上角出一个绿点，点开版本弹窗 -->
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
        <!-- 名称 16px 粗体、版本号 12px：字号、粗细、深浅一起拉开主次。版本号平时是灰字；有新版本时是绿色小胶囊加向上箭头 -->
        <div class="grid min-w-0 flex-1 justify-items-start group-data-[collapsible=icon]:hidden">
          <span class="truncate text-base leading-5.5 font-semibold tracking-tight">agy2api</span>
          <Tooltip v-if="version">
            <TooltipTrigger as-child>
              <button
                type="button"
                :class="
                  version.update
                    ? '-mx-1.5 inline-flex max-w-full items-center gap-1 rounded-full bg-emerald-500/10 px-1.5 text-xs leading-4 text-emerald-700 outline-none hover:bg-emerald-500/20 focus-visible:ring-2 focus-visible:ring-sidebar-ring dark:text-emerald-400'
                    : 'max-w-full truncate rounded-sm text-xs leading-4 text-sidebar-foreground/50 outline-none hover:text-sidebar-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring'
                "
                @click="openVersionDialog"
              >
                <span class="truncate">{{ version.version }}</span><ArrowUpCircle v-if="version.update" class="size-3 shrink-0" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="right">{{ version.update ? `有新版本 ${version.update}` : '版本和更新' }}</TooltipContent>
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
      <!-- 主题、退出做成图标按钮排一行，悬停出说明；收起成图标栏时放不下，竖着排。图标中心和上面菜单的图标对齐。 -->
      <div class="flex items-center gap-1 group-data-[collapsible=icon]:flex-col">
        <ThemeToggle placement="sidebar" />
        <IconAction label="退出" @click="logout"><LogOut /></IconAction>
      </div>
    </SidebarFooter>
    <!-- 不放 SidebarRail：它在边缘显示左右调整光标却只能点击，和顶栏的收起按钮重复 -->
  </Sidebar>
</template>
