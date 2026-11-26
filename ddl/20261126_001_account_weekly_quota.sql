-- 每周额度：retrieveUserQuotaSummary 返回的分组额度（5 小时、每周窗口），开启的账号随后台额度刷新一起拉。
-- weekly_quota 是账号自己的开关：'' 跟随全局设置，'on' 开，'off' 关。quota_summary_json 是解析后的分组额度（JSON），
-- quota_summary_at 是上次拉到的 Unix 毫秒。
ALTER TABLE accounts ADD COLUMN weekly_quota TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN quota_summary_json TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN quota_summary_at INTEGER NOT NULL DEFAULT 0;
