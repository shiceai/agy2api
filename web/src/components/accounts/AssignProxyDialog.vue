<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import { api, type Proxy } from '@/api/client'
import { choiceOf, freeProxies } from '@/lib/proxy'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import ProxyChoiceSelect from '@/components/proxies/ProxyChoiceSelect.vue'

// 批量给选中的账号分配代理：自动（只给没绑代理的账号各分配一个空闲代理）、直连、或全部绑到一个代理。
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ ids: number[]; proxies: Proxy[] }>()
const emit = defineEmits<{ done: [] }>()

const choice = ref('auto')
const busy = ref(false)
const error = ref('')
watch(open, (o) => {
  if (!o) return
  choice.value = freeProxies(props.proxies) ? 'auto' : 'none'
  error.value = ''
})

const hint = computed(() => {
  if (choice.value === 'auto') return '选中账号里没绑代理的，各分配一个空闲代理；已绑的不变。空闲代理不够时不做改动。'
  if (choice.value === 'none') return '选中的账号都改成直连。'
  return '选中的账号都绑到这个代理，会共用同一个出口。'
})

async function submit() {
  busy.value = true
  error.value = ''
  try {
    const res = await api.post<{ assigned: number; skipped: number }>('/api/admin/accounts/assign-proxy', {
      ids: props.ids,
      ...choiceOf(choice.value),
    })
    if (choice.value === 'auto') {
      toast.success(`已给 ${res.assigned} 个账号分配代理`, res.skipped ? { description: `${res.skipped} 个已有代理，没改` } : {})
    }
    emit('done')
    open.value = false
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>给 {{ ids.length }} 个账号分配代理</DialogTitle>
        <DialogDescription>改了之后，账号的所有请求都经新的代理出网。</DialogDescription>
      </DialogHeader>
      <div class="grid min-w-0 gap-2">
        <Label for="assign-proxy">代理</Label>
        <ProxyChoiceSelect id="assign-proxy" v-model="choice" :proxies="proxies" allow-auto />
        <p class="text-xs text-muted-foreground">{{ hint }}</p>
        <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
      </div>
      <DialogFooter>
        <Button variant="outline" @click="open = false">取消</Button>
        <Button :disabled="busy" @click="submit">确定</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
