-- LockMailboxEASSync 在 S5 迁移至 internal/store/sqlite_methods.go（手写同名方法）：
-- PG 用事务级 advisory lock 串行化同一设备的 EAS 同步；SQLite 无 advisory lock，
-- 改为单写者事务串行（BEGIN IMMEDIATE），详见 scripts/sqlite/lock-semantics.md。

