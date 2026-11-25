// 费用和单价的显示。费用按十亿分之一美元存整数，单价按百万分之一美元每百万 token 存整数。

// fmtCost 格式化费用：1 美元以上保留两位小数；以下保留两位有效数字（precise 时四位，算式里用），如 $0.00039；
// 不到百万分之一美元写成 <$0.000001；没算过为 —。
export function fmtCost(nanos: number | null | undefined, precise = false): string {
  if (nanos == null) return '—'
  const usd = nanos / 1e9
  if (usd === 0) return '$0'
  if (usd >= 1) return '$' + usd.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  if (usd < 1e-6) return '<$0.000001'
  return '$' + Number(usd.toPrecision(precise ? 4 : 2)).toString()
}

// fmtRate 格式化每百万 token 的单价，如 $0.075、$3。
export function fmtRate(micro: number | null | undefined): string {
  if (micro == null) return '—'
  return '$' + Number((micro / 1e6).toFixed(6)).toString()
}

// sumCost 合计若干项费用；全部没算过时为 null。
export function sumCost(values: (number | null | undefined)[]): number | null {
  let total: number | null = null
  for (const v of values) if (v != null) total = (total ?? 0) + v
  return total
}
