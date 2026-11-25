<script setup lang="ts">
import { computed, nextTick, onMounted, useTemplateRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import TierSettings from '@/components/settings/TierSettings.vue'
import ModelAliasSettings from '@/components/settings/ModelAliasSettings.vue'
import SchedulingSettings from '@/components/settings/SchedulingSettings.vue'
import PriceSettings from '@/components/settings/PriceSettings.vue'
import DataSettings from '@/components/settings/DataSettings.vue'
import ClientFilterSettings from '@/components/settings/ClientFilterSettings.vue'
import SystemPromptSettings from '@/components/settings/SystemPromptSettings.vue'
import RequestHandlingSettings from '@/components/settings/RequestHandlingSettings.vue'
import { appName } from '@/lib/brand'
import { openVersionDialog, useVersion } from '@/lib/version'

// 当前标签记在地址的 tab 参数里，刷新、分享链接都停在同一个标签。
const route = useRoute()
const router = useRouter()
const tabs = ['tiers', 'aliases', 'scheduling', 'client-filter', 'system-prompt', 'request', 'prices', 'data']
const tab = computed({
  get: () => (tabs.includes(String(route.query.tab)) ? String(route.query.tab) : 'tiers'),
  set: (v: string | number) => router.replace({ query: v === 'tiers' ? {} : { tab: String(v) } }),
})

// 手机上标签栏横向滑动：打开或切到某个标签时把它滚进可见范围（链接直接打开靠后的标签时，它可能在屏幕外）。
const tabBar = useTemplateRef<HTMLElement>('tabBar')
function revealActiveTab() {
  nextTick(() => tabBar.value?.querySelector('[role="tab"][data-state="active"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' }))
}
onMounted(revealActiveTab)
watch(tab, revealActiveTab)

// 手机上没有侧边栏，版本号放在设置页最底部。
const version = useVersion()
</script>

<template>
  <!-- 设置页和其它页一样铺满内容区：设置卡片（SettingCard）够宽时排成两列（SettingGrid），大块内容占满整行；
       说明只留一句，背景原理收进卡片标题旁的 ⓘ。每个标签各自保存 -->
  <Tabs v-model="tab" class="gap-6">
    <!-- 七个标签在手机上放不下：标签栏自己横向滑动（贴到屏幕边缘、不显示滚动条），不把整页撑宽 -->
    <div ref="tabBar" class="-mx-4 scroll-px-4 overflow-x-auto px-4 [scrollbar-width:none] lg:mx-0 lg:px-0 [&::-webkit-scrollbar]:hidden">
      <TabsList>
        <TabsTrigger value="tiers">思考档位</TabsTrigger>
        <TabsTrigger value="aliases">模型别名</TabsTrigger>
        <TabsTrigger value="scheduling">账号调度</TabsTrigger>
        <TabsTrigger value="client-filter">客户端过滤</TabsTrigger>
        <TabsTrigger value="system-prompt">系统提示词</TabsTrigger>
        <TabsTrigger value="request">请求处理</TabsTrigger>
        <TabsTrigger value="prices">价格</TabsTrigger>
        <TabsTrigger value="data">数据</TabsTrigger>
      </TabsList>
    </div>
    <TabsContent value="tiers"><TierSettings /></TabsContent>
    <TabsContent value="aliases"><ModelAliasSettings /></TabsContent>
    <TabsContent value="scheduling"><SchedulingSettings /></TabsContent>
    <TabsContent value="client-filter"><ClientFilterSettings /></TabsContent>
    <TabsContent value="system-prompt"><SystemPromptSettings /></TabsContent>
    <TabsContent value="request"><RequestHandlingSettings /></TabsContent>
    <TabsContent value="prices"><PriceSettings /></TabsContent>
    <TabsContent value="data"><DataSettings /></TabsContent>
    <!-- 手机上没有侧边栏：版本号放在这里（桌面在侧边栏顶部、名称下面），点开版本弹窗 -->
    <button
      v-if="version"
      type="button"
      class="self-start rounded-sm text-left text-xs text-muted-foreground underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50 md:hidden"
      @click="openVersionDialog"
    >
      {{ appName }} {{ version.version }}<span v-if="version.update" class="text-emerald-700 dark:text-emerald-400"> · 有新版本 {{ version.update }}</span>
    </button>
  </Tabs>
</template>
