<script setup lang="ts">
import { appName } from '@/lib/brand'
import LogoMark from './LogoMark.vue'
</script>

<template>
  <div class="flex items-center gap-2">
    <LogoMark class="size-7" />
    <span class="text-sm font-semibold tracking-tight">{{ appName }}</span>
  </div>
</template>
