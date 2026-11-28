<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { api } from '@/api/client'
import { buildClients, defaultBase, guideKey, keyPlaceholder, loadBase, saveBase } from '@/lib/guide'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import CodeBlock from '@/components/CodeBlock.vue'
import SettingCard from '@/components/settings/SettingCard.vue'
import SettingRow from '@/components/settings/SettingRow.vue'

// 接入页：填好接口地址和模型后，按客户端给出可直接复制的配置（和 README 各客户端的写法一致）。
// Key 只在新建时看得到：从"密钥"页新建后点"查看接入示例"会自动填上（只在内存里），否则示例里是占位。
const route = useRoute()
const router = useRouter()

const base = ref(loadBase())
watch(base, (v) => saveBase(v))
const cleanBase = computed(() => base.value.trim().replace(/\/+$/, '') || defaultBase())

const models = ref<string[]>([])
const model = ref('gemini-3-flash')
onMounted(async () => {
  try {
    const res = await api.get<{ models: string[]; families: string[] }>('/api/admin/models')
    const list = [...new Set([...res.families, ...res.models])]
    models.value = list
    if (list.length && !list.includes(model.value)) model.value = list[0]!
  } catch {
    // 取不到模型列表时示例里保留默认模型名，不打断页面。
  }
})

const maskedKey = computed(() => (guideKey.value ? `${guideKey.value.slice(0, 8)}…${guideKey.value.slice(-4)}` : ''))
const clients = computed(() => buildClients(cleanBase.value, guideKey.value, model.value))

// 当前客户端记在地址的 client 参数里，刷新、分享链接都停在同一个标签。
const client = computed({
  get: () => (clients.value.some((c) => c.id === route.query.client) ? String(route.query.client) : clients.value[0]!.id),
  set: (v: string | number) => router.replace({ query: v === clients.value[0]!.id ? {} : { client: String(v) } }),
})
</script>

<template>
  <div class="space-y-6">
    <SettingCard title="地址和密钥" description="下面的示例随这里的设置更新">
      <template #info>
        <p>接口地址默认是打开管理端的地址。经反向代理、内网访问时客户端用的地址可能不同，改成客户端能访问到的地址；改过的地址记在当前浏览器。</p>
        <p>完整的 Key 只在新建时能看到：在"密钥"页新建后点"查看接入示例"，这里会自动填上。它只在当前页面的内存里，刷新后清除。</p>
      </template>
      <SettingRow label="接口地址" description="客户端访问网关用的地址" input-id="guide-base">
        <Input id="guide-base" v-model="base" class="w-72" autocomplete="off" spellcheck="false" />
        <Button v-if="cleanBase !== defaultBase()" variant="ghost" size="sm" @click="base = defaultBase()">恢复</Button>
      </SettingRow>
      <SettingRow label="API Key" :description="guideKey ? '已填入刚新建的 Key，刷新后清除' : `示例里的 ${keyPlaceholder} 换成你的 Key`">
        <template v-if="guideKey">
          <code class="font-mono text-xs">{{ maskedKey }}</code>
          <Button variant="ghost" size="sm" @click="guideKey = ''">清除</Button>
        </template>
        <Button v-else variant="outline" size="sm" as-child><RouterLink to="/keys">去新建</RouterLink></Button>
      </SettingRow>
      <SettingRow label="模型" description="示例里用的模型" input-id="guide-model">
        <Select v-model="model">
          <SelectTrigger id="guide-model" class="w-56"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="m in models.length ? models : [model]" :key="m" :value="m">{{ m }}</SelectItem>
          </SelectContent>
        </Select>
      </SettingRow>
    </SettingCard>

    <Tabs v-model="client" class="gap-4">
      <!-- 手机上标签栏横向滑动，不把整页撑宽 -->
      <div class="-mx-4 scroll-px-4 overflow-x-auto px-4 [scrollbar-width:none] lg:mx-0 lg:px-0 [&::-webkit-scrollbar]:hidden">
        <TabsList>
          <TabsTrigger v-for="c in clients" :key="c.id" :value="c.id">{{ c.label }}</TabsTrigger>
        </TabsList>
      </div>
      <TabsContent v-for="c in clients" :key="c.id" :value="c.id">
        <Card>
          <CardContent class="space-y-4 text-sm">
            <p>{{ c.intro }}</p>
            <ol v-if="c.steps" class="list-decimal space-y-1 pl-5">
              <li v-for="s in c.steps" :key="s">{{ s }}</li>
            </ol>
            <CodeBlock v-for="s in c.snippets" :key="s.label" :label="s.label" :code="s.code" />
            <ul v-if="c.notes.length" class="list-disc space-y-1 pl-5 text-muted-foreground">
              <li v-for="n in c.notes" :key="n">{{ n }}</li>
            </ul>
          </CardContent>
        </Card>
      </TabsContent>
    </Tabs>
  </div>
</template>
