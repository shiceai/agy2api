<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, type Proxy, type ProxyScheme } from '@/api/client'
import { proxyAddress } from '@/lib/proxy'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

// 编辑代理：改名称；填了新地址就换地址（密码不回显，要换就整条重填），换地址后测试结果清空、绑定的账号走新地址。
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ proxy: Proxy | null }>()
const emit = defineEmits<{ saved: [list: Proxy[], addressChanged: boolean] }>()

const name = ref('')
const address = ref('')
const scheme = ref<ProxyScheme>('http')
const busy = ref(false)
const error = ref('')

watch(open, (o) => {
  if (!o || !props.proxy) return
  name.value = props.proxy.name
  address.value = ''
  scheme.value = props.proxy.scheme
  error.value = ''
})

async function save() {
  if (!props.proxy) return
  busy.value = true
  error.value = ''
  try {
    const res = await api.put<{ proxies: Proxy[] }>(`/api/admin/proxies/${props.proxy.id}`, {
      name: name.value.trim(),
      url: address.value.trim(),
      scheme: scheme.value,
    })
    emit('saved', res.proxies, !!address.value.trim())
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
        <DialogTitle>编辑代理</DialogTitle>
        <DialogDescription>换地址后会重新测试，绑定它的账号之后走新地址。</DialogDescription>
      </DialogHeader>
      <form class="grid gap-4" @submit.prevent="save">
        <div class="grid gap-2">
          <Label for="proxy-name">名称</Label>
          <Input id="proxy-name" v-model="name" maxlength="64" :placeholder="proxy?.host" />
        </div>
        <div class="grid gap-2">
          <Label for="proxy-address">地址</Label>
          <Input id="proxy-address" v-model="address" class="font-mono text-xs" autocomplete="off" spellcheck="false" placeholder="不改就留空" />
          <p class="text-xs text-muted-foreground">
            现在是 <span class="font-mono">{{ proxy ? proxyAddress(proxy) : '' }}</span>，密码不显示。要换就整条重填，不带协议的写法（如 user:pass@host:port）按下面的协议。
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <Label for="proxy-edit-scheme" class="font-normal text-muted-foreground">不带协议时按</Label>
          <Select v-model="scheme">
            <SelectTrigger id="proxy-edit-scheme" class="w-28"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="http">http</SelectItem>
              <SelectItem value="https">https</SelectItem>
              <SelectItem value="socks5">socks5</SelectItem>
              <SelectItem value="socks5h">socks5h</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
        <button type="submit" class="hidden" />
      </form>
      <DialogFooter>
        <Button variant="outline" @click="open = false">取消</Button>
        <Button :disabled="busy" @click="save">保存</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
