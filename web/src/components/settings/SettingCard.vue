<script setup lang="ts">
import { useSlots } from 'vue'
import { Info } from '@lucide/vue'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { FieldGroup } from '@/components/ui/field'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'

// 设置页的一张卡片。卡片头：标题、可选的一句话说明（description，最多一行）、分区操作（actions 插槽）。
// 背景和原理不直接写在页面上，放 info 插槽：标题旁出现 ⓘ，点开才看（Popover，手机上也能点）。
// 默认插槽是设置行（SettingRow），放在 FieldGroup 里、行间分隔线；raw 为真时内容原样放（表格、文本框等）。
// footer 插槽放 CardFooter。
defineProps<{ title: string; description?: string; raw?: boolean; contentClass?: string }>()
const slots = useSlots()
</script>

<template>
  <Card>
    <CardHeader>
      <CardTitle class="flex items-center gap-1">
        {{ title }}
        <Popover v-if="slots.info">
          <PopoverTrigger as-child>
            <button
              type="button"
              :aria-label="`${title}：说明`"
              class="inline-flex size-6 items-center justify-center rounded-md text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
            >
              <Info class="size-3.5" />
            </button>
          </PopoverTrigger>
          <PopoverContent align="start" class="w-80 space-y-2 text-sm leading-relaxed">
            <slot name="info" />
          </PopoverContent>
        </Popover>
      </CardTitle>
      <CardDescription v-if="description">{{ description }}</CardDescription>
      <CardAction v-if="slots.actions"><slot name="actions" /></CardAction>
    </CardHeader>
    <CardContent :class="contentClass">
      <slot v-if="raw" />
      <FieldGroup v-else class="gap-0 divide-y">
        <slot />
      </FieldGroup>
    </CardContent>
    <slot name="footer" />
  </Card>
</template>
