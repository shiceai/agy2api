import { ref } from 'vue'

// 接入页：接口地址（默认当前管理端的地址，改过的记在浏览器）、刚新建的 Key（只在内存里，见 guideKey）、
// 各客户端的配置示例。示例和 README 里各客户端的写法保持一致。

// guideKey 是在"密钥"页新建后带过来的完整 Key：只放在内存里，不进地址栏、不存浏览器，刷新页面就没了。
export const guideKey = ref('')

// 没有带过来 Key 时示例里的占位，和 README 一致。
export const keyPlaceholder = 'agy-xxxxxxxx'

const baseStorageKey = 'agy2api_guide_base'

// 接口地址：客户端访问网关用的地址。反向代理或内网访问时可能和打开管理端的地址不同，可以改，改过的记在浏览器。
export function defaultBase(): string {
  return location.origin
}
export function loadBase(): string {
  return localStorage.getItem(baseStorageKey) || defaultBase()
}
export function saveBase(v: string) {
  const base = v.trim().replace(/\/+$/, '')
  if (!base || base === defaultBase()) localStorage.removeItem(baseStorageKey)
  else localStorage.setItem(baseStorageKey, base)
}

export interface Snippet {
  label: string // 代码块标题，如 bash、~/.codex/config.toml
  code: string
}

export interface GuideClient {
  id: string
  label: string
  intro: string // 一句话步骤
  steps?: string[] // 没有代码时的分步说明（Cherry Studio）
  snippets: Snippet[]
  notes: string[] // 要点，最多三条
}

// buildClients 按接口地址、Key、模型生成各客户端的示例。base 不带结尾的 /。
export function buildClients(base: string, key: string, model: string): GuideClient[] {
  const k = key || keyPlaceholder
  return [
    {
      id: 'claude-code',
      label: 'Claude Code',
      intro: '设好两个环境变量后启动 Claude Code。',
      snippets: [
        { label: 'bash / zsh', code: `export ANTHROPIC_BASE_URL=${base}\nexport ANTHROPIC_AUTH_TOKEN=${k}\nclaude` },
        { label: 'PowerShell', code: `$env:ANTHROPIC_BASE_URL = "${base}"\n$env:ANTHROPIC_AUTH_TOKEN = "${k}"\nclaude` },
      ],
      notes: [
        `Claude Code 发来的官方模型名按"设置 → 模型别名"映射到号池里的模型；要用别的模型：claude --model ${model}`,
        '上游会拦截 Claude Code 的系统提示，网关默认把它降为用户消息（"客户端兼容"），不用另外设置。',
      ],
    },
    {
      id: 'codex',
      label: 'Codex',
      intro: '把 Key 放进环境变量 AGY2API_KEY，再在 ~/.codex/config.toml 里加一个服务商。',
      snippets: [
        { label: 'bash / zsh', code: `export AGY2API_KEY=${k}` },
        {
          label: '~/.codex/config.toml',
          code: `model = "${model}"\nmodel_provider = "agy2api"\n\n[model_providers.agy2api]\nname = "AGY2API"\nbase_url = "${base}/v1"\nenv_key = "AGY2API_KEY"\nwire_api = "responses"`,
        },
      ],
      notes: [
        'Codex 不认识号池里的模型名，启动时提示 "Model metadata ... not found"，不影响使用。',
        'Codex 的内置网页搜索（web_search）上游没有对应，会被去掉。',
      ],
    },
    {
      id: 'openai',
      label: 'OpenAI SDK',
      intro: 'Base URL 填接口地址加 /v1，API Key 填你的 Key。',
      snippets: [
        {
          label: 'Python',
          code: `from openai import OpenAI\n\nclient = OpenAI(base_url="${base}/v1", api_key="${k}")\nresp = client.chat.completions.create(\n    model="${model}",\n    messages=[{"role": "user", "content": "你好"}],\n)\nprint(resp.choices[0].message.content)`,
        },
      ],
      notes: [
        '思考内容放在 reasoning_content 字段，可以在"设置 → 请求处理"里关掉。',
        '客户端用的模型名不在号池里时，在"设置 → 模型别名"里加规则映射。',
      ],
    },
    {
      id: 'gemini',
      label: 'Gemini SDK',
      intro: '用 google-genai，把 base_url 设成接口地址。',
      snippets: [
        {
          label: 'Python',
          code: `from google import genai\nfrom google.genai import types\n\nclient = genai.Client(api_key="${k}", http_options=types.HttpOptions(base_url="${base}"))\nresp = client.models.generate_content(model="${model}", contents="你好")\nprint(resp.text)`,
        },
      ],
      notes: ['生图模型（gemini-3.1-flash-image）的图片在回复的 inline_data 里，多轮改图时把上一轮回复原样带回。'],
    },
    {
      id: 'cherry-studio',
      label: 'Cherry Studio',
      intro: '在"设置 → 模型服务"里添加一个提供商。',
      steps: [
        '提供商类型选 OpenAI（也可以选 Gemini 或 Anthropic）。',
        `API 地址填 ${base}（OpenAI 类型会自动补 /v1）。`,
        'API 密钥填你的 Key，点"管理"拉取模型列表，勾选要用的模型。',
      ],
      snippets: [
        { label: 'API 地址', code: base },
        { label: 'API 密钥', code: k },
      ],
      notes: ['思考内容放在 reasoning_content 字段，Cherry Studio 会显示。'],
    },
    {
      id: 'curl',
      label: 'curl',
      intro: '三种接口各一个例子，用来确认 Key 和模型能用。',
      snippets: [
        {
          label: 'Gemini',
          code: `curl ${base}/v1beta/models/${model}:generateContent \\\n  -H "x-goog-api-key: ${k}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}'`,
        },
        {
          label: 'Anthropic',
          code: `curl ${base}/v1/messages \\\n  -H "x-api-key: ${k}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${model}","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'`,
        },
        {
          label: 'OpenAI',
          code: `curl ${base}/v1/chat/completions \\\n  -H "Authorization: Bearer ${k}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${model}","messages":[{"role":"user","content":"你好"}]}'`,
        },
      ],
      notes: [],
    },
  ]
}
