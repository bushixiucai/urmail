
-- 中文注记（sqlc 的 sqlite 引擎按字节偏移切分语句，任何非 ASCII 注记都必须放在文件末尾）：
-- file note: MarketingCampaignReportSnapshot 在 S7 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- file note: PG 版为 WITH ... AS MATERIALIZED + jsonb_agg(to_jsonb(...) ORDER BY ...)，并以
-- file note: current_setting('urmail.user_id') 做 owner/admin 校验；SQLite 无 MATERIALIZED、
-- file note: 无会话设置，且 sqlc 的 SQLite 解析器不接受 CTE 内聚合，故按同一有界事实集在 Go 侧组装。
-- file note: MarketingFlowReportSnapshot 同（entries/positions/executions 三路事实）。
-- file note: SetMarketingReportBudget 在 S7 迁移至 internal/store/sqlite_methods.go（手写同名空操作）：
-- file note: PG 用语句级 statement_timeout 限制报告快照；SQLite 无该函数，超时由进程内查询预算承担。
