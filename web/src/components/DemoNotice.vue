<script setup lang="ts">
import { Info } from '@lucide/vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
</script>

<!-- 演示站（vite build --mode demo）内容区顶部的提示：数据是浏览器里模拟的，改动刷新后恢复。 -->
<template>
  <Alert>
    <Info />
    <AlertDescription>
      <span>
        演示站：数据是模拟的，刷新后恢复。
        <a href="https://github.com/shiceai/agy2api" target="_blank" rel="noopener" class="underline underline-offset-4">GitHub</a>
      </span>
    </AlertDescription>
  </Alert>
</template>
