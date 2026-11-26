# 接口说明

各客户端怎么接入见 [README](../README.md)，这里是各接口的细节：请求怎么转换、哪些不支持、会话粘性怎么认对话。部署见[部署说明](deploy.md)。

## 模型列表和档位

`/v1/models`（三种接口共用，按请求头区分格式）列出的模型和官方客户端一致，取自上游模型目录里给官方客户端用的信息：

- 模型选择器里的模型。分档的模型（官方分组里同一家族的 Low、Medium、High、XHigh）合成一个家族名，如 `gemini-4-neon`、
  `claude-opus-5-5`，显示名用家族的（Gemini 4 Neon）；不分档的照原名，如 Orchestrator、`gpt-oss-120b-medium`。
- 官方客户端辅助用途的模型：`gemini-3-flash`（命令）、`gemini-3.5-flash-lite`（网页搜索、提交信息）、`gemini-3.1-flash-image`（生图）。
- 其余的不列出：旧名字（如 `gemini-2.5-flash`，上游指向 Gemini 3.5 Flash Lite）、自动分档和监控用的变体（`*-tiered*`、
  `gemini-4-hydrogen-sonar`）、编辑器补全模型（`tab_*`）等。它们和各档位的名字（如 `gemini-4-neon-high`）一样照样能调用，
  只是不保证能当普通模型用。
- 账号的模型目录还没刷新到带这些信息时（刚升级上来），列出目录里的全部模型。各账号的目录取并集。
- API Key 限制了模型时只列允许的；允许了家族名就列家族，只允许了其中某些档位（如 `gemini-4-neon-high`）就列这些档位。

用家族名时，档位按请求里的思考参数选：Gemini 的 `thinkingLevel`（或 `thinkingBudget` 换算），OpenAI 的 `reasoning_effort`、
`reasoning.effort`，Anthropic 的 `thinking` 预算或 `output_config.effort`；都没有时用管理端"设置 → 思考档位"里这个家族单独设的
档位，再用默认档位。家族没有这一档时先往低、再往高取最接近的。按家族名查单个模型（`/v1/models/{家族名}`）时，显示名是
按默认档位解析到的那一档。

## Anthropic 接口

除了 Gemini 原生接口，网关还提供 Anthropic Messages 接口，Claude Code 等客户端可以直接接入（用法见 README）：

- `POST /v1/messages`（流式和非流式）、`POST /v1/messages/count_tokens`；带 `anthropic-version` 请求头的 `GET /v1/models`
  返回 Anthropic 格式的模型列表。API Key 放在 `x-api-key` 或 `Authorization: Bearer` 里。
- 请求转成 Gemini 格式后走同一套号池：选号、冷却、换号、会话粘性、Key 的限额和允许的模型、客户端过滤、系统提示词伪装都一样。
  工具调用、图片和 PDF、思考参数（`thinking`、`output_config.effort`）都会转换；Claude Code 加在 system 里的计费信息块不会发给上游。
- **模型别名**：客户端的模型名不在号池目录里时，先去掉末尾的 `[1m]` 和 `-YYYYMMDD` 日期，还不在就按规则映射（自定义规则在前、
  预置规则在后）。预置规则把 Claude 的官方名映射到号池里的 `claude-opus-5-5`、`claude-sonnet-5-5` 家族，Haiku 映射到
  `claude-sonnet-5-5-low`。在管理端"设置 → 模型别名"里可以关掉预置规则、加自定义规则（每行 `别名 => 目标`，可用 `*` 通配），
  并试解析一个模型名。所有接口都生效。
- 不支持：Anthropic 的服务端工具（如 `web_search`，请求里有时去掉，其它工具照常）、文件 API（`file_id`）、批处理 API。
  Antigravity 上的 Claude 不返回思考内容。
- **客户端兼容**（默认开，作用于 Anthropic 和 OpenAI Responses 入站）：上游按系统提示里的客户端特征（Claude Code、Codex 等）拦截，返回
  伪装成额度耗尽的 429（实测与模型、入站协议无关，与措辞无关，删改单段也躲不掉；但上游只检查系统提示、不看对话内容）。
  开启后把客户端 system 整体降为开头的一条用户消息、系统提示留空即可放行，代价是客户端 system 从 system 级降到 user 级、
  可能略微改变模型遵循方式。在"设置 → 模型别名 → 客户端兼容"里开关。用别的中间服务以 Gemini 协议转发 Claude Code
  的请求时不走这个开关（它只认 Anthropic、Responses 入站），可开"系统提示词伪装"的"客户端转用户"模式达到同样效果，模板留空即可（只降级、不注入）。
- 用量页的"接口"一列区分 Gemini、Anthropic、OpenAI（Chat Completions）和 OpenAI Responses 的请求，请求模型记客户端原来的名字，"实际发出"是映射后的模型。

## OpenAI 接口

网关还提供 OpenAI Chat Completions 和 Responses 接口，OpenAI SDK、Cherry Studio、Codex 等客户端把 Base URL 设成
`http://<地址>/v1` 即可接入（用法见 README）：

- `POST /v1/chat/completions`（流式和非流式）；`GET /v1/models`、`GET /v1/models/{id}` 返回 OpenAI 格式。API Key 放在
  `Authorization: Bearer` 里。
- `/v1/models` 三种接口共用，按请求头区分格式：带 `anthropic-version` 的是 Anthropic 格式，带 `x-goog-api-key` 或 `?key=`
  （Gemini SDK 都带）的是 Gemini 格式，其余是 OpenAI 格式。`/v1beta/models` 始终是 Gemini 格式。
- 和 Anthropic 接口一样走同一套号池和设置（模型别名、客户端过滤、系统提示词伪装、请求处理）；"客户端兼容"开关作用于
  Responses 接口，不作用于 Chat Completions。开头的 `system`、`developer` 消息进系统提示，对话中间的转成用户消息里的 `<system-reminder>`。
- 转换：工具调用和 `tool_choice`；图片（data URL，http 地址要打开远程图片内联）、音频（`input_audio`）、文件（`file_data`）；
  `max_completion_tokens`（或 `max_tokens`）、`temperature`、`top_p`、`stop`；`reasoning_effort`（none、minimal 按 low）。
  `prompt_cache_key` 用来认对话（见"会话粘性"）。其它参数（如 `presence_penalty`、`seed`、`logprobs`）忽略。
- 思考内容在 `reasoning_content` 字段返回（非标准字段，DeepSeek 带起来的写法）。OpenAI 格式没有放思考签名的位置，下一轮
  历史里的工具调用由网关补占位签名，多轮工具调用照常可用。用量按 OpenAI 口径：`prompt_tokens` 含命中缓存，
  `completion_tokens` 含思考。
- 结构化输出：`response_format` 的 `json_schema` 转成上游认的 `responseSchema`（内联 `$ref`、去掉不支持的关键字），
  对 Gemini 模型生效；Claude 模型上游报 400。`json_object` 上游不强制，靠提示词。
- 不支持：`n` 大于 1、用 Files API 上传的文件（`file_id`）。

### Responses 接口

- `POST /v1/responses`（流式和非流式），Codex 等客户端用（配置见 README）。响应对象、流式事件按 OpenAI 的格式，错误格式同上。
- 不存响应：每轮要带上完整对话（Codex 就是这样，`store: false`）；`previous_response_id`、`conversation`、`background`
  返回 400，也没有 `GET`、`DELETE /v1/responses/{id}`。
- 转换：`instructions` 和开头的 `system`、`developer` 消息进系统提示（开"客户端兼容"时一起降为用户消息），对话中间的转成
  `<system-reminder>`；`input` 里的消息（文字、图片、文件）、工具调用和结果、reasoning 条目；函数工具、命名空间工具（展开成
  "命名空间__函数名"，回给客户端时拆回来）和自定义工具（Codex 的 `apply_patch` 等，转成只有一个字符串参数 `input` 的函数，
  回给客户端时还原成 `custom_tool_call`）；`max_output_tokens`、`temperature`、`top_p`、`tool_choice`、`reasoning.effort`
  （同 `reasoning_effort`）、`text.format`（同 `response_format`）。内置工具（`web_search`、`file_search` 等）上游没有对应，去掉。
- 思考放在 reasoning 条目的 `summary` 里。`include` 里有 `reasoning.encrypted_content` 时思考签名放进 `encrypted_content`：
  Claude 的签名跟着思考；Gemini 的签名挂在工具调用上，网关在工具调用前插一条摘要为空、只带签名的 reasoning 条目。客户端下一轮
  原样传回，网关再挂回原处；不带签名的 reasoning 条目不发给上游。
- 用量：`input_tokens` 含命中缓存，`output_tokens` 含思考。输出到上限时以 `response.incomplete` 结束，上游中途出错时是
  `response.failed`。

## 会话粘性

同一对话优先用上次的账号：上游的缓存按账号算，换号就命中不了、多花额度。在"设置 → 账号调度 → 会话粘性"里开关（默认开）
和设置闲置多久后失效。所有接口都生效，对话按"API Key + 模型 + 会话标识"认，会话标识按下面的顺序取：

1. `X-Claude-Code-Session-Id` 请求头：Claude Code 每个请求都带。子代理还带 `X-Claude-Code-Agent-Id`，单独认对话，
   不会影响主会话用的账号。
2. `X-Session-Id` 请求头：通用写法。多人共用一个 Key，或前面还有一层网关时，按人或按会话设置这个头，
   首条消息相同的不同对话就不会被认成一个。
3. OpenAI 接口请求体里的 `prompt_cache_key`（Codex 填的是会话 ID）。
4. 都没有时用第一条用户消息（客户端的 system 被转成用户消息时跳过那一条）。

用量详情的"会话"一行显示沿用账号、换了账号还是新对话，以及按什么认的对话。
