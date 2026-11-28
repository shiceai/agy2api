-- 告警通知的渠道（"设置 → 通知"）：账号被封、refresh token 失效、没号可用等事件发到这些渠道。
-- type 是渠道类型（webhook、dingtalk、feishu、wecom、telegram）；config_enc 是加密后的配置 JSON（Webhook 地址、
-- 加签密钥、Bot Token、Chat ID 等，含凭据，不能明文落库）；enabled 为 0 时不发。
-- last_sent_at、last_error 是最近一次发送（含测试）的时间和失败原因，成功时 last_error 为空。
CREATE TABLE IF NOT EXISTS notify_channels (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    type         TEXT    NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    config_enc   TEXT    NOT NULL,
    last_sent_at INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;
