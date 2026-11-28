<script setup lang="ts">
import { ref, watch } from 'vue'
import { Check } from '@lucide/vue'
import { useUnsavedGuard } from '@/lib/unsaved'
import { Button } from '@/components/ui/button'

// 设置标签页的未保存修改栏：有改动时贴在可视区底部（手机在底部导航栏上方），写明"有未保存的修改"，给"放弃"和"保存"；
// 保存成功后显示"已保存"一会儿再收起，没改动时不显示。放在标签页内容的最后，sticky 定位，不遮住别的内容。
// 同时登记 dirty：切换标签、离开页面前由设置页确认，关闭浏览器时触发浏览器自带的提示（见 lib/unsaved.ts）。
// problem 不为空时说明为什么不能保存（如取值越界），保存按钮禁用。
const props = defineProps<{ dirty: boolean; saving?: boolean; problem?: string }>()
const emit = defineEmits<{ save: []; discard: [] }>()

useUnsavedGuard(() => props.dirty)

// 点了保存、保存结束时已经没有改动：算保存成功，显示"已保存"。保存失败时改动还在，栏不收起（原因由调用方 Toast）。
const pending = ref(false)
const justSaved = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
watch(
  () => props.saving,
  (saving, was) => {
    if (!was || saving || !pending.value) return
    pending.value = false
    if (props.dirty) return
    justSaved.value = true
    clearTimeout(timer)
    timer = setTimeout(() => (justSaved.value = false), 1500)
  },
)
watch(
  () => props.dirty,
  (d) => d && (justSaved.value = false),
)

function save() {
  pending.value = true
  emit('save')
}
</script>

<template>
  <div
    v-if="dirty || justSaved"
    role="region"
    aria-label="未保存的修改"
    class="sticky bottom-[calc(4.75rem+env(safe-area-inset-bottom))] z-20 mt-6 flex flex-wrap items-center gap-x-4 gap-y-2 rounded-xl border bg-background/95 px-4 py-3 shadow-lg backdrop-blur-sm supports-backdrop-filter:bg-background/80 md:bottom-4"
  >
    <p v-if="justSaved && !dirty" class="flex flex-1 items-center gap-1.5 text-sm text-muted-foreground" aria-live="polite">
      <Check class="size-4 text-emerald-600 dark:text-emerald-400" />已保存
    </p>
    <template v-else>
      <p class="min-w-0 flex-1 text-sm">
        <span v-if="problem" class="text-destructive">{{ problem }}</span>
        <!-- 窄屏只留前半句，修改栏保持一行 -->
        <template v-else>有未保存的修改<span class="hidden text-muted-foreground @md/main:inline"> · 保存后立即生效</span></template>
      </p>
      <div class="ml-auto flex gap-2">
        <Button variant="outline" size="sm" :disabled="saving" @click="emit('discard')">放弃</Button>
        <Button size="sm" :disabled="saving || !!problem" @click="save">{{ saving ? '保存中…' : '保存' }}</Button>
      </div>
    </template>
  </div>
</template>
