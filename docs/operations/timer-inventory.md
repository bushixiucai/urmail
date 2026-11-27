# urMail Timer & Cron Inventory (Legacy systemd → Compose Topology)

本文档对应 `ARCH-PLAN-SQLITE-COMPOSE-20261119` §1.4，盘点旧栈（deb + systemd）中的全部定时任务（systemd `.timer` / cron）与关联 oneshot 服务，对照当前 Compose 形态的覆盖状态。

> 说明：代码库中未定义独立的 `/etc/cron.*` 任务；旧栈全部周期性调度均通过 `packaging/systemd/*.timer` 承载（`installer/install.sh` 启用、`installer/upgrade-checkpoint.py` 与 `cmd/urmailctl/restore_quarantine.go` 纳管）。经 Phase 2 `pm-phase2-timers-g123-1` 迁移后，G1–G3 三个缺口已全部由 `urmaild-agent` 内置 Go 调度器覆盖，无任何 systemd 运行时依赖。

---

## 1. 定时任务覆盖矩阵（Legacy Timer → Compose）

| Legacy Timer Unit | 触发频率 (`On*`) | Legacy 关联 Service & 命令 | Compose 形态覆盖方式 | 覆盖状态 | 实现落点与测试覆盖 |
|---|---|---|---|---|---|
| `urmail-backup-auto.timer` | `OnActiveSec=15min`<br>`OnUnitActiveSec=1h`<br>`Persistent=true` | `urmail-backup-auto.service`<br>`ExecStart=/usr/bin/urmailctl backup-auto`<br>（`Pre/Post`: `backup-auto-resume`） | 1. **DB 持续复制**：`compose.yaml` 中 `litestream` sidecar（SQLite 阶段转正，`compose/litestream.yml`）持续复制 `urmail.sqlite`。<br>2. **定时全量快照调度**：`urmaild-agent` 内置 `agentd.BackupScheduler` 周期触发 `backup-auto`（并支持 `backup.local.create` 手动触发），写状态文件 `backup-scheduler.json` 供 `probeBackupScheduler` 探针校验。 | **已覆盖** | `internal/agentd/backup_scheduler.go`<br>`internal/agentd/readiness.go` (`probeBackupScheduler`)<br>`internal/agentd/ops.go` (`BackupLocalCreate`)<br>`cmd/urmaild-agent/main.go`<br>单测：`backup_scheduler_test.go`、`ops_backup_test.go` |
| `urmail-cert-renew.timer` | `OnCalendar=*-*-* 03:17:00`<br>`RandomizedDelaySec=6h`<br>`Persistent=true` | `urmail-cert-renew.service`<br>`ExecStart=/usr/bin/urmailctl cert renew` | 1. **HTTPS 入口证书**：`compose.yaml` 中 `caddy` 容器原生内置 ACME 自动签发与续期。<br>2. **邮件协议 TLS 证书自动续期**：`urmaild-agent` 内置 `agentd.CertScheduler` 每日周期（含随机退避）调用 `CertificateRenew`，原子更新证书卷并在 Compose 拓扑下写入 `postfix/.cert-reload-stamp` 与 `dovecot/.cert-reload-stamp` 触发服务重载。 | **已覆盖** | `internal/agentd/cert_scheduler.go`<br>`internal/agentd/certificates.go` (`reloadComposeCertificates`)<br>`cmd/urmaild-agent/main.go`<br>单测：`cert_scheduler_test.go` |
| `urmail-usage-sync.timer` | `OnBootSec=1min`<br>`OnUnitActiveSec=5min`<br>`Persistent=true` | `urmail-usage-sync.service`<br>`ExecStart=/usr/bin/urmailctl usage sync` | `urmaild-agent` 内置 `usagesync.Scheduler` 每 5 分钟执行一次存储用量采集；`internal/usagesync` 支持在无 `doveadm` 的容器环境中直接读取共享 Maildir 卷 `maildirsize` 账本，并已解除 root/systemd 前置限制（保留 upgrade/restore hold 门禁与操作锁）。 | **已覆盖** | `internal/usagesync/sync.go`<br>`internal/usagesync/scheduler.go`<br>`cmd/urmailctl/usage_sync.go`<br>`cmd/urmaild-agent/main.go`<br>单测：`internal/usagesync/sync_test.go` |

---

## 2. 关联常驻服务对照（参考，非 Timer）

以下旧栈 systemd 常驻 unit 在 Compose 形态中已一对一映射为容器服务（`compose.yaml`）：

| Legacy systemd Service | Compose Service | 说明 |
|---|---|---|
| `urmaild.service` | `urmaild` | 核心 API / Web / 策略与归档内置循环（`cmd/urmaild/main.go`、`cmd/urmaild/telemetry.go`） |
| `urmaild-agent.service` | `urmail-agent`（`profiles: ["agent"]`） | 特权配置渲染、邮件访问监听与 G1/G2/G3 内置调度器（`cmd/urmaild-agent/main.go`） |
| `urmail-prometheus.service` | `prometheus` | 指标采集（`compose/prometheus/prometheus.yml`） |
| `urmail-alertmanager.service` | `alertmanager` | 告警路由（`compose/alertmanager/alertmanager.yml`） |

---

## 3. 缺口收口台账（G1–G3 已全部覆盖）

1. **G1 — 全量加密离线备份定时调度与健康探针去 systemd 化**：**已覆盖**。`BackupScheduler` 维护 `backup-scheduler.json` 健康状态并响应 `backup.local.create` 手动触发；`probeBackupScheduler` 与 `BackupLocalCreate` 已完全移除 `/usr/bin/systemctl` 依赖；与 `litestream` 实时流复制并存。
2. **G2 — 邮件协议 TLS 证书自动续期与重载触发**：**已覆盖**。`CertScheduler` 每日带随机退避执行 `CertificateRenew`，原子写入证书文件后通过 Compose 重载标记触发 Postfix/Dovecot 重载，无 systemd 依赖。
3. **G3 — 邮箱存储用量周期同步（`usage sync`）**：**已覆盖**。`usagesync.Sync` 解耦 root 与 systemd，支持共享 Maildir 卷直接读取 `maildirsize`，由 `usagesync.Scheduler` 每 5 分钟周期调度。
