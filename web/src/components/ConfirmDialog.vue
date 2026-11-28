<script setup lang="ts">
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { confirmState, settleConfirm } from '@/lib/confirm'

// 全局确认框，在 App.vue 里挂一次，由 useConfirm() 驱动。
// 确认按钮不用 AlertDialogAction：它点击时先执行自带的关闭，经 update:open 按取消结束，之后的 @click 已不起作用。
// 普通按钮先按确认结束、再关掉弹窗；取消、Esc 仍经 update:open 按取消结束。
function onOpenChange(open: boolean) {
  if (!open) settleConfirm(false)
}
</script>

<template>
  <AlertDialog :open="confirmState.open" @update:open="onOpenChange">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ confirmState.options.title }}</AlertDialogTitle>
        <AlertDialogDescription v-if="confirmState.options.description">
          {{ confirmState.options.description }}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel>{{ confirmState.options.cancelText ?? '取消' }}</AlertDialogCancel>
        <Button :variant="confirmState.options.destructive ? 'destructive' : 'default'" @click="settleConfirm(true)">
          {{ confirmState.options.confirmText ?? '确认' }}
        </Button>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
