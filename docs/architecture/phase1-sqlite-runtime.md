# Phase 1 SQLite 单引擎运行时架构与边界说明

> 基线提交：`ca7772ca77144f16a60082301ec3ff34b8bcb217`（tree `bbb33327066d6bc9034352638935ada6ec79f0c7`），对应 `main` CI run `45572290894`（17/17 success）。
> 本文档只记录当前代码已落地的事实与已冻结设计（`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md`）；未合入切片（S6 / S7.5 / S8）均明确标为**计划中 / 待办**。

---

## 1. 单文件 SQLite 的取舍与租户隔离边界

### 1.1 与 PostgreSQL 多租户 / RLS 的边界差异
- PostgreSQL 时代依赖多数据库角色（`urmail_migrate` / `urmail_app` / `urmail_platform` / `urmail_dovecot`）与行级安全策略（RLS）在引擎层兜底；单文件 SQLite 没有服务端角色与 RLS，引擎层无法区分调用方身份。
- 因此 Phase 1 将隔离契约上移至应用层数据库边界与引擎触发器双层协同：
  - **连接池封装**：`internal/db/sqlite_pool.go:22-30`（`NewSQLitePool`）将 `*sql.DB` 包装为带 `isolation.Scope` 的 `*db.Pool`，使 `RequireTenantTx` / `RequirePlatformTx` 沿用统一事务契约。
  - **方法级强制（装饰器）**：`internal/db/isolation/isolation.go:7-9` 与生成文件 `internal/db/isolation/scoped_querier_gen.go:1-60` 对 `store.Querier` 全部 703 个方法做统一包装。所有 `ScopeTenant` 方法在进入内层查询前必须通过：
    1. `CheckMethod(ctx, ..., ScopeTenant)`：无 `ScopeTenant` 上下文或 `TenantID <= 0` 直接返回 `ErrMissingTenantScope`（`internal/db/isolation/isolation.go:44`）。
    2. `CheckTenantMatch`：参数中的 `TenantID` 与上下文不一致（或 `pgtype.Int8` 无效）直接返回 `ErrCrossTenantAccess`（`internal/db/isolation/isolation.go:48`）。
  - **越权矩阵回归**：`internal/db/isolation/scoped_querier_test.go:180-260` 结合 `internal/db/isolation/method_scopes.json` 与 `internal/store/querier.go` 反射遍历全部方法，断言空 `ctx`、`nil` `ctx`、错误 scope、`TenantID <= 0`、跨租户参数（`202 != 101`）及无效 `pgtype.Int8` 均被拦截且内层调用次数为 `0`；该套件已纳入 PR 快集 `.github/workflows/ci.yaml:194`（`go test -race`）。
  - **Append-only 引擎触发器**：5 张只追加表（`audit_logs`、`announcements`、`channels`、`ai_usage_requests`、`delivery_events`）在 `migrations_sqlite/0001_init.sql:2776-2809` 均通过 `BEFORE UPDATE` 与 `BEFORE DELETE` 触发器执行 `RAISE(ABORT, '<table> is append-only')`，不依赖应用层约定。

---

## 2. 迁移链与运行时 PRAGMA 口径

- **迁移链**：`migrations_sqlite/` 包含 `0001_init.sql` 至 `0007_backup_offsite_state.sql`，当前链顶版本 `ExpectedSchemaVersion = 7`（`internal/sqlitedb/open.go:34`）。
- **每连接 PRAGMA 契约**（`internal/sqlitedb/open.go:46-57`）：
  - `PRAGMA foreign_keys=ON`
  - `PRAGMA busy_timeout=5000`
  - `PRAGMA journal_mode=WAL`
  - `PRAGMA synchronous=NORMAL`
- **启动门禁（fail-closed）**：`internal/sqlitedb/open.go:274-328`（`OpenExisting`）拒绝不存在路径、符号链接、非常规文件、`PRAGMA user_version != 7`、`schema_migrations` 存在 `dirty <> 0` 行，并调用 `AssertPragmas`（`internal/sqlitedb/open.go:236-268`）逐项校验上述 4 项 PRAGMA。
- **迁移硬断言与幂等性**：`internal/sqlitedb/schema_test.go:46-104` 验证空库建库后 `PRAGMA foreign_key_check` 为 `0` 行、`PRAGMA integrity_check = ok`、`PRAGMA user_version = 7`，且重复执行 `Migrate` 保持幂等。

---

## 3. Auth 快照与失效语义（计划中：S6）

> **当前状态**：**计划中（S6）**。`ca7772ca` 中尚不存在 `internal/dovecotauth` 包，Dovecot 认证快照库与 `RequireFreshAfterRestore` 尚未合入。

按照已冻结设计 `PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §5.4（`158-169` 行）：
1. **补偿为主路径（废弃 ATTACH 多库原子）**：由于 SQLite 官方规则在任一参与库启用 `WAL` 时不做两阶段提交，改密/停用/删域事务**先提交主库**，随后普通连接同步更新快照行；写入失败时置 `snapshot_dirty` 并使 readiness 降级（degraded），通过退避重试清除 dirty。
2. **启动对账与全量重建**：进程启动时抽样对账；全量重建仅用于故障修复与 24h 兜底，落盘须执行 `PRAGMA wal_checkpoint(TRUNCATE)` 与 `journal_mode=DELETE` 后单文件 `rename` 原子替换。
3. **恢复后强制重建**：主库文件级恢复或回退完成后，强制全量重建 auth 快照作为恢复序列放行前置。

---

## 4. 备份与恢复位点现状

### 4.1 已落地的备份快照与编排位点
- **一致快照**：`cmd/urmailctl/backup_sqlite.go:28-82`（`createSQLiteSnapshot`）先执行 `PRAGMA wal_checkpoint(TRUNCATE)`，再执行 `VACUUM INTO ?`（不直接拷贝活动 WAL 库），随后以 `OpenExisting(..., ExpectedSchemaVersion)` 重开校验 `PRAGMA integrity_check = ok` 与 `user_version = 7`。
- **`backupauto` 引擎无关 artifact 映射**：
  1. `internal/backupauto/planner.go:18,70-72`：`KindSqliteSnapshot = "sqlite-snapshot"`，映射为 `urmail-<stamp>.sqlite`；
  2. `internal/backupauto/spool.go:18,30,55`：支持与 `urmail-mail-<stamp>.tar.gz`、`urmail-config-<stamp>.tar.gz` 同代扫描；
  3. `internal/backupauto/retention_store.go:101`：retention manifest 校验接受 `KindPgDump || KindSqliteSnapshot`；
  4. `internal/backupauto/retention_store.go:252-309`：`completeLocalArtifacts` -> `VerifyCompleteArtifactSet` -> `expectedArtifactKind` 校验同代 3 件套（1 个 DB + config tar + mail tar）。
- **升级围栏前置（fail-closed）**：`cmd/urmailctl/upgrade.go:932-995`（`preflightFreshBackup`）在 `IsSQLite()` 模式下对配置无效、SQLite 账本不可打开、缺 `backup_runs` 表、`Scope != ScopeComplete`、无效 `OffsiteState` 均直接报错拒绝（不走 PG 的 degraded 降级放行）。
- **Offsite 记账与只读核验**：`cmd/urmailctl/backup_offsite.go:244-279`（`verify-offsite`）仅拉取远端清单与对象校验和，不写本地 `backup_runs` 状态，失败或密钥不匹配返回非零退出码。

### 4.2 待办（S6 / S7.5）：SQLite Restore 链路尚未可用
- `cmd/urmailctl/restore_database_sqlite.go:27-78` 已实现 `restoreSQLiteSnapshot`（含 checkpoint、清理 `-wal`/`-shm`、staged 校验与原子替换）并通过单元测试，但**尚未接入 `urmailctl restore` 主流程**。
- 当前 `cmd/urmailctl/restore.go:178-185` 调用 `requirePostgresRestoreEngine(activeCfg)`（`cmd/urmailctl/restore_database.go:99-103`），对 `cfg.IsSQLite()` **显式 fail-closed 拒绝**，提示需等待 S6 auth-snapshot（`dovecotauth.RequireFreshAfterRestore`）与 S7.5 SQLite restore 执行器合入。

---

## 5. 首启 `config prepare --first-boot` 门禁语义

- **零放宽口径**：`cmd/urmailctl/config_prepare.go:22-59` 仅接受 `--first-boot`（传入 `--skip-preflight`、`--skip-backup` 等其他参数均报 usage 错误退出）；当升级围栏激活（`maintenance.IsUpgradeHeld(root)`）时拒绝 `--first-boot`，强制走常规 `config prepare`。
- **空根保护**：`internal/agentd/prepare.go:41-49,85-94`（`PrepareOfflineFirstBoot`）复用 `offlinePreparationGuard`，并检查 `manifest.json` 与 `appliedManifestBackupPath` 不得存在，防止覆盖已渲染的现网配置。
- **常规升级路径未受影响**：`firstBoot == false` 分支继续强制持有维护锁、`IsUpgradeHeld` 与写者单元全停检查（`cmd/urmailctl/config_prepare.go:41-58`）。

---

## 6. 身份单源化

- 邮件存储属主与 Dovecot 配置共享单一配置来源 `URMAIL_MAIL_USER`（默认 `"mail"`，`internal/config/config.go:100,305`）：
  - 存储目录制备：`internal/agentd/mail_storage.go:23,137-142` 使用 `m.cfg.MailUser`（默认 `"mail"`）；
  - Dovecot 渲染：`internal/reconcile/snapshot.go:261` 透传 `MailUID: cfg.MailUser` 至 `internal/mail/dovecot/render.go:9,102`，无 PostgreSQL / SQLite 双份残留。

---

## 7. 已知 P3（只登记，本片不改）

1. **`platform_observability.sql` 备份超期告警缺少 `scope` 过滤（P-QA-5）**：
   - `internal/store/queries/platform_observability.sql:29-31` 中 `PlatformBackupFreshness` 使用 `(SELECT max(finished_at) FROM backup_runs WHERE state='success') AS last_success_at`（未过滤 `scope = 'local-mail-and-metadata'`），且 `internal/telemetry/observability.go:282-288` 与 `internal/telemetry/alerts.go:149` 仅用 `LastSuccessAt` 判断 `telemetry_backup_stale`；因此一次 `metadata-only` 的成功备份会压掉完整备份超期告警。
2. **`settings` 目录界面令牌豁免已解除（历史背景确认）**：
   - `web/scripts/check-color-tokens.mjs:2,26` 确认 `settings` 目录的硬编码颜色豁免已于 `v0.6.20` A3 解除并纳入全量计数（`< 140`），当前无残留豁免项。
