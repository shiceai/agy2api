-- 失败请求的完整请求体：开了"保存失败请求的正文"（设置 → 数据，默认关）时，上游返回了错误的请求把实际发给上游的
-- JSON 用 gzip 压缩后存这里，管理端在用量详情里下载，排查上游为什么拒绝。含用户的对话内容。
-- request_id 对应 usage_records.request_id；size 是压缩前的字节数，body_gz 的长度是占用。
-- 只留最近的一部分（条数和总大小都有上限，见 relay 的 capture），超过用量保留期的随用量记录一起删。
CREATE TABLE IF NOT EXISTS request_bodies (
    request_id TEXT    PRIMARY KEY,
    body_gz    BLOB    NOT NULL,
    size       INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_request_bodies_created ON request_bodies (created_at);
