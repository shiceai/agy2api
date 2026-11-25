-- 基线表结构。时间统一为 Unix 毫秒整数；JSON 以文本存储，在 Go 里解析。
-- 使用 STRICT 表让类型检查更接近 PostgreSQL，便于以后迁移。

-- 管理端可调的运行设置，键值存储。值统一存文本，含义和校验在 Go 里定义。
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- 代理：账号绑定一个代理后，令牌刷新、身份和项目查询、模型目录、生成请求都经它出网，连不上时不回落直连。
-- url_enc 是加密后的代理地址（含用户名密码）；接口只返回协议、host:port 和用户名，日志只写名称。
-- egress_*：最近一次成功检测到的出口（IP、国家两位代码、地区、城市、运营商 ASN，地区等来自 ipinfo.io，取不到时为空）；
-- 检测到出口和上次不同时，prev_egress_ip、egress_changed_at 记下原来的 IP 和时间。
-- check_error 是最近一次检测的失败原因，成功时为空；latency_ms 是最近一次经代理请求 Google 的往返耗时（新建连接），
-- 0 表示没测出来。停用（disabled）的代理不参与自动分配，绑定它的账号暂停调度（不改成直连）。
CREATE TABLE IF NOT EXISTS proxies (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL DEFAULT '',
    url_enc           TEXT    NOT NULL,
    egress_ip         TEXT    NOT NULL DEFAULT '',
    egress_country    TEXT    NOT NULL DEFAULT '',
    prev_egress_ip    TEXT    NOT NULL DEFAULT '',
    egress_changed_at INTEGER NOT NULL DEFAULT 0,
    checked_at        INTEGER NOT NULL DEFAULT 0,
    check_error       TEXT    NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    egress_region     TEXT    NOT NULL DEFAULT '',
    egress_city       TEXT    NOT NULL DEFAULT '',
    egress_org        TEXT    NOT NULL DEFAULT '',
    latency_ms        INTEGER NOT NULL DEFAULT 0,
    status            TEXT    NOT NULL DEFAULT 'enabled'   -- enabled | disabled
) STRICT;

-- 账号。user_agent 是导入时按账号派生后固定下来的设备画像（Cloud Code 请求用的 User-Agent），之后核验构建列表变化
-- 也不会让已有账号的版本跳变。max_concurrency 是单独设置的并发上限，0 表示跟随全局默认值。
-- subscription_json 是 loadCodeAssist 返回的当前档位、付费档位、不符合资格的原因（JSON），付费档位降到
-- Antigravity Starter Quota 时管理端提示已降档；subscription_checked_at 是上次检查的时间。
-- models_refreshed_at 是模型目录（含每个模型的额度）上次刷新的时间。
-- proxy_id 是绑定的代理，NULL 表示直连（或进程的 HTTPS_PROXY 环境变量）；还有账号绑定的代理不能删除。
CREATE TABLE IF NOT EXISTS accounts (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    email                   TEXT    NOT NULL,
    name                    TEXT    NOT NULL DEFAULT '',
    project_id              TEXT    NOT NULL DEFAULT '',
    user_project_id         TEXT    NOT NULL DEFAULT '',
    refresh_token_enc       TEXT    NOT NULL,
    client_id               TEXT    NOT NULL DEFAULT '',
    client_secret_enc       TEXT    NOT NULL DEFAULT '',
    scope                   TEXT    NOT NULL DEFAULT '',
    status                  TEXT    NOT NULL DEFAULT 'enabled',   -- enabled | disabled
    disabled_reason         TEXT    NOT NULL DEFAULT '',
    models_json             TEXT    NOT NULL DEFAULT '[]',
    synced_at               INTEGER NOT NULL DEFAULT 0,
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    user_agent              TEXT    NOT NULL DEFAULT '',
    max_concurrency         INTEGER NOT NULL DEFAULT 0,
    subscription_json       TEXT    NOT NULL DEFAULT '',
    subscription_checked_at INTEGER NOT NULL DEFAULT 0,
    models_refreshed_at     INTEGER NOT NULL DEFAULT 0,
    proxy_id                INTEGER REFERENCES proxies(id)
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_email ON accounts(email);

-- 账号 × 模型的冷却：上游限流、额度耗尽、需要验证等情况下，该账号暂时不接这个模型的请求。
-- 落库是为了重启后不丢（额度耗尽可能要等几天）。号池启动和重新加载时读入内存，选号只看内存。
-- until_at 过期的记录读取时顺手清掉。账号删除时一并删除。
CREATE TABLE IF NOT EXISTS account_cooldowns (
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,          -- 解析后实际发给上游的模型 ID
    until_at   INTEGER NOT NULL,          -- 冷却截止，Unix 毫秒
    reason     TEXT    NOT NULL DEFAULT '', -- 分类，如 rate_limit、quota_exhausted、validation_required
    created_at INTEGER NOT NULL,
    PRIMARY KEY (account_id, model)
) STRICT;

-- 下游 API Key。expires_at 是到期时间，0 表示永不过期，到期后入站鉴权返回 401。
-- token_limit、token_period：Token 上限和周期（day 每天、month 每月、total 总量，按服务器时区划分），按用量记录的
-- total_tokens（输入 + 输出 + 推理）累计；token_limit 为 0 表示不限，此时周期无意义。
-- allowed_models：允许请求的模型名（JSON 字符串数组，支持 * 通配），空表示不限制。
-- archived_tokens：用量记录清理前，把要删的记录的 Token 按 Key 累加到这里。"总量"上限按 已归档 + 现存记录 计，
-- 删旧记录不会让已用量变少；"每天""每月"上限只看保留期内的记录（保留期至少 32 天）。
CREATE TABLE IF NOT EXISTS api_keys (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT    NOT NULL DEFAULT '',
    key_hash        TEXT    NOT NULL,
    prefix          TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'enabled',   -- enabled | disabled
    created_at      INTEGER NOT NULL,
    last_used_at    INTEGER NOT NULL DEFAULT 0,
    expires_at      INTEGER NOT NULL DEFAULT 0,
    token_limit     INTEGER NOT NULL DEFAULT 0,
    token_period    TEXT    NOT NULL DEFAULT '',
    allowed_models  TEXT    NOT NULL DEFAULT '',
    archived_tokens INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);

-- 用量记录，每个下游请求一条。request_id 与响应头 X-Request-Id、日志里的 request_id 对应。
-- cached_tokens、thoughts_tokens、first_token_ms 允许 NULL：上游没给这项数据时界面显示"—"而不是 0；
-- 首字延迟只有流式请求有。reasoning 是推理强度，resolved_model 是实际发给上游的模型。
-- client_ip、user_agent（截断到 512 字符）是客户端信息；detail_json 是耗时明细、选中时账号的并发占用、每次尝试的结果、
-- 失败原因、上游响应 ID 和 trace ID（JSON），只在管理端的详情里用，不参与筛选。
-- cost_nanos 是按记录时的模型价格估算的费用，单位十亿分之一美元（整数，避免小数累加误差）；NULL 表示没有算：
-- 没有 token（失败的请求），或当时这个模型还没有价格。之后有了价格时补算 NULL 的记录，已算过的不随改价变化。
CREATE TABLE IF NOT EXISTS usage_records (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    api_key_id      INTEGER NOT NULL DEFAULT 0,
    account_id      INTEGER NOT NULL DEFAULT 0,
    request_model   TEXT    NOT NULL DEFAULT '',
    upstream_model  TEXT    NOT NULL DEFAULT '',
    prompt_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    total_tokens    INTEGER NOT NULL DEFAULT 0,
    status_code     INTEGER NOT NULL DEFAULT 0,
    stream          INTEGER NOT NULL DEFAULT 0,
    latency_ms      INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    request_id      TEXT    NOT NULL DEFAULT '',
    cached_tokens   INTEGER,
    thoughts_tokens INTEGER,
    first_token_ms  INTEGER,
    reasoning       TEXT    NOT NULL DEFAULT '',
    resolved_model  TEXT    NOT NULL DEFAULT '',
    client_ip       TEXT    NOT NULL DEFAULT '',
    user_agent      TEXT    NOT NULL DEFAULT '',
    detail_json     TEXT    NOT NULL DEFAULT '',
    cost_nanos      INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS idx_usage_created ON usage_records(created_at);
CREATE INDEX IF NOT EXISTS idx_usage_account ON usage_records(account_id);
CREATE INDEX IF NOT EXISTS idx_usage_key     ON usage_records(api_key_id);
-- 补算费用时只扫有 token、还没算费用的记录。
CREATE INDEX IF NOT EXISTS idx_usage_unpriced ON usage_records(id) WHERE cost_nanos IS NULL AND total_tokens > 0;

-- 模型价格，用来按官方 API 价格估算用量的费用（只显示，不扣费）。
-- source 为 official 的从厂商官方价格页同步（Google AI Studio、Anthropic），每次同步按型号覆盖；
-- source 为 manual 的是管理端手动设置的，同一个 model 上手动价优先，删除手动价即恢复官方价。
-- price_json 是单价（美元每百万 token，按百万分之一美元存整数）、按提示长度的分档和限时价，结构在 Go 里定义。
CREATE TABLE IF NOT EXISTS model_prices (
    model      TEXT    NOT NULL,            -- 官方价为官方型号 ID（如 gemini-4-neon）；手动价为号池里的模型名或家族名
    source     TEXT    NOT NULL,            -- official / manual
    vendor     TEXT    NOT NULL DEFAULT '', -- 官方价的来源厂商：google / anthropic；手动价为空
    name       TEXT    NOT NULL DEFAULT '', -- 官方价格页上的显示名
    price_json TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (model, source)
) STRICT;
