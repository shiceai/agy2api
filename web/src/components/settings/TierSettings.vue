<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Trash2 } from '@lucide/vue'
import { api, errorMessage, type Settings, type SettingsResponse, type TierFamily } from '@/api/client'
import { cn } from '@/lib/utils'
import { CardFooter } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import IconAction from '@/components/IconAction.vue'
import SettingGrid from './SettingGrid.vue'
import SettingCard from './SettingCard.vue'
import UnsavedBar from './UnsavedBar.vue'
import { toastError } from '@/lib/toast'
import { Button } from '@/components/ui/button'

interface Row {
  family: string
  tier: string
}

const router = useRouter()
const tiers = ref<string[]>([])
const families = ref<TierFamily[]>([])
const defaultTier = ref('')
const rows = ref<Row[]>([])
const saved = ref('') // 库里当前生效的设置（序列化后），用来判断是否有改动
const error = ref('')
const saving = ref(false)

const tierHints: Record<string, string> = {
  low: '思考最少，最省额度',
  medium: '折中，默认值',
  high: '思考更充分，更费额度',
  xhigh: '最强，部分家族才有',
}

function current(): Settings {
  const modelTiers: Record<string, string> = {}
  for (const r of rows.value) if (r.family) modelTiers[r.family] = r.tier
  return { default_tier: defaultTier.value, model_tiers: modelTiers }
}
// 按键排序后序列化，避免对象键顺序不同被误判为有改动。
function serialize(s: Settings): string {
  const sorted = Object.fromEntries(Object.entries(s.model_tiers).sort(([a], [b]) => a.localeCompare(b)))
  return JSON.stringify({ default_tier: s.default_tier, model_tiers: sorted })
}
// 设置加载完（saved 不为空）才比较：加载前表单是空的，不算改动。
const dirty = computed(() => saved.value !== '' && serialize(current()) !== saved.value)

// setForm 把表单设成 s。
function setForm(s: Settings) {
  defaultTier.value = s.default_tier
  rows.value = Object.entries(s.model_tiers ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([family, tier]) => ({ family, tier }))
}

function apply(res: SettingsResponse) {
  tiers.value = res.tiers ?? []
  families.value = res.families ?? []
  setForm(res.settings)
  saved.value = serialize(current())
}

function discard() {
  setForm(JSON.parse(saved.value))
}

function familyTiers(family: string, keep?: string): string[] {
  const list = families.value.find((f) => f.family === family)?.tiers ?? []
  // 目录变了导致已保存的档位不在可选范围时，仍保留它，免得静默改掉用户的设置。
  return keep && !list.includes(keep) ? [...list, keep] : list
}
function familyOptions(index: number): string[] {
  const used = new Set(rows.value.filter((_, i) => i !== index).map((r) => r.family))
  const options = families.value.map((f) => f.family).filter((f) => !used.has(f))
  const own = rows.value[index]?.family
  return own && !options.includes(own) ? [own, ...options] : options
}
const canAdd = computed(() => families.value.some((f) => !rows.value.some((r) => r.family === f.family)))

function pickTier(family: string): string {
  const list = familyTiers(family)
  return list.includes(defaultTier.value) ? defaultTier.value : (list[0] ?? defaultTier.value)
}
function addRow() {
  const family = familyOptions(rows.value.length)[0]
  if (family) rows.value.push({ family, tier: pickTier(family) })
}
function changeFamily(row: Row, family: unknown) {
  if (typeof family !== 'string') return
  row.family = family
  if (!familyTiers(family).includes(row.tier)) row.tier = pickTier(family)
}

function handle(e: unknown) {
  error.value = errorMessage(e)
}

async function load() {
  try {
    apply(await api.get<SettingsResponse>('/api/admin/settings'))
  } catch (e) {
    handle(e)
  }
}
onMounted(load)

async function save() {
  saving.value = true
  try {
    apply(await api.put<SettingsResponse>('/api/admin/settings', current()))
  } catch (e) {
    toastError('保存思考档位失败', e)
  } finally {
    saving.value = false
  }
}
</script>

<!-- 设置页"思考档位"标签：默认档位和按家族单独设置的档位。 -->
<template>
  <div class="space-y-6">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <SettingGrid>
      <SettingCard title="默认档位" description="家族名请求没带思考参数时用" raw>
        <template #info>
          <p>
            客户端只传家族名（如 <code class="rounded bg-muted px-1 font-mono text-xs">gemini-3.8-flash</code>、<code
              class="rounded bg-muted px-1 font-mono text-xs"
              >claude-opus-5-5</code
            >）、没带 thinkingLevel / thinkingBudget，该家族也没单独设置时用这一档。
          </p>
          <p>家族没有该档时取最接近的一档，先往低找。</p>
        </template>
        <div role="radiogroup" aria-label="默认档位" class="grid grid-cols-2 gap-2">
          <button
            v-for="t in tiers"
            :key="t"
            type="button"
            role="radio"
            :aria-checked="defaultTier === t"
            :class="cn(
              'rounded-lg border p-3 text-left transition-colors outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50',
              defaultTier === t ? 'border-primary bg-muted/60' : 'hover:bg-muted/50',
            )"
            @click="defaultTier = t"
          >
            <div class="text-sm font-semibold">{{ t }}</div>
            <div class="mt-1 text-xs text-muted-foreground">{{ tierHints[t] ?? '' }}</div>
          </button>
        </div>
      </SettingCard>

      <SettingCard title="按模型设置" description="给某个家族单独指定档位" raw content-class="space-y-2">
        <template #info>
          <p>优先于默认档位。请求里带了 thinkingLevel / thinkingBudget 时仍以请求为准。</p>
        </template>
        <!-- 窄屏两个下拉上下排，加边框让一行的家族和档位成组；宽屏同一行，去掉边框。 -->
        <div v-for="(row, i) in rows" :key="i" class="flex items-center gap-2 rounded-lg border p-2 sm:border-0 sm:p-0">
          <div class="grid flex-1 grid-cols-1 gap-2 sm:grid-cols-[minmax(0,1fr)_10rem]">
            <Select :model-value="row.family" @update:model-value="changeFamily(row, $event)">
              <SelectTrigger class="w-full" aria-label="模型家族"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="f in familyOptions(i)" :key="f" :value="f">{{ f }}</SelectItem>
              </SelectContent>
            </Select>
            <Select v-model="row.tier">
              <SelectTrigger class="w-full" aria-label="档位"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="t in familyTiers(row.family, row.tier)" :key="t" :value="t">{{ t }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <IconAction label="删除" @click="rows.splice(i, 1)"><Trash2 /></IconAction>
        </div>
        <p v-if="!rows.length" class="text-sm text-muted-foreground">还没有单独设置的家族。</p>
        <template #footer>
          <CardFooter class="flex-wrap gap-3">
            <Button variant="outline" size="sm" :disabled="!canAdd" @click="addRow"><Plus />添加</Button>
            <span v-if="!families.length" class="text-xs text-muted-foreground">账号目录里还没有分档家族，导入账号后再设置。</span>
          </CardFooter>
        </template>
      </SettingCard>
    </SettingGrid>

    <UnsavedBar :dirty="dirty" :saving="saving" @save="save" @discard="discard" />
  </div>
</template>
