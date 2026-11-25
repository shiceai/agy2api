<script setup lang="ts">
import { computed } from 'vue'
import type { UsageLog } from '@/api/client'
import { fmtDuration } from '@/lib/utils'

// 一条用量的耗时明细：上游自报的处理时长、请求阶段（首个输出、正文、完成）、传输阶段（等冷却、选号、取令牌、
// 响应头、首个上游帧、并发占用）。耗时悬浮和详情弹窗共用。老记录没有详情时只有首字和总耗时。
const props = defineProps<{ record: UsageLog }>()

interface Row {
  label: string
  value: string
  strong?: boolean
}
interface Section {
  title: string
  source: string
  rows: Row[]
}

const sections = computed<Section[]>(() => {
  const r = props.record
  const t = r.detail?.timings
  const out: Section[] = []
  if (t?.upstream_ms != null) {
    out.push({
      title: '上游',
      source: 'Server-Timing',
      rows: [{ label: r.stream ? '首段处理' : '处理耗时', value: fmtDuration(t.upstream_ms) }],
    })
  }
  const req: Row[] = []
  if (r.first_token_ms != null) req.push({ label: '首个输出等待', value: fmtDuration(r.first_token_ms) })
  if (r.first_token_ms != null && t?.first_text_ms != null) {
    req.push({ label: '首个输出到正文', value: fmtDuration(Math.max(0, t.first_text_ms - r.first_token_ms)) })
  }
  if (t?.first_text_ms != null) req.push({ label: '正文到完成', value: fmtDuration(Math.max(0, r.latency_ms - t.first_text_ms)) })
  req.push({ label: '总耗时', value: fmtDuration(r.latency_ms), strong: true })
  out.push({ title: '请求阶段', source: '本地', rows: req })
  if (t) {
    const tr: Row[] = []
    if (t.cooldown_wait_ms != null) tr.push({ label: '等冷却', value: fmtDuration(t.cooldown_wait_ms) })
    if (t.acquire_ms != null) tr.push({ label: '选号等待', value: fmtDuration(t.acquire_ms) })
    if (t.token_ms != null) tr.push({ label: '取令牌', value: fmtDuration(t.token_ms) })
    if (t.header_ms != null) tr.push({ label: '上游响应头', value: fmtDuration(t.header_ms) })
    if (t.first_event_ms != null) tr.push({ label: '首个上游帧', value: fmtDuration(t.first_event_ms) })
    const slot = r.detail?.slot
    if (slot) tr.push({ label: '账号并发', value: `${slot.in_use} / ${slot.limit}` })
    if (tr.length) out.push({ title: '传输阶段', source: '本地', rows: tr })
  }
  return out
})
</script>

<template>
  <div class="divide-y text-xs tabular-nums">
    <section v-for="s in sections" :key="s.title" class="py-2 first:pt-0 last:pb-0">
      <div class="mb-1.5 flex items-center justify-between gap-4">
        <span class="font-semibold">{{ s.title }}</span>
        <span class="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">{{ s.source }}</span>
      </div>
      <dl class="space-y-1">
        <div v-for="row in s.rows" :key="row.label" class="flex justify-between gap-6">
          <dt class="text-muted-foreground">{{ row.label }}</dt>
          <dd :class="row.strong && 'font-semibold'">{{ row.value }}</dd>
        </div>
      </dl>
    </section>
  </div>
</template>
