<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, errorMessage, type SystemPrompt, type SystemPromptResponse } from '@/api/client'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import SegmentedControl from '@/components/SegmentedControl.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'
import UnsavedBar from './UnsavedBar.vue'
import { toastError } from '@/lib/toast'

const enabled = ref(false)
const mode = ref<SystemPrompt['mode']>('as_user')
// source：内置用官方基线、自定义用下面的文本。custom 是自定义文本的缓冲，切回内置不丢。
const source = ref<'builtin' | 'custom'>('builtin')
const custom = ref('')
const builtin = ref('')
const saved = ref('')
const error = ref('')
const saving = ref(false)
const ready = ref(false)

const modeOptions = [
  { value: 'prepend', label: '前置' },
  { value: 'replace', label: '替换' },
  { value: 'as_user', label: '转用户' },
] as const
const modeHelp: Record<string, string> = {
  prepend: '模板加在客户端 system 前面，两者都保留',
  replace: '只用模板，丢掉客户端的 system',
  as_user: '模板作为 system，客户端原 system 转成一条 user 消息，伪装最彻底又保留客户端行为',
}
const sourceOptions = [
  { value: 'builtin', label: '内置' },
  { value: 'custom', label: '自定义' },
] as const

// 实际保存的设置：来源为内置时 template 留空（服务端用内置基线）。
const payload = computed<SystemPrompt>(() => ({
  enabled: enabled.value,
  mode: mode.value,
  template: source.value === 'custom' ? custom.value : '',
}))
const dirty = computed(() => ready.value && JSON.stringify(payload.value) !== saved.value)

// setForm 把表单设成 s：模板为空表示用内置。
function setForm(s: SystemPrompt) {
  enabled.value = s.enabled
  mode.value = s.mode
  if (s.template.trim()) {
    source.value = 'custom'
    custom.value = s.template
  } else {
    source.value = 'builtin'
    custom.value = ''
  }
}

function apply(res: SystemPromptResponse) {
  setForm(res.settings)
  builtin.value = res.builtin
  saved.value = JSON.stringify({ enabled: res.settings.enabled, mode: res.settings.mode, template: res.settings.template })
  ready.value = true
}

function discard() {
  setForm(JSON.parse(saved.value))
}

onMounted(async () => {
  try {
    apply(await api.get<SystemPromptResponse>('/api/admin/settings/system-prompt'))
  } catch (e) {
    error.value = errorMessage(e)
  }
})

async function save() {
  saving.value = true
  try {
    apply(await api.put<SystemPromptResponse>('/api/admin/settings/system-prompt', payload.value))
  } catch (e) {
    toastError('保存系统提示词失败', e)
  } finally {
    saving.value = false
  }
}
</script>

<!-- 设置页"系统提示词"标签：让请求的 system 看起来像官方客户端发的。模板是大块文本，卡片占满整行。 -->
<template>
  <div v-if="ready" class="space-y-6">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <SettingCard title="系统提示词伪装" description="让请求的 system 看起来像官方客户端发的">
      <template #info>
        <p>给请求注入官方 Antigravity 客户端的基础 system 提示词，减少被上游当成非官方客户端的概率。默认开启、客户端转用户、用内置官方基线。</p>
        <p>前置：模板在前、客户端 system 在后，两者都保留，可能相互冲突。替换：只用模板，会改变客户端行为。
          客户端转用户：模板作为 system，客户端原 system 转成开头的一条 user 消息（带桥接说明），伪装最彻底又保留客户端行为。</p>
      </template>
      <SettingRow label="启用注入" description="每个请求多几千 token，会改变模型行为" input-id="sp-enabled">
        <Switch id="sp-enabled" v-model="enabled" />
      </SettingRow>
      <SettingRow label="注入方式" :description="modeHelp[mode]">
        <SegmentedControl v-model="mode" :options="modeOptions" label="注入方式" hide-label :class="!enabled && 'pointer-events-none opacity-50'" />
      </SettingRow>
      <SettingRow label="提示词来源" description="内置官方基线，或自己粘贴抓取的提示词">
        <SegmentedControl v-model="source" :options="sourceOptions" label="提示词来源" hide-label :class="!enabled && 'pointer-events-none opacity-50'" />
      </SettingRow>
    </SettingCard>

    <SettingCard :title="source === 'builtin' ? '内置官方基线' : '自定义模板'" raw :class="!enabled && 'opacity-60'">
      <template #info>
        <p>对所有账号统一使用，与账号 UA 版本无关（相邻版本的基础 system 基本一致）。内置基线由官方客户端的 system 模板重构而来；要用自己抓取的版本就切到"自定义"。</p>
      </template>
      <Textarea
        v-if="source === 'builtin'"
        :model-value="builtin"
        readonly
        rows="14"
        spellcheck="false"
        aria-label="内置官方基线（只读）"
        class="font-mono text-[13px]"
      />
      <Textarea
        v-else
        v-model="custom"
        :disabled="!enabled"
        rows="14"
        spellcheck="false"
        aria-label="自定义 system 提示词模板"
        class="font-mono text-[13px]"
        placeholder="从官方客户端抓取的 system 提示词；留空则用内置基线"
      />
    </SettingCard>

    <UnsavedBar :dirty="dirty" :saving="saving" @save="save" @discard="discard" />
  </div>
</template>
