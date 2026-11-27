<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CircleCheck, CircleX, Circle } from '@lucide/vue'
import { api, type ProxyAddResult, type ProxyScheme } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

// 添加代理：每行一个，完整地址或代理商常见的 host:port、host:port:user:pass（协议按下拉框）。
// 添加后逐行显示结果，新加的交给页面去测试。
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ added: [ids: number[]] }>()

const text = ref('')
const scheme = ref<ProxyScheme>('http')
const busy = ref(false)
const error = ref('')
const results = ref<ProxyAddResult[]>([])

watch(open, (o) => {
  if (o) {
    text.value = ''
    error.value = ''
    results.value = []
  }
})

const lineCount = computed(() => text.value.split('\n').filter((l) => l.trim()).length)
const statusText = { added: '已添加', duplicate: '已有同样的代理', invalid: '格式不对' } as const

// 每行结果的说明：有说明用说明，否则按状态给文案。写成函数而不在模板里直接按键取 statusText：
// vue-tsc 3.3 在 TypeScript 5.9 下会把模板里按键取值的普通常量误当成 ref（补 .value）而报类型错误。
function resultText(r: ProxyAddResult) {
  return r.message || statusText[r.status]
}

async function add() {
  busy.value = true
  error.value = ''
  try {
    const res = await api.post<{ results: ProxyAddResult[] }>('/api/admin/proxies', { text: text.value, scheme: scheme.value })
    results.value = res.results
    const ids = res.results.filter((r) => r.status === 'added' && r.id).map((r) => r.id as number)
    if (ids.length) emit('added', ids)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>添加代理</DialogTitle>
        <DialogDescription>每行一个。添加后自动测试出口和延迟；之后在账号页给账号绑定，或添加账号时自动分配。</DialogDescription>
      </DialogHeader>

      <form v-if="!results.length" class="grid min-w-0 gap-4" @submit.prevent="add">
        <div class="grid gap-2">
          <Label for="proxy-lines">代理</Label>
          <Textarea
            id="proxy-lines"
            v-model="text"
            rows="7"
            class="font-mono text-xs"
            autocomplete="off"
            spellcheck="false"
            placeholder="socks5h://user:pass@1.2.3.4:1080&#10;user:pass@1.2.3.4:8080&#10;1.2.3.4:8080:user:pass"
          />
          <p class="text-xs text-muted-foreground">
            支持 http、https、socks5、socks5h（两种 socks 都由代理解析域名）。不带协议的 user:pass@host:port、host:port:user:pass、
            user:pass:host:port、host:port 按下面选的协议。Shadowsocks、VMess 等协议可以在本机跑 Clash 或 sing-box，
            每个节点开一个 socks5 端口再添加。
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <Label for="proxy-scheme" class="font-normal text-muted-foreground">不带协议的行按</Label>
          <Select v-model="scheme">
            <SelectTrigger id="proxy-scheme" class="w-28"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="http">http</SelectItem>
              <SelectItem value="https">https</SelectItem>
              <SelectItem value="socks5">socks5</SelectItem>
              <SelectItem value="socks5h">socks5h</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
      </form>

      <ul v-else class="max-h-80 divide-y overflow-y-auto rounded-lg border text-sm">
        <li v-for="(r, i) in results" :key="i" class="flex items-start gap-2 px-3 py-2">
          <CircleCheck v-if="r.status === 'added'" class="mt-0.5 size-4 shrink-0 text-emerald-600" />
          <Circle v-else-if="r.status === 'duplicate'" class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          <CircleX v-else class="mt-0.5 size-4 shrink-0 text-destructive" />
          <div class="min-w-0">
            <div class="font-mono text-xs break-all">{{ r.line }}</div>
            <div class="text-xs text-muted-foreground">{{ resultText(r) }}</div>
          </div>
        </li>
      </ul>

      <DialogFooter>
        <template v-if="!results.length">
          <Button variant="outline" @click="open = false">取消</Button>
          <Button :disabled="busy || !lineCount" @click="add">添加{{ lineCount ? ` ${lineCount} 个` : '' }}</Button>
        </template>
        <Button v-else @click="open = false">完成</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
