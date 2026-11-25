<script setup lang="ts">
import { useRoute } from 'vue-router'
import { computed } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { LogOut } from '@lucide/vue'
import { navItems, useLogout } from '@/lib/nav'
import { selectionMode } from '@/lib/selection'
import { useTheme } from '@/lib/theme'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { Toaster } from '@/components/ui/sonner'
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import { TooltipProvider } from '@/components/ui/tooltip'
import AppSidebar from './components/AppSidebar.vue'
import MobileNav from './components/MobileNav.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import ThemeToggle from './components/ThemeToggle.vue'
import DemoNotice from './components/DemoNotice.vue'
import { isDemo } from '@/api/client'

const route = useRoute()
const isLogin = computed(() => route.name === 'login')
const pageLabel = computed(() => navItems.find((i) => i.name === route.name)?.label ?? '')
const logout = useLogout()
// Toast 跟着主题走：跟随系统时交给它自己判断。
const theme = useTheme()
const toastTheme = computed<'light' | 'dark' | 'system'>(() =>
  theme.store.value === 'auto' ? 'system' : theme.state.value === 'dark' ? 'dark' : 'light',
)
// 操作结果的 Toast：桌面在右下角；手机放顶部，避开底部标签栏。
const mobile = useMediaQuery('(max-width: 767px)')

const headerClass = computed(() =>
  cn(
    'sticky top-0 z-30 border-b pt-[env(safe-area-inset-top)] transition-colors md:static md:rounded-t-xl md:pt-0',
    selectionMode.value
      ? 'bg-muted'
      : 'bg-background/95 backdrop-blur supports-backdrop-filter:bg-background/80 md:bg-transparent md:backdrop-blur-none',
  ),
)
</script>

<template>
  <!-- 提示框只在键盘聚焦时打开：关闭弹窗、切回浏览器标签时焦点会回到按钮上，不该再冒出提示 -->
  <TooltipProvider :delay-duration="300" ignore-non-keyboard-focus>
    <RouterView v-if="isLogin" />
    <SidebarProvider v-else>
      <AppSidebar />
      <!-- 桌面端内容区是侧边栏右侧的圆角面板（inset），铺满剩余宽度 -->
      <SidebarInset class="min-w-0">
        <!-- 顶栏：页名只在这里出现一次（内容区不再放大标题），右侧是页面主操作（页面用 PageActions 传送过来）。
             桌面端左侧是侧边栏开关；移动端吸顶，最右是退出。
             页面有勾选时进入选择模式（浅灰底）：页名、页面操作、退出收起，换成页面的选择栏（SelectionBar 传送过来）。
             两个传送目标一直在 DOM 里，只切换显示。 -->
        <header :class="headerClass">
          <div class="flex h-14 items-center gap-2 px-4 md:h-12 lg:px-6">
            <div v-show="selectionMode" id="page-selection" class="flex min-w-0 flex-1 items-center" />
            <template v-if="!selectionMode">
              <SidebarTrigger class="-ml-1.5 hidden md:inline-flex" />
              <Separator orientation="vertical" class="mr-1 hidden data-[orientation=vertical]:h-4 data-[orientation=vertical]:self-center md:block" />
              <h1 class="text-base font-semibold">{{ pageLabel }}</h1>
            </template>
            <div v-show="!selectionMode" id="page-actions" class="ml-auto flex items-center gap-2" />
            <!-- 手机上主题切换在顶栏（桌面在侧边栏底部）。ThemeToggle 的根是不渲染元素的 DropdownMenu，class 要加在外层 -->
            <div v-if="!selectionMode" class="md:hidden"><ThemeToggle /></div>
            <Button v-if="!selectionMode" variant="ghost" size="icon" class="-mr-2 md:hidden" aria-label="退出登录" @click="logout">
              <LogOut />
            </Button>
          </div>
        </header>
        <!-- 移动端底部给标签栏和安全区留出空间。@container/main：页面按内容区宽度（而不是视口）排版，侧边栏收起展开都准 -->
        <div class="@container/main flex-1 px-4 py-6 pb-[calc(5.5rem+env(safe-area-inset-bottom))] md:pb-8 lg:px-6">
          <DemoNotice v-if="isDemo" class="mb-6" />
          <RouterView />
        </div>
      </SidebarInset>
      <MobileNav />
    </SidebarProvider>
    <ConfirmDialog />
    <Toaster :position="mobile ? 'top-center' : 'bottom-right'" :theme="toastTheme" />
  </TooltipProvider>
</template>
