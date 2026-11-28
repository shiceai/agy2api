<script setup lang="ts">
import { ref, watch } from 'vue'
import { useEventListener } from '@vueuse/core'
import { Check } from '@lucide/vue'
import { useUnsavedGuard } from '@/lib/unsaved'
import { Button } from '@/components/ui/button'

// 设置标签页的未保存修改栏：有改动时停靠在内容面板底部（App.vue 的 #page-bottom，铺满宽度，页面长时贴在可视区底部，
// 手机在底部导航栏上方），从底部滑入。反色（浅色模式深底、深色模式浅底），和白色卡片明显区分。给"放弃"和"保存"，
// Ctrl+S / ⌘S 也能保存；保存成功后显示"已保存，已生效"一会儿再收起，没改动时不显示。
// 同时登记 dirty：切换标签、离开页面前由设置页确认，关闭浏览器时触发浏览器自带的提示（见 lib/unsaved.ts）。
// problem 不为空时说明为什么不能保存（如取值越界），保存按钮禁用。
const props = defineProps<{ dirty: boolean; saving?: boolean; problem?: string }>()
const emit = defineEmits<{ save: []; discard: [] }>()

useUnsavedGuard(() => props.dirty)

// 点了保存、保存结束时已经没有改动：算保存成功，显示"已保存，已生效"。保存失败时改动还在，栏不收起（原因由调用方 Toast）。
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
  if (!props.dirty || props.saving || props.problem) return
  pending.value = true
  emit('save')
}

// Ctrl+S / ⌘S：在设置页里总是拦下浏览器的"保存网页"，有改动时保存。
useEventListener(window, 'keydown', (e: KeyboardEvent) => {
  if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey || e.key.toLowerCase() !== 's') return
  e.preventDefault()
  save()
})
</script>

<template>
  <Teleport defer to="#page-bottom">
    <Transition
      enter-active-class="transition duration-150 ease-out motion-reduce:transition-none"
      enter-from-class="translate-y-full opacity-0"
      leave-active-class="transition duration-150 ease-in motion-reduce:transition-none"
      leave-to-class="translate-y-full opacity-0"
    >
      <div
        v-if="dirty || justSaved"
        role="region"
        aria-label="未保存的修改"
        class="flex flex-wrap items-center gap-x-4 gap-y-2 bg-foreground px-4 py-3 text-background shadow-[0_-4px_16px_rgb(0_0_0/0.12)] md:rounded-b-xl lg:px-6"
      >
        <p v-if="justSaved && !dirty" class="flex flex-1 items-center gap-2 text-sm" aria-live="polite">
          <Check class="size-4 text-emerald-400 dark:text-emerald-600" />已保存，已生效
        </p>
        <template v-else>
          <p class="flex min-w-0 flex-1 items-center gap-2 text-sm">
            <span class="size-2 shrink-0 rounded-full bg-amber-400 dark:bg-amber-500" aria-hidden="true" />
            <span v-if="problem" class="text-red-300 dark:text-red-600">{{ problem }}</span>
            <span v-else>有未保存的修改</span>
          </p>
          <div class="ml-auto flex gap-2">
            <Button
              variant="ghost"
              size="sm"
              class="text-background hover:bg-background/15 hover:text-background"
              :disabled="saving"
              @click="emit('discard')"
            >
              放弃
            </Button>
            <Button size="sm" class="bg-background text-foreground hover:bg-background/90" :disabled="saving || !!problem" title="保存（Ctrl+S）" @click="save">
              {{ saving ? '保存中…' : '保存' }}
            </Button>
          </div>
        </template>
      </div>
    </Transition>
  </Teleport>
</template>
