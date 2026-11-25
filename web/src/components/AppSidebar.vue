<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { LogOut } from '@lucide/vue'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { navItems, useLogout } from '@/lib/nav'
import { useVersion } from '@/lib/version'
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
  SidebarMenuAction,
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
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton size="lg" as-child>
            <RouterLink to="/">
              <!-- 收起后按钮是 32px 见方，标志也用 32px，和下面的图标对齐。按钮把直接子元素里的 svg 统一设成 16px
                   （[&>svg]:size-4，优先级比 size-8 高），这里要用 ! 盖过去。 -->
              <LogoMark class="size-8! shrink-0" />
              <!-- 名称下面一行是版本号（shadcn sidebar-07 的写法）：正式版如 v0.1.0，开发构建带提交距离和提交号。
                   名称 16px 粗体、版本号 12px 灰字，字号、粗细、深浅一起拉开主次。收起成图标栏时只剩标志。 -->
              <div class="grid flex-1">
                <span class="truncate text-base leading-5.5 font-semibold tracking-tight">agy2api</span>
                <span v-if="version" class="truncate text-xs leading-4 text-sidebar-foreground/50">{{ version.version }}</span>
              </div>
            </RouterLink>
          </SidebarMenuButton>
          <!-- 有新版本时名称右侧出一个小圆点，点开是设置页的"版本"标签。和上面的链接是兄弟节点，不嵌套；收起成图标栏时隐藏 -->
          <Tooltip v-if="version?.update">
            <TooltipTrigger as-child>
              <SidebarMenuAction as-child>
                <RouterLink :to="{ path: '/settings', query: { tab: 'version' } }" :aria-label="`有新版本 ${version.update}`">
                  <span class="size-2 rounded-full bg-emerald-500" />
                </RouterLink>
              </SidebarMenuAction>
            </TooltipTrigger>
            <TooltipContent side="right">有新版本 {{ version.update }}</TooltipContent>
          </Tooltip>
        </SidebarMenuItem>
      </SidebarMenu>
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
