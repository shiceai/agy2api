<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { cn } from '@/lib/utils'
import { navItems } from '@/lib/nav'

const route = useRoute()
</script>

<!-- 移动端底部标签栏：一级入口常驻、单手可达；底部留出 iPhone 安全区。 -->
<template>
  <nav
    aria-label="主导航"
    class="fixed inset-x-0 bottom-0 z-40 border-t bg-background/95 pb-[env(safe-area-inset-bottom)] backdrop-blur-sm supports-backdrop-filter:bg-background/80 md:hidden"
  >
    <div class="flex h-16">
      <RouterLink
        v-for="item in navItems"
        :key="item.name"
        :to="item.to"
        :aria-current="route.name === item.name ? 'page' : undefined"
        :class="cn(
          'flex flex-1 flex-col items-center justify-center gap-1 text-xs transition-colors',
          route.name === item.name ? 'text-foreground' : 'text-muted-foreground',
        )"
      >
        <span
          :class="cn(
            'flex h-7 w-12 items-center justify-center rounded-full transition-colors',
            route.name === item.name && 'bg-secondary',
          )"
        >
          <component :is="item.icon" class="h-5 w-5" />
        </span>
        {{ item.label }}
      </RouterLink>
    </div>
  </nav>
</template>
