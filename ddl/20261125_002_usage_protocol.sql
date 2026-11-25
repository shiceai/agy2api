-- 用量记录加"接口"：gemini（Gemini 原生接口）或 anthropic（Anthropic Messages 接口）。之前的记录都来自 Gemini 接口。
ALTER TABLE usage_records ADD COLUMN protocol TEXT NOT NULL DEFAULT 'gemini';
