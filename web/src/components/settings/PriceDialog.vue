<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, type PriceRow, type PricesView } from '@/api/client'
import { fmtRate } from '@/lib/cost'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

// 手动设置一个模型家族的价格（美元每百万 token），优先于官方价。打开时填入当前生效的标准价。
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ row: PriceRow | null }>()
const emit = defineEmits<{ saved: [view: PricesView] }>()

const input = ref('')
const cache = ref('')
const output = ref('')
const error = ref('')
const busy = ref(false)

const usd = (micro: number | undefined) => (micro == null ? '' : String(micro / 1e6))
watch(open, (o) => {
  if (!o || !props.row) return
  const p = props.row.price?.price
  input.value = usd(p?.input)
  cache.value = usd(p?.cache_read)
  output.value = usd(p?.output)
  error.value = ''
})

// parse 读一个单价：空串为 undefined，不是非负数时为 NaN。
function parse(v: string): number | undefined {
  const s = String(v).trim()
  if (s === '') return undefined
  const n = Number(s)
  return Number.isFinite(n) && n >= 0 ? n : NaN
}

async function save() {
  if (!props.row) return
  const i = parse(input.value)
  const o = parse(output.value)
  const c = parse(cache.value)
  if (i === undefined || o === undefined || Number.isNaN(i) || Number.isNaN(o) || Number.isNaN(c)) {
    error.value = '输入和输出要填，单价是不小于 0 的数'
    return
  }
  if (i === 0 && o === 0) {
    error.value = '输入和输出不能都是 0'
    return
  }
  busy.value = true
  error.value = ''
  try {
    const view = await api.put<PricesView>(`/api/admin/prices/${encodeURIComponent(props.row.model)}`, {
      input: i,
      output: o,
      cache_read: c ?? null,
    })
    emit('saved', view)
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
        <DialogTitle class="wrap-anywhere">设置 {{ row?.model }} 的价格</DialogTitle>
        <DialogDescription>
          单价为美元每百万 token，优先于官方价，各档位同价。保存后，还没算费用的调用记录按这个价格补算，已算过的不变。
        </DialogDescription>
      </DialogHeader>
      <form class="grid gap-4" @submit.prevent="save">
        <div class="grid grid-cols-3 gap-3">
          <div class="grid gap-2">
            <Label for="price-input">输入</Label>
            <Input id="price-input" v-model="input" inputmode="decimal" autocomplete="off" placeholder="$" />
          </div>
          <div class="grid gap-2">
            <Label for="price-cache">缓存读取</Label>
            <Input id="price-cache" v-model="cache" inputmode="decimal" autocomplete="off" placeholder="同输入" />
          </div>
          <div class="grid gap-2">
            <Label for="price-output">输出</Label>
            <Input id="price-output" v-model="output" inputmode="decimal" autocomplete="off" placeholder="$" />
          </div>
        </div>
        <p class="text-xs text-muted-foreground">
          缓存读取留空时按输入价算；推理 Token 按输出价算。
          <template v-if="row?.official">
            官方价：输入 {{ fmtRate(row.official.price.input) }}，缓存读取 {{ fmtRate(row.official.price.cache_read ?? row.official.price.input) }}，输出
            {{ fmtRate(row.official.price.output) }}。
          </template>
        </p>
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
