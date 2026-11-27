-- 代理测试拆成两个延迟：connect_ms 是服务器和代理建立 TCP 连接的耗时（到代理）；latency_ms 改为经代理连上 Google 后
-- 在同一连接上再请求一次的往返（到 Google，不含建连和握手）。都是最近一次测试的结果，0 表示没测出来。
ALTER TABLE proxies ADD COLUMN connect_ms INTEGER NOT NULL DEFAULT 0;

-- 旧的 latency_ms 含建连和握手，和新口径不能比，清掉；启用的代理标成未测试，启动后由后台测试按新口径重测。
UPDATE proxies SET latency_ms = 0;
UPDATE proxies SET checked_at = 0 WHERE status = 'enabled';
