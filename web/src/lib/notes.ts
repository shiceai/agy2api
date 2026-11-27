// 发布说明（GitHub Release 的正文，取自 CHANGELOG.md）的解析：只认标题、列表、段落，行内的加粗、代码、
// [文字](网址) 和裸的 http(s) 网址（发布说明末尾的"完整变更"就是裸网址，GitHub 页面上也会识别成链接），
// 解析成结构交给模板渲染，不用 v-html：正文里的 HTML 原样显示成文字。
export type Inline = { kind: 'text' | 'strong' | 'code'; text: string } | { kind: 'link'; text: string; href: string }
export type Block = { kind: 'heading'; inlines: Inline[] } | { kind: 'paragraph'; inlines: Inline[] } | { kind: 'list'; items: Inline[][] }

// 裸网址只取网址里允许的 ASCII 字符，遇到中文、全角标点或空白就结束。
const inlinePattern = /\*\*(.+?)\*\*|`([^`]+)`|\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)|(https?:\/\/[\w\-.~:/?#[\]@!$&'()*+,;=%]+)/g

// trimURL 去掉裸网址末尾的英文标点和没配对的右括号，它们是句子的一部分，不属于网址。
function trimURL(url: string): string {
  for (;;) {
    const c = url.at(-1)
    if (c && `.,;:!?'"`.includes(c)) url = url.slice(0, -1)
    else if (c === ')' && url.split(')').length > url.split('(').length) url = url.slice(0, -1)
    else return url
  }
}

export function parseInline(text: string): Inline[] {
  const out: Inline[] = []
  let last = 0
  for (const m of text.matchAll(inlinePattern)) {
    if (m.index > last) out.push({ kind: 'text', text: text.slice(last, m.index) })
    let end = m.index + m[0].length
    if (m[1] !== undefined) out.push({ kind: 'strong', text: m[1] })
    else if (m[2] !== undefined) out.push({ kind: 'code', text: m[2] })
    else if (m[3] !== undefined) out.push({ kind: 'link', text: m[3], href: m[4] })
    else {
      const url = trimURL(m[5])
      out.push({ kind: 'link', text: url, href: url })
      end = m.index + url.length // 去掉的标点留给后面的文字
    }
    last = end
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
