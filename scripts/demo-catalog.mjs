// 更新演示站用的目录快照 web/src/demo/catalog.json：从运行中的 agy2api 取模型目录、思考档位家族、价格表、
// 可选模型名和客户端过滤的预置名单。这些都是公开信息；不取账号、Key、代理、用量，写入前还会自检。
//
// 用法（服务里要有已同步的启用账号）：
//   AGY2API_ADMIN_PASSWORD=<口令> node scripts/demo-catalog.mjs [http://127.0.0.1:8080]
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const base = (process.argv[2] ?? 'http://127.0.0.1:8080').replace(/\/$/, '') + '/api/admin'
const password = process.env.AGY2API_ADMIN_PASSWORD
if (!password) {
  console.error('先设置 AGY2API_ADMIN_PASSWORD')
  process.exit(1)
}
const get = async (p) => {
  const res = await fetch(base + p, { headers: { 'X-Admin-Password': password } })
  if (!res.ok) throw new Error(`${p}: HTTP ${res.status}`)
  return res.json()
}

const accounts = await get('/accounts')
const source = accounts.accounts.find((a) => a.status === 'enabled' && a.models.length)
if (!source) throw new Error('没有已同步的启用账号，取不到模型目录')
const settings = await get('/settings')
const prices = await get('/prices')
const catalog = {
  // 只留界面用到的字段；额度和重置时间由演示后端按每个演示账号生成。
  models: source.models.map((m) => ({ id: m.id, display_name: m.display_name, supports_thinking: !!m.supports_thinking })),
  tiers: settings.tiers,
  families: settings.families,
  prices: { status: prices.status, auto_sync: prices.auto_sync, models: prices.models },
  model_names: await get('/models'),
  default_keywords: (await get('/settings/client-filter')).default_keywords,
}

const text = JSON.stringify(catalog)
for (const bad of ['@', 'refresh', 'MODEL_PLACEHOLDER', 'project_id', 'proxy']) {
  if (text.includes(bad)) throw new Error(`快照里出现了不该有的内容：${bad}`)
}
const out = path.join(path.dirname(fileURLToPath(import.meta.url)), '../web/src/demo/catalog.json')
fs.writeFileSync(out, JSON.stringify(catalog, null, 2) + '\n')
console.log(`已更新 ${out}：模型 ${catalog.models.length}，家族 ${catalog.families.length}，价格 ${catalog.prices.models.length} 行`)
