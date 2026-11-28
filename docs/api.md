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

## 生图模型

`gemini-3.1-flash-image` 走 Gemini 原生接口（`generateContent`、`streamGenerateContent`），请求和响应原样透传：图片在回复的
`inlineData` 里（JPEG，base64），同一个 part 还带一段较大的思考签名（`thoughtSignature`，几百 KB）。多轮改图时把上一轮模型回复的
part 原样带回（图片和签名都要），再提新的要求；请求体上限 16 MiB，图片多、尺寸大时注意总大小。

- 费用按官方价格页的图片输出价估算（上游报的输出 token 不分文字和图片，生图模型的输出基本是图片），附带的文字很少时略微高估。
- OpenAI、Anthropic 接口目前不返回图片：生图模型的回复在这两种接口里内容为空。

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
  的请求时不走这个开关（它只认 Anthropic、Responses 入站），可用"系统提示词伪装"的"客户端转用户"模式达到同样效果。
- **系统提示词伪装**（默认开，作用于所有入站）：给请求的 system 注入官方 Antigravity 客户端的基础提示词，让上游更像在和官方客户端对话，降低被风控的概率。
  默认用内置的官方基线（由官方客户端的 system 模板重构而来），也可在"设置 → 系统提示词"里切到"自定义"粘贴自己抓取的版本。
  三种方式：前置（模板在前、客户端 system 在后）、替换（只用模板）、客户端转用户（模板作为 system，客户端原 system 转成开头的一条 user 消息，默认）。
  每个请求会多几千 token 并可能改变模型行为，不需要时可整体关闭。
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

## 失败换号与冷却

- 上游返回限流、5xx 等可恢复的错误时，这个账号的这个模型进入冷却，换下一个账号重试。上游给了恢复时间就冷却到那时；没给的
  瞬时限流默认从 60 秒起，同一账号连续触发时翻倍，最长 5 分钟。重试次数和这几个时长在"设置 → 账号调度"里设。
- 同一请求在 2 个账号上都遇到不带原因的 429 时，按请求本身被上游拒绝处理：不再换号，返回 429（`Retry-After: 10`），这两个账号
  这次的冷却缩短到 10 秒、不计入连续限流，用量里记为"请求被拒"。这类请求原样重试多半仍会失败。
- **被拒上下文拦截**（默认开，在"设置 → 请求处理"里开关）：请求被拒时记下这段对话内容的指纹（只存哈希、不存原文）。之后包含这段
  内容的请求（原样重试、在后面继续对话）在本地返回 400，不再发往上游，少让账号反复碰被拒的内容；删改了相关内容的、新开的对话照常
  发往上游。管理端能查看、删除，7 天没再拦到的自动删除。
- **封禁保护**（默认关，在"设置 → 账号调度"里开）：有账号被上游以违反服务条款（TOS_VIOLATION）封禁时，被封的账号总会自动停用；
  开启后整个号池再暂停一段时间（默认 6 小时，可设 1～72 小时）。暂停期间所有生成和计数请求直接返回 503，`Retry-After` 是到暂停结束的秒数，
  用量里记为"号池暂停"；到时自动恢复，管理端概览、账号页可以提前恢复。
- 上游返回了错误的请求，用量详情里有**请求摘要**（大小、对话条数、文字字数、图片文件、工具、生成参数，不含文字内容）和**上游
  原始错误**（前 4 KB），排查上游为什么拒绝。
- 需要看原文时，在"设置 → 数据"里打开**保存失败请求的正文**（默认关）：上游返回了错误的请求把实际发给上游的请求存下，在用量
  详情里下载。会保存用户的对话内容；每分钟最多存 10 条，最多保留最近 200 条、共 500 MB，超过用量保留期的随用量记录一起删，
  可随时清空。成功的请求不存。

## 会话粘性

同一对话优先用上次的账号：上游的缓存按账号算，换号就命中不了、多花额度。在"设置 → 账号调度 → 选号"里选会话粘性的方式
和设置闲置多久后失效（默认 1 小时）：

- **严格**（默认）：对话绑定一个账号。绑定的账号并发满、或还在新号预热的请求间隔内时先等它（最多等"并发满时最多等待"
  的时长），等不到再临时借别的账号。其余同"优先"。
- **优先**：绑定的账号暂时接不了时直接临时借别的账号。"暂时"指 5 分钟内能恢复：并发满、新号预热的请求间隔、短冷却
  （5xx、容量不足、瞬时限流）、代理暂停。临时借号不改绑定，下个请求回到原账号。绑定的账号恢复不了（停用或删除、不支持这个模型、
  额度耗尽或冷却超过 5 分钟、额度低于避让阈值）时才改绑到这次成功的账号。
- **关闭**：每个请求按选号方式选账号。

对话还没有绑定账号、或绑定的账号这次接不了时，按"设置 → 账号调度 → 选号"里的选号方式选：轮询（默认）、智能（按负载、剩余额度、
近期失败率和流式首字延迟打分）或重置优先（先用这个模型额度最快重置的账号）。

所有接口都生效，对话按"API Key + 模型 + 会话标识"认，会话标识按下面的顺序取：

1. `X-Claude-Code-Session-Id` 请求头：Claude Code 每个请求都带。子代理还带 `X-Claude-Code-Agent-Id`，单独认对话，
   不会影响主会话用的账号。
2. `X-Session-Id` 请求头：通用写法。多人共用一个 Key，或前面还有一层网关时，按人或按会话设置这个头，
   首条消息相同的不同对话就不会被认成一个。
3. OpenAI 接口请求体里的 `prompt_cache_key`（Codex 填的是会话 ID）。
4. 都没有时用第一条用户消息（客户端的 system 被转成用户消息时跳过那一条）。

用量详情的"会话"一行显示沿用绑定的账号、临时借号、换号并改绑还是新对话，以及按什么认的对话。绑定只在内存里，
重启后对话重新选号。

## 新号预热与请求节奏

默认关，在"设置 → 预热节奏"里开。开启后刚导入的账号不立刻承接全部流量，而是从低并发、较大请求间隔起步，随号龄和累计成功数
逐步放开到正常水平，让上游看到渐进的使用曲线而不是突然打满；同时隔开同一账号的连续请求（最小间隔从预热初期降到稳态）。

- **预热进度**按累计成功数和号龄各算一份、取较大者：任一达标即视为预热完成。用较大者是为了重启后丢失内存里的成功计数时，
  老账号靠号龄仍是满进度、不会被重新限流。成功数或号龄任一填 0 表示该维度不参与。
- **并发**从"初始并发"随进度抬到账号的正常上限；**请求间隔**从"初始间隔"随进度降到"稳态间隔"（稳态间隔为 0 表示预热完成后不再限制）。
- 预热进度（成功计数、请求间隔）只在内存里，重启清零；号龄按账号导入时间算，不受重启影响。
- 请求遇到账号都在间隔内时，等到最早的账号放行就选号（受"并发满时最多等待"限制）。
- 账号页的状态列给预热中的账号标"预热 N%"，点开看进度、压低后的并发上限和请求间隔；并发列和用量详情里的
  "在用/上限"显示的也是压低后的上限。预热完成或关闭预热后不再显示。
