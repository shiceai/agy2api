<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, errorMessage, type ClientFilter, type ClientFilterResponse } from '@/api/client'
import { Switch } from '@/components/ui/switch'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import SegmentedControl from '@/components/SegmentedControl.vue'
import SettingGrid from './SettingGrid.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'
import UnsavedBar from './UnsavedBar.vue'
import { toastError } from '@/lib/toast'
import { Button } from '@/components/ui/button'

const router = useRouter()
const form = ref<ClientFilter | null>(null)
const keywords = ref<string[]>([])
const saved = ref('') // 库里当前生效的设置（序列化后），判断是否有改动
const error = ref('')
const saving = ref(false)

const modeOptions = [
  { value: 'rewrite', label: '替换' },
  { value: 'block', label: '拦截' },
] as const

const dirty = computed(() => !!form.value && JSON.stringify(form.value) !== saved.value)

function apply(res: ClientFilterResponse) {
  form.value = res.settings
  keywords.value = res.default_keywords ?? []
  saved.value = JSON.stringify(res.settings)
}

function handle(e: unknown) {
  error.value = errorMessage(e)
}

onMounted(async () => {
  try {
    apply(await api.get<ClientFilterResponse>('/api/admin/settings/client-filter'))
  } catch (e) {
    handle(e)
  }
})

async function save() {
  if (!form.value) return
  saving.value = true
  try {
    apply(await api.put<ClientFilterResponse>('/api/admin/settings/client-filter', form.value))
  } catch (e) {
    toastError('保存客户端过滤失败', e)
  } finally {
    saving.value = false
  }
}

function discard() {
  form.value = JSON.parse(saved.value)
}
</script>

<!-- 设置页"客户端过滤"标签：替换或拦截 system 提示词里的第三方客户端名称。 -->
<template>
  <div v-if="form" class="space-y-6">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <SettingGrid>
      <SettingCard title="客户端名称过滤" description="替换或拦截 system 里的第三方客户端名">
        <template #info>
          <p>第三方编码客户端（Cline、Roo Code、Claude Code 等）常把自己的名字写进 system 提示词，在伪装成官方客户端的请求里可能成为识别信号。</p>
          <p>只改 system 提示词，不动用户消息。没有证据表明过滤能减少风控，所以默认关闭。</p>
        </template>
        <SettingRow label="启用过滤" description="改写 system 可能影响客户端行为" input-id="cf-enabled">
          <Switch id="cf-enabled" v-model="form.enabled" />
        </SettingRow>
        <SettingRow label="命中后" description="替换成 Antigravity，或直接拒绝（400）">
          <SegmentedControl v-model="form.mode" :options="modeOptions" label="命中后" hide-label :class="!form.enabled && 'pointer-events-none opacity-50'" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="规则" :class="!form.enabled && 'opacity-60'">
        <template #info>
          <p>不区分大小写，长名优先匹配（如 Codex CLI 先于 Codex）。自定义规则覆盖同名的预置规则，最多 200 条。</p>
        </template>
        <SettingRow label="启用预置规则" input-id="cf-defaults">
          <Popover>
            <PopoverTrigger as-child>
              <Button variant="link" size="xs" class="h-auto px-0 text-muted-foreground">看清单（{{ keywords.length }}）</Button>
            </PopoverTrigger>
            <PopoverContent align="end" class="w-80">
              <div class="mb-2 text-sm font-semibold">预置客户端名称</div>
              <p class="mb-2 text-xs text-muted-foreground">各替换为 Antigravity（拦截模式下命中即拒绝）。</p>
              <div class="flex flex-wrap gap-1">
                <span v-for="k in keywords" :key="k" class="rounded bg-muted px-1.5 py-0.5 text-xs">{{ k }}</span>
              </div>
            </PopoverContent>
          </Popover>
          <Switch id="cf-defaults" v-model="form.use_default_keywords" :disabled="!form.enabled" />
        </SettingRow>
        <Field class="py-4 last:pb-0">
          <FieldLabel for="cf-rules">自定义规则</FieldLabel>
          <Textarea
            id="cf-rules"
            v-model="form.rules"
            :disabled="!form.enabled"
            rows="6"
            spellcheck="false"
            class="font-mono text-[13px]"
            placeholder="MyClient=>Antigravity&#10;# 删除这个词：&#10;InternalTool"
          />
          <FieldDescription>
            每行一条 <code class="rounded bg-muted px-1 font-mono text-xs">查找词=&gt;替换词</code>，省略
            <code class="rounded bg-muted px-1 font-mono text-xs">=&gt;</code> 表示删除，<code class="rounded bg-muted px-1 font-mono text-xs">#</code> 开头是注释
          </FieldDescription>
        </Field>
      </SettingCard>
    </SettingGrid>

    <UnsavedBar :dirty="dirty" :saving="saving" @save="save" @discard="discard" />
  </div>
</template>
