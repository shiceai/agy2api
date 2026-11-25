<script setup lang="ts">
import { CircleAlert } from '@lucide/vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

// 页面数据加载失败的提示，放在内容区顶部；操作结果用 Toast（lib/toast.ts）。
defineProps<{ message: string }>()
</script>

<template>
  <Alert variant="destructive">
    <CircleAlert />
    <AlertTitle>加载失败</AlertTitle>
    <AlertDescription class="wrap-anywhere">{{ message }}</AlertDescription>
  </Alert>
</template>
