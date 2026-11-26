<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Check } from '@lucide/vue'
import { api, errorMessage, type SystemPrompt, type SystemPromptResponse } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import SegmentedControl from '@/components/SegmentedControl.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'

const router = useRouter()
const form = ref<SystemPrompt | null>(null)
const saved = ref('')
const error = ref('')
const saving = ref(false)
const justSaved = ref(false)

const modeOptions = [
  { value: 'prepend', label: '前置' },
  { value: 'replace', label: '替换' },
  { value: 'as_user', label: '转用户' },
] as const
const modeHelp: Record<string, string> = {
  prepend: '模板加在客户端 system 前面，两者都保留',
  replace: '只用模板，丢掉客户端的 system',
  as_user: '客户端 system 转成一条 user 消息；模板可留空（规避上游对客户端 system 的识别）',
}

const dirty = computed(() => !!form.value && JSON.stringify(form.value) !== saved.value)

function apply(res: SystemPromptResponse) {
  form.value = res.settings
  saved.value = JSON.stringify(res.settings)
}

function handle(e: unknown) {
  error.value = errorMessage(e)
}

onMounted(async () => {
  try {
    apply(await api.get<SystemPromptResponse>('/api/admin/settings/system-prompt'))
  } catch (e) {
    handle(e)
  }
})

async function save() {
  if (!form.value) return
  saving.value = true
  error.value = ''
  try {
    apply(await api.put<SystemPromptResponse>('/api/admin/settings/system-prompt', form.value))
    justSaved.value = true
    setTimeout(() => (justSaved.value = false), 1500)
  } catch (e) {
    handle(e)
  } finally {
    saving.value = false
  }
}
</script>

<!-- 设置页"系统提示词"标签：注入官方客户端的基础 system 提示词。模板是大块文本，两张卡片都占满整行。 -->
<template>
  <div v-if="form" class="space-y-6">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <SettingCard title="系统提示词伪装" description="让请求的 system 看起来像官方客户端发的">
      <template #info>
        <p>注入官方 Antigravity 客户端的基础 system 提示词。没有证据表明能减少 429 或封号，所以默认关闭。</p>
        <p>前置：模板在前、客户端 system 在后，两者都保留，可能相互冲突。替换：只用模板，会改变客户端行为。
          客户端转用户：模板作为 system，客户端原 system 转成开头的一条 user 消息（带桥接说明），伪装最彻底又保留客户端行为。</p>
      </template>
      <SettingRow label="启用注入" description="每个请求多几千 token，会改变模型行为" input-id="sp-enabled">
        <Switch id="sp-enabled" v-model="form.enabled" />
      </SettingRow>
      <SettingRow label="注入方式" :description="modeHelp[form.mode]">
        <SegmentedControl v-model="form.mode" :options="modeOptions" label="注入方式" hide-label :class="!form.enabled && 'pointer-events-none opacity-50'" />
      </SettingRow>
    </SettingCard>

    <SettingCard title="模板" raw :class="!form.enabled && 'opacity-60'">
      <template #info>
        <p>项目不内置官方提示词，要自己从官方客户端抓取后粘贴进来。对所有账号统一使用，与账号 UA 版本无关（相邻补丁版本的基础 system 基本一致）。前置、替换开启时模板不能为空；转用户可以留空，此时只把客户端 system 降为用户消息、不注入模板，用来规避上游对客户端 system 的识别（对 Gemini 入站的 Claude Code 等有用）。</p>
      </template>
      <Textarea
        id="sp-template"
        v-model="form.template"
        :disabled="!form.enabled"
        rows="14"
        spellcheck="false"
        aria-label="system 提示词模板"
        class="font-mono text-[13px]"
        placeholder="从官方客户端抓取的 system 提示词"
      />
    </SettingCard>

    <div class="flex flex-wrap items-center gap-3">
      <Button :disabled="!dirty || saving" @click="save">保存</Button>
      <span v-if="justSaved" class="flex items-center gap-1 text-sm text-muted-foreground"><Check class="size-4" />已保存</span>
      <span v-else class="text-sm text-muted-foreground">保存后立即生效，不用重启。</span>
    </div>
  </div>
</template>
