# urMail Timer & Cron Inventory (Legacy systemd → Compose Topology)

本文档对应 `ARCH-PLAN-SQLITE-COMPOSE-20261119` §1.4，盘点旧栈（deb + systemd）中的全部定时任务（systemd `.timer` / cron）与关联 oneshot 服务，对照当前 Compose 形态的覆盖状态与显式缺口。

> 说明：代码库中未定义独立的 `/etc/cron.*` 任务；旧栈全部周期性调度均通过 `packaging/systemd/*.timer` 承载（`installer/install.sh` 启用、`installer/upgrade-checkpoint.py` 与 `cmd/urmailctl/restore_quarantine.go` 纳管）。

---

## 1. 定时任务覆盖矩阵（Legacy Timer → Compose）

| Legacy Timer Unit | 触发频率 (`On*`) | Legacy 关联 Service & 命令 | Compose 形态覆盖方式 | 覆盖状态 | 缺口与后续动作 |
|---|---|---|---|---|---|
| `urmail-backup-auto.timer` | `OnActiveSec=15min`<br>`OnUnitActiveSec=1h`<br>`Persistent=true` | `urmail-backup-auto.service`<br>`ExecStart=/usr/bin/urmailctl backup-auto`<br>（`Pre/Post`: `backup-auto-resume`） | 1. **DB 持续复制**：`compose.yaml` 中 `litestream` sidecar（SQLite 阶段转正，`compose/litestream.yml`）持续复制 `urmail.sqlite`。<br>2. **完整加密离线备份**：尚无内置调度器。 | **部分覆盖**（仅 SQLite WAL 增量复制；全量加密归档未覆盖） | **缺口 G1**：`internal/agentd/ops.go:399`（`systemctl start --no-block urmail-backup-auto.service`）与 `internal/agentd/readiness.go:330-345`（`probeBackupScheduler` 调 `systemctl cat/is-enabled/is-active urmail-backup-auto.timer`）强依赖 systemd；Compose 内需替换为 Go 内置 ticker 调度器或独立备份 worker，并解耦 root/systemd 前置检查（`cmd/urmailctl/backup_auto.go`）。 |
| `urmail-cert-renew.timer` | `OnCalendar=*-*-* 03:17:00`<br>`RandomizedDelaySec=6h`<br>`Persistent=true` | `urmail-cert-renew.service`<br>`ExecStart=/usr/bin/urmailctl cert renew` | 1. **HTTPS 入口证书**：`compose.yaml` 中 `caddy` 容器原生内置 ACME 自动签发与续期。<br>2. **邮件协议 TLS 证书（Postfix/Dovecot）**：`internal/agentd/certificates.go` 提供签发/续期/安装实现，但无定时触发器。 | **部分覆盖**（HTTPS 已由 Caddy 接管；邮件协议证书续期未自动触发） | **缺口 G2**：`urmail-cert-renew.timer` 尚未迁移为 Compose 内周期任务；Postfix/Dovecot 使用的证书路径（`/srv/urmail/data/certs` / `DomainCertDir`）需在 `urmaild-agent` 或独立调度循环中定时续期并触发邮件服务重载。 |
| `urmail-usage-sync.timer` | `OnBootSec=1min`<br>`OnUnitActiveSec=5min`<br>`Persistent=true` | `urmail-usage-sync.service`<br>`ExecStart=/usr/bin/urmailctl usage sync` | 无。`urmaild` 与 `urmaild-agent` 均未启动周期性存储用量采集循环，Compose 中亦无对应 sidecar。 | **未覆盖** | **缺口 G3**：`cmd/urmailctl/usage_sync.go` 要求 root 与升级/恢复锁检查并调用本地 `doveadm`；Compose 形态需在 `urmaild` / `urmaild-agent` 内置 5 分钟调度循环或独立 sidecar，并通过共享卷/受控通信采集 Dovecot 邮箱用量。 |

---

## 2. 关联常驻服务对照（参考，非 Timer）

以下旧栈 systemd 常驻 unit 在 Compose 形态中已一对一映射为容器服务（`compose.yaml`）：

| Legacy systemd Service | Compose Service | 说明 |
|---|---|---|
| `urmaild.service` | `urmaild` | 核心 API / Web / 策略与归档内置循环（`cmd/urmaild/main.go`、`cmd/urmaild/telemetry.go`） |
| `urmaild-agent.service` | `urmail-agent`（`profiles: ["agent"]`） | 特权配置渲染与邮件访问监听（`cmd/urmaild-agent/main.go`） |
| `urmail-prometheus.service` | `prometheus` | 指标采集（`compose/prometheus/prometheus.yml`） |
| `urmail-alertmanager.service` | `alertmanager` | 告警路由（`compose/alertmanager/alertmanager.yml`） |

---

## 3. 未覆盖项（缺口清单汇总）

1. **G1 — 全量加密离线备份定时调度与健康探针去 systemd 化**：
   - 影响：`urmail-backup-auto.timer` 在 Compose 中不存在；`ApplyManager.probeBackupScheduler`（`internal/agentd/readiness.go:330`）与 `backupLocalCreateCommand`（`internal/agentd/ops.go:399`）在无 systemd 容器内无法通过。
   - 建议落地：在 `urmaild-agent`（或独立备份 worker）中增加受 `backup_schedules` 驱动的 Go 定时调度，并通过容器内健康文件/状态表替代 `systemctl` 探针。
2. **G2 — 邮件协议 TLS 证书自动续期与重载触发**：
   - 影响：`urmail-cert-renew.timer` 缺失导致 Postfix/Dovecot 证书不会自动续期（Caddy 仅覆盖 HTTP/HTTPS）。
   - 建议落地：在 `urmaild-agent` 中增加每日周期循环调用 `certificates.go` 续期逻辑（含随机退避），续期后原子更新证书卷并触发 Postfix/Dovecot 重载。
3. **G3 — 邮箱存储用量周期同步（`usage sync`）**：
   - 影响：`urmail-usage-sync.timer` 缺失导致邮箱配额/用量展示与 `quota` 统计在 Compose 形态下不会每 5 分钟自动刷新。
   - 建议落地：将 `usage sync` 逻辑抽象为可跨容器/共享 Maildir 卷执行的调度任务（默认 5 分钟周期），在 `urmaild` 或 `urmaild-agent` 中常驻执行。
