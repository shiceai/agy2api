<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Check } from '@lucide/vue'
import { api, errorMessage, type AliasTestResult, type AnthropicCompatResponse, type ModelAliasResponse, type ModelAliasSettings } from '@/api/client'
import { toastError } from '@/lib/toast'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import LoadError from '@/components/LoadError.vue'
import SettingCard from './SettingCard.vue'
import SettingGrid from './SettingGrid.vue'
import SettingRow from './SettingRow.vue'

// 设置页"模型别名"标签：客户端用的模型名（Claude Code 的官方名等）不在号池里时，按规则换成号池里的模型。
// Gemini、Anthropic、OpenAI 三个接口都生效。右边的"试一下"按已保存的规则解析。
const form = ref<ModelAliasSettings | null>(null)
const presets = ref<ModelAliasResponse['presets']>([])
const saved = ref('')
const loadError = ref('')
const saving = ref(false)
const justSaved = ref(false)

const testModel = ref('claude-haiku-5-5')
const testing = ref(false)
const result = ref<AliasTestResult | null>(null)

// Claude Code 兼容：单独的开关，切换即保存（见下方卡片）。
const systemAsUser = ref(true)
const savingCompat = ref(false)

const dirty = computed(() => !!form.value && JSON.stringify(form.value) !== saved.value)

function apply(res: ModelAliasResponse) {
  form.value = { ...res.settings }
  presets.value = res.presets ?? []
  saved.value = JSON.stringify(res.settings)
}

onMounted(async () => {
  try {
    const [aliases, compat] = await Promise.all([
      api.get<ModelAliasResponse>('/api/admin/settings/model-aliases'),
      api.get<AnthropicCompatResponse>('/api/admin/settings/anthropic-compat'),
    ])
    apply(aliases)
    systemAsUser.value = compat.settings.system_as_user
  } catch (e) {
    loadError.value = errorMessage(e)
  }
})

// 兼容开关切换即保存；失败回滚。
async function setCompat(value: boolean) {
  savingCompat.value = true
  try {
    const res = await api.put<AnthropicCompatResponse>('/api/admin/settings/anthropic-compat', { system_as_user: value })
    systemAsUser.value = res.settings.system_as_user
  } catch (e) {
    systemAsUser.value = !value
    toastError('保存 Claude Code 兼容失败', e)
  } finally {
    savingCompat.value = false
  }
}

async function save() {
  if (!form.value) return
  saving.value = true
  try {
    apply(await api.put<ModelAliasResponse>('/api/admin/settings/model-aliases', form.value))
    result.value = null // 规则变了，之前的试解析结果作废
    justSaved.value = true
    setTimeout(() => (justSaved.value = false), 1500)
  } catch (e) {
    toastError('保存模型别名失败', e)
  } finally {
    saving.value = false
  }
}

async function test() {
  const model = testModel.value.trim()
  if (!model) return
  testing.value = true
  try {
    result.value = await api.post<AliasTestResult>('/api/admin/settings/model-aliases/test', { model })
  } catch (e) {
    toastError('解析失败', e)
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <LoadError v-if="loadError" :message="loadError" />
  <div v-else-if="form" class="space-y-6">
    <SettingGrid>
      <SettingCard title="模型别名" description="客户端用的模型名不在号池里时，换成号池里的模型">
        <template #info>
          <p>先看原名在不在号池目录里（精确模型或家族名都算）；不在就去掉末尾的 [1m] 这类标记和 -20261015 这类日期再看；还不在就按规则找，自定义规则在前、预置在后，第一条匹配的生效。</p>
          <p>目标可以写精确模型或家族名，家族名按请求的思考参数选档。密钥的"允许的模型"按换过之后的模型判断；用量记录里的请求模型仍是客户端原来的名字。</p>
        </template>
        <SettingRow label="启用预置规则" description="Claude Code 等客户端用的官方模型名" input-id="alias-presets">
          <Popover>
            <PopoverTrigger as-child>
              <Button variant="link" size="xs" class="h-auto px-0 text-muted-foreground">看清单（{{ presets.length }}）</Button>
            </PopoverTrigger>
            <PopoverContent align="end" class="w-80">
              <div class="mb-2 text-sm font-semibold">预置规则</div>
              <ul class="space-y-1 text-sm">
                <li v-for="p in presets" :key="p.pattern" class="flex items-center gap-2">
                  <span>{{ p.pattern }}</span><span class="text-muted-foreground">→</span><span>{{ p.target }}</span>
                </li>
              </ul>
            </PopoverContent>
          </Popover>
          <Switch id="alias-presets" v-model="form.use_presets" />
        </SettingRow>
        <Field class="py-4 last:pb-0">
          <FieldLabel for="alias-rules">自定义规则</FieldLabel>
          <Textarea
            id="alias-rules"
            v-model="form.rules"
            rows="6"
            spellcheck="false"
            class="text-[13px]"
            placeholder="claude-*haiku* => gemini-3.5-flash-lite&#10;# 旧版本统一用新模型：&#10;gemini-2.5-pro => gemini-4-neon"
          />
          <FieldDescription>每行一条 别名 =&gt; 目标，别名可以用 * 通配，# 开头是注释；排在预置规则前面</FieldDescription>
        </Field>
      </SettingCard>

      <SettingCard title="试一下" description="看一个模型名最终会用哪个模型（按已保存的规则）">
        <form class="flex gap-2" @submit.prevent="test">
          <Input v-model="testModel" aria-label="模型名" placeholder="如 claude-sonnet-5-5-20261015" class="min-w-0 flex-1" />
          <Button type="submit" variant="outline" :disabled="testing || !testModel.trim()">解析</Button>
        </form>
        <dl v-if="result" class="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-2 pt-4 text-sm">
          <dt class="text-muted-foreground">命中规则</dt>
          <dd v-if="result.rule">{{ result.rule.pattern }} → {{ result.rule.target }}</dd>
          <dd v-else class="text-muted-foreground">没有（{{ result.target === result.model.toLowerCase() ? '原名' : '去掉后缀后' }}直接用）</dd>
          <dt class="text-muted-foreground">交给号池</dt>
          <dd class="wrap-anywhere">{{ result.target }}</dd>
          <dt class="text-muted-foreground">实际模型</dt>
          <dd v-if="result.resolved" class="wrap-anywhere">{{ result.resolved }}</dd>
          <dd v-else class="text-destructive">号池里没有这个模型</dd>
        </dl>
      </SettingCard>

      <SettingCard title="Claude Code 兼容" description="把客户端 system 降为用户消息，绕过上游对 Claude Code 的拦截">
        <template #info>
          <p>上游会识别第三方客户端写在系统提示里的内容，返回伪装成额度耗尽的 429（与模型、措辞都无关，删改也躲不掉）。检测只看系统提示、不看对话内容，所以把客户端 system 降为开头的一条用户消息即可放行。</p>
          <p>代价：客户端 system 从 system 级降到 user 级，可能略微改变模型遵循指令的方式。只作用于 Anthropic 接口，不影响 Gemini、OpenAI 接口。</p>
        </template>
        <SettingRow label="降客户端 system 为用户消息" description="关掉后 Claude Code 等直接接入会一直被上游拦截（429）" input-id="compat-sysuser">
          <Switch id="compat-sysuser" :model-value="systemAsUser" :disabled="savingCompat" @update:model-value="setCompat(Boolean($event))" />
        </SettingRow>
      </SettingCard>
    </SettingGrid>

    <div class="flex flex-wrap items-center gap-3">
      <Button :disabled="!dirty || saving" @click="save">保存</Button>
      <span v-if="justSaved" class="flex items-center gap-1 text-sm text-muted-foreground"><Check class="size-4" />已保存</span>
      <span v-else class="text-sm text-muted-foreground">保存后立即生效，不用重启。</span>
    </div>
  </div>
</template>
