<script setup lang="ts">
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { confirmState, settleConfirm } from '@/lib/confirm'

// 全局确认框，在 App.vue 里挂一次，由 useConfirm() 驱动。
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
        <AlertDialogCancel @click="settleConfirm(false)">取消</AlertDialogCancel>
        <AlertDialogAction
          :variant="confirmState.options.destructive ? 'destructive' : 'default'"
          @click="settleConfirm(true)"
        >
          {{ confirmState.options.confirmText ?? '确认' }}
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
