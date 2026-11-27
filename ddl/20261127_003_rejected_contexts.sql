-- 被上游拒绝的上下文（"设置 → 请求处理 → 被拒上下文拦截"，默认开）：一个请求被判为"请求被拒"（多个账号都对它
-- 回了不带原因的 429）时，记下它对话内容的上下文指纹（见 antigravity.ContextFingerprints，只存哈希、不存原文）。
-- 之后包含这段上下文的请求（原样重试、在后面继续对话）在本地直接拒绝，不再发往上游。
-- fingerprint 是最后一个前缀的指纹；turns 是被拒时参与指纹的消息条数；api_key_id、model、client_ip、request_id
-- 是首次被拒的请求（request_id 对应用量记录）；hits 是之后被拦下的次数，last_hit_at 是最近一次被拦（或首次被拒）
-- 的时间，超过 7 天没再拦到的自动删除。
CREATE TABLE IF NOT EXISTS rejected_contexts (
    fingerprint TEXT    PRIMARY KEY,
    turns       INTEGER NOT NULL,
    api_key_id  INTEGER NOT NULL DEFAULT 0,
    model       TEXT    NOT NULL DEFAULT '',
    client_ip   TEXT    NOT NULL DEFAULT '',
    request_id  TEXT    NOT NULL DEFAULT '',
    hits        INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    last_hit_at INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_rejected_contexts_last_hit ON rejected_contexts (last_hit_at);
