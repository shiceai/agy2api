<script setup lang="ts">
import { computed, ref } from 'vue'
import { useElementSize } from '@vueuse/core'
import type { HourlyUsage } from '@/api/client'
import { fmtNum, fmtTokens } from '@/lib/utils'

// 近 24 小时每小时请求数的柱状图。每根柱子由成功（下）和失败（上）两段叠成，中间留 2px 底色缝；
// 柱宽不超过 24px，顶端 4px 圆角、底端平直。悬停或聚焦后按左右方向键查看某个小时的明细。
// 图形对读屏隐藏，数据另附一份读屏可见的表格。
const props = defineProps<{ buckets: HourlyUsage[] }>()

const box = ref<HTMLElement | null>(null)
const { width } = useElementSize(box)
const height = 200
const pad = { top: 8, right: 4, bottom: 24, left: 36 }
const plotH = height - pad.top - pad.bottom
const plotW = computed(() => Math.max(0, width.value - pad.left - pad.right))
const slot = computed(() => (props.buckets.length ? plotW.value / props.buckets.length : 0))
const barW = computed(() => Math.max(2, Math.min(24, slot.value * 0.6)))
const gap = 2

const empty = computed(() => props.buckets.every((b) => b.requests === 0))

// y 轴：步长取 1、2、5 × 10ⁿ 且至少为 1，最多 4 格，顶端是步长的整数倍。
const scale = computed(() => {
  const max = Math.max(1, ...props.buckets.map((b) => b.requests))
  const raw = max / 4
  const p = 10 ** Math.floor(Math.log10(raw))
  const step = Math.max(1, [1, 2, 5, 10].map((m) => m * p).find((s) => s >= raw) ?? 10 * p)
  const top = Math.ceil(max / step) * step
  const ticks: number[] = []
  for (let v = 0; v <= top; v += step) ticks.push(v)
  return { top, ticks }
})
const y = (v: number) => pad.top + plotH - (v / scale.value.top) * plotH
// 非零的段至少 2px，避免小数值看不见。
const segH = (v: number) => (v > 0 ? Math.max(2, (v / scale.value.top) * plotH) : 0)

// 顶端可选圆角、底端平直的柱段路径。
function colPath(x: number, top: number, w: number, h: number, round: boolean): string {
  const r = round ? Math.min(4, h, w / 2) : 0
  const bottom = top + h
  return `M${x},${bottom}V${top + r}A${r},${r} 0 0 1 ${x + r},${top}H${x + w - r}A${r},${r} 0 0 1 ${x + w},${top + r}V${bottom}Z`
}

const bars = computed(() =>
  props.buckets.map((b, i) => {
    const x = pad.left + slot.value * i + (slot.value - barW.value) / 2
    const okH = segH(b.requests - b.errors)
    const errH = segH(b.errors)
    const base = pad.top + plotH
    const errTop = base - okH - (okH && errH ? gap : 0) - errH
    return {
      ok: okH ? colPath(x, base - okH, barW.value, okH, !errH) : '',
      err: errH ? colPath(x, errTop, barW.value, errH, true) : '',
    }
  }),
)

const pad2 = (n: number) => String(n).padStart(2, '0')
// x 轴每 6 个小时标一次（按本地时间整点）。
const xLabels = computed(() =>
  props.buckets.flatMap((b, i) => {
    const h = new Date(b.start).getHours()
    return h % 6 === 0 ? [{ x: pad.left + slot.value * (i + 0.5), text: `${pad2(h)}:00` }] : []
  }),
)

function rangeLabel(i: number): string {
  const d = new Date(props.buckets[i]!.start)
  const h = d.getHours()
  const day = d.toDateString() === new Date().toDateString() ? '今天' : '昨天'
  const tail = i === props.buckets.length - 1 ? ' · 进行中' : ''
  return `${day} ${pad2(h)}:00–${pad2(h + 1)}:00${tail}`
}

// 当前查看的小时：悬停或键盘选中。
const active = ref<number | null>(null)
const current = computed(() => (active.value == null ? null : props.buckets[active.value] ?? null))
// 提示框放在柱子右侧，靠右的几个小时放左侧，避免超出图表。
const tipStyle = computed(() => {
  if (active.value == null) return {}
  const center = pad.left + slot.value * (active.value + 0.5)
  const right = active.value >= props.buckets.length * 0.6
  return right
    ? { right: `${width.value - center + barW.value / 2 + 8}px`, top: `${pad.top}px` }
    : { left: `${center + barW.value / 2 + 8}px`, top: `${pad.top}px` }
})

// 读屏播报：键盘切换小时时念出这一小时的数据。
const liveText = computed(() => {
  const b = current.value
  if (!b) return ''
  return `${rangeLabel(active.value!)}，成功 ${b.requests - b.errors}，失败 ${b.errors}，Token ${b.total_tokens}`
})

// 只有鼠标移出时收起提示；触屏点按后手指抬起也会触发离开，这时保留，点别处（失焦）再收起。
function onLeave(e: PointerEvent) {
  if (e.pointerType === 'mouse') active.value = null
}
function onFocus() {
  if (active.value == null && props.buckets.length) active.value = props.buckets.length - 1
}
function onKey(e: KeyboardEvent) {
  const n = props.buckets.length
  if (!n) return
  if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
    e.preventDefault()
    const cur = active.value ?? n - 1
    active.value = Math.min(n - 1, Math.max(0, cur + (e.key === 'ArrowLeft' ? -1 : 1)))
  } else if (e.key === 'Escape') {
    active.value = null
  }
}
</script>

<template>
  <div class="space-y-3">
    <!-- 图例：两组数据，色块和柱子形状一致 -->
    <div class="flex items-center justify-end gap-4 text-xs text-muted-foreground" aria-hidden="true">
      <span class="inline-flex items-center gap-1.5"><span class="size-2.5 rounded-[3px] bg-chart-success" />成功</span>
      <span class="inline-flex items-center gap-1.5"><span class="size-2.5 rounded-[3px] bg-chart-failure" />失败</span>
    </div>

    <div
      ref="box"
      class="relative rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      :style="{ height: `${height}px` }"
      tabindex="0"
      role="group"
      aria-label="近 24 小时每小时请求数，聚焦后用左右方向键逐小时查看"
      @keydown="onKey"
      @focus="onFocus"
      @blur="active = null"
      @pointerleave="onLeave"
    >
      <svg v-if="width" :width="width" :height="height" class="block" aria-hidden="true">
        <!-- 网格线和 y 轴刻度：1px 实线，尽量不抢眼 -->
        <g class="text-[11px] tabular-nums">
          <template v-for="t in scale.ticks" :key="t">
            <line :x1="pad.left" :x2="width - pad.right" :y1="y(t) + 0.5" :y2="y(t) + 0.5" class="stroke-border" />
            <text :x="pad.left - 8" :y="y(t)" text-anchor="end" dominant-baseline="middle" class="fill-muted-foreground">{{ fmtNum(t) }}</text>
          </template>
        </g>
        <!-- 当前查看的小时：整列浅底 -->
        <rect
          v-if="active != null"
          :x="pad.left + slot * active"
          :y="pad.top"
          :width="slot"
          :height="plotH"
          rx="4"
          class="fill-muted"
        />
        <g v-for="(b, i) in bars" :key="i">
          <path v-if="b.ok" :d="b.ok" class="fill-chart-success" />
          <path v-if="b.err" :d="b.err" class="fill-chart-failure" />
        </g>
        <text
          v-for="l in xLabels"
          :key="l.x"
          :x="l.x"
          :y="height - 6"
          text-anchor="middle"
          class="fill-muted-foreground text-[11px] tabular-nums"
        >
          {{ l.text }}
        </text>
        <!-- 命中区域：每个小时整列，比柱子大，方便对准 -->
        <rect
          v-for="(_, i) in buckets"
          :key="`hit-${i}`"
          :x="pad.left + slot * i"
          :y="pad.top"
          :width="slot"
          :height="plotH"
          fill="transparent"
          @pointerenter="active = i"
        />
      </svg>

      <p v-if="empty" class="pointer-events-none absolute inset-x-0 top-1/2 -translate-y-1/2 text-center text-sm text-muted-foreground">
        近 24 小时没有请求
      </p>

      <!-- 提示框：数值在前、名称在后，用短线标出对应的数据 -->
      <div
        v-if="current"
        class="pointer-events-none absolute z-10 min-w-36 rounded-lg border bg-popover px-3 py-2 text-xs text-popover-foreground shadow-md"
        :style="tipStyle"
        aria-hidden="true"
      >
        <div class="mb-1.5 text-muted-foreground">{{ rangeLabel(active!) }}</div>
        <div class="space-y-1 tabular-nums">
          <div class="flex items-center gap-2">
            <span class="h-0.5 w-3 rounded-full bg-chart-success" />
            <span class="font-semibold">{{ fmtNum(current.requests - current.errors) }}</span>
            <span class="text-muted-foreground">成功</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="h-0.5 w-3 rounded-full bg-chart-failure" />
            <span class="font-semibold">{{ fmtNum(current.errors) }}</span>
            <span class="text-muted-foreground">失败</span>
          </div>
          <div class="flex items-center gap-2 border-t pt-1">
            <span class="w-3" />
            <span class="font-semibold">{{ fmtTokens(current.total_tokens) }}</span>
            <span class="text-muted-foreground">Token</span>
          </div>
        </div>
      </div>
    </div>

    <p class="sr-only" aria-live="polite">{{ liveText }}</p>
    <!-- 读屏用的数据表。sr-only 要放在外层 div 上：表格会把 height 当成最小高度，直接给 table 加 sr-only
         藏不住，24 行会撑到几百像素高，又是绝对定位，会把页面底部撑出一大段空白。 -->
    <div class="sr-only">
      <table>
        <caption>近 24 小时每小时请求</caption>
        <thead>
          <tr><th>时间</th><th>成功</th><th>失败</th><th>Token</th></tr>
        </thead>
        <tbody>
          <tr v-for="(b, i) in buckets" :key="b.start">
            <td>{{ rangeLabel(i) }}</td>
            <td>{{ b.requests - b.errors }}</td>
            <td>{{ b.errors }}</td>
            <td>{{ b.total_tokens }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
