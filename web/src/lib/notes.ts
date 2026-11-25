// 发布说明（GitHub Release 的正文，取自 CHANGELOG.md）的解析：只认标题、列表、段落，行内的加粗、代码和 http(s) 链接，
// 解析成结构交给模板渲染，不用 v-html：正文里的 HTML 原样显示成文字。
export type Inline = { kind: 'text' | 'strong' | 'code'; text: string } | { kind: 'link'; text: string; href: string }
export type Block = { kind: 'heading'; inlines: Inline[] } | { kind: 'paragraph'; inlines: Inline[] } | { kind: 'list'; items: Inline[][] }

const inlinePattern = /\*\*(.+?)\*\*|`([^`]+)`|\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g

export function parseInline(text: string): Inline[] {
  const out: Inline[] = []
  let last = 0
  for (const m of text.matchAll(inlinePattern)) {
    if (m.index > last) out.push({ kind: 'text', text: text.slice(last, m.index) })
    if (m[1] !== undefined) out.push({ kind: 'strong', text: m[1] })
    else if (m[2] !== undefined) out.push({ kind: 'code', text: m[2] })
    else out.push({ kind: 'link', text: m[3], href: m[4] })
    last = m.index + m[0].length
  }
  if (last < text.length) out.push({ kind: 'text', text: text.slice(last) })
  return out
}

export function parseNotes(md: string): Block[] {
  const blocks: Block[] = []
  // 段落和列表项可以跨行，先攒原文，遇到空行、标题或新的列表项时收尾。
  let para: string[] = []
  let items: string[] | null = null
  const flush = () => {
    if (para.length) blocks.push({ kind: 'paragraph', inlines: parseInline(para.join(' ')) })
    if (items) blocks.push({ kind: 'list', items: items.map(parseInline) })
    para = []
    items = null
  }
  for (const raw of md.split(/\r?\n/)) {
    const line = raw.trim()
    const heading = /^#{1,6}\s+(.*)$/.exec(line)
    const item = /^[-*]\s+(.*)$/.exec(line)
    if (!line) {
      flush()
    } else if (heading) {
      flush()
      blocks.push({ kind: 'heading', inlines: parseInline(heading[1]) })
    } else if (item) {
      if (para.length) flush()
      ;(items ??= []).push(item[1])
    } else if (items) {
      items[items.length - 1] += ' ' + line
    } else {
      para.push(line)
    }
  }
  flush()
  return blocks
}
