<script setup lang="ts">
import { ref } from 'vue'
import { CirclePause } from '@lucide/vue'
import { api, type PoolPause } from '@/api/client'
import { useConfirm } from '@/lib/confirm'
import { toastError } from '@/lib/toast'
import { fmtRemaining, fmtShortTime } from '@/lib/utils'
import { Alert, AlertAction, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

// 封禁后全池暂停的提示（概览、账号页顶部，数据来自实时接口）：到几点恢复、哪个账号被封触发的，可以立即恢复。
// 琥珀色浅底，和设置页的未保存提示一样表示"需要注意"。
const props = defineProps<{ pause: PoolPause }>()
const emit = defineEmits<{ resumed: [] }>()
const confirm = useConfirm()
const busy = ref(false)

async function resume() {
  const ok = await confirm({
    title: '立即恢复号池？',
    description: '恢复后请求照常选号；被封的账号仍保持停用。',
    confirmText: '恢复',
  })
  if (!ok) return
  busy.value = true
  try {
    await api.post('/api/admin/pool-pause/resume')
    emit('resumed')
  } catch (e) {
    toastError('恢复号池失败', e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <!-- 内容区窄时按钮放在文字下面，宽时放在右上角（Alert 默认给按钮留的 pr-18 不够放"立即恢复"） -->
  <Alert
    class="border-amber-300 bg-amber-50 text-amber-950 has-data-[slot=alert-action]:pr-4 @xl/main:has-data-[slot=alert-action]:pr-32 dark:border-amber-500/30 dark:bg-[color-mix(in_oklab,var(--color-amber-500)_12%,var(--background))] dark:text-amber-50"
  >
    <CirclePause class="text-amber-600 dark:text-amber-400" />
    <AlertTitle>号池暂停中，{{ fmtShortTime(props.pause.until) }} 恢复（还剩 {{ fmtRemaining(props.pause.until) }}）</AlertTitle>
    <AlertDescription class="text-amber-900/80 dark:text-amber-50/80">
      账号 {{ props.pause.account || `#${props.pause.account_id}` }} 被条款封禁，暂停期间所有请求返回 503。
    </AlertDescription>
    <AlertAction class="static col-start-2 mt-2 justify-self-start @xl/main:absolute @xl/main:mt-0">
      <Button variant="outline" size="sm" :disabled="busy" @click="resume">立即恢复</Button>
    </AlertAction>
  </Alert>
</template>
