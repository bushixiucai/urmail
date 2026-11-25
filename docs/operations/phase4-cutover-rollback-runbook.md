# Phase 4 同机原地切换与回滚 Runbook

> **状态：`DRAFT（未演练）——禁止据此直接执行生产切换；每条标 [待实装核定] 的步骤须由 DevOps 在隔离环境实跑并签字后转正式版`**
>
> 基线提交：`da9918d45841246c56a6ef7ca0fe907c2954ea6e`
> 依据文档：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md`（§1.6、§3 Phase 4、§4）、`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md`（§5.4、§7 S8、§9.2）

---

## 1. 前置门禁

切换前必须全部满足以下前置条件；**新鲜完整备份未通过不得开始任何停写或切换操作，且切换前严禁使用 `--skip-preflight`**（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4；`cmd/urmailctl/upgrade.go:111,932-995`）。

1. **唯一硬门禁：新鲜完整备份 + S3 双落点核验**
   - 核验最新一条 `backup_runs` 为 `state='success'` 且 `scope='local-mail-and-metadata'`，未超新鲜度窗口（默认 24h），且存在有效 `offsite_state='uploaded'` 与 `offsite_uri`（来源：`cmd/urmailctl/upgrade.go:971-995`；`upgrade.sh:153-185`）。
   - 执行只读远端核验（不改本地状态）：
     ```bash
     urmailctl backup verify-offsite --manifest <MANIFEST_FILE> --uri <OFFSITE_URI> --identity <AGE_IDENTITY_FILE>
     ```
     （来源：`cmd/urmailctl/backup_offsite.go:25,241-279`）
   - 禁止传入 `--skip-preflight` 或设置 `URMAIL_SKIP_FRESH_BACKUP_PREFLIGHT=1`（来源：`cmd/urmailctl/upgrade.go:111,125-126`）。

2. **磁盘预算检查（对 56G 空闲空间核算）**
   - 宿主机基线：129G 已用 / 56G 空闲（70%），旧镜像回收后约 46G 空闲（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6）。
   - 预算必须覆盖：`PG × 1.1` + `SQLite × 2` + Dovecot auth 快照 + Docker build cache + 10 份全量备份 + litestream 缓冲（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §7 S8 行；`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4）。
   - `[待实装核定 #1]`：生产切换前在目标宿主机执行 `df -h /srv/urmail /var/backups /var/lib/docker` 与逐项体积核算脚本并签字放行（需 **DevOps** 在隔离演练机与切换窗口前核定具体阈值与命令）。

3. **构建溯源与工作树检查**
   - 拒绝脏工作树与非 release tag HEAD；构建后校验内嵌 tag+SHA 与镜像 digest（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.8；`upgrade.sh:8`）。
   - `[待实装核定 #2]`：目标 release tag 的全栈 Compose 镜像在干净隔离环境 `docker compose build && up -d` 全绿并记录镜像 digest（需 **DevOps** 在 S7.5/Compose 全栈就绪后实跑签字）。

---

## 2. 停写与迁移

执行顺序严格如下（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §7 S8 行；`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4）：

1. **复用 `upgrade fence` + `fresh-backup pre-flight`**：
   - 使用 `urmailctl upgrade fence --reason "<cutover-reason>"` 校验新鲜完整备份并进入停写围栏（来源：`cmd/urmailctl/upgrade.go:48,932-995`；`upgrade.sh:157-185`）。
2. **停写窗口（设计目标：连续停写 ≤60s、单次邮件中断 ≤60s）**：
   - 停止旧栈写者（`urmaild`、Postfix、Dovecot 等），保留 PG 容器 `urmail-m8-cutover-pg` 供迁移与回滚读取（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6、§3 Phase 4；`upgrade.sh:122,201`）。
   - `[待实装核定 #3]`：实测连续停写窗口与单次邮件中断时长是否 ≤60s（需 **DevOps + QA** 在隔离全栈演练时实测计时）。
3. **迁移器五关（PG → SQLite）**：
   - 执行 S5 迁移器五关校验与导入，确认 `PRAGMA foreign_key_check = 0`、`PRAGMA integrity_check = ok`、`PRAGMA user_version = 7`（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §7 S5/S8 行；`internal/sqlitedb/open.go:34,236-328`）。
   - `[待实装核定 #4]`：确认生产切换所用的 `urmailctl` 迁移子命令/参数与全量样本 0-diff 校验入口（需 **Backend + DevOps** 对齐 S5 正式 CLI 入口后在隔离环境实跑核定；当前 `ca7772ca` 中 `cmd/urmailctl/s5_recall_stage.go:76` 仅含 staging 子集）。
4. **切配置与拉起新栈（只读 Canary 阶段）**：
   - 通过 `urmailctl config prepare --first-boot` 渲染离线首启配置（来源：`cmd/urmailctl/config_prepare.go:22-88`；`internal/agentd/prepare.go:41-49`）。
   - `[待实装核定 #5]`：基于 `compose.yaml`（含 SQLite/Litestream/Postfix/Dovecot/Caddy 正式配置而非占位 profile）执行 `docker compose up -d` 并以只读 canary 模式验证 `healthz`（`curl -fsS http://127.0.0.1:8080/api/v1/healthz`）及只读查询（需 **DevOps** 在 Phase 2/3 Compose 全栈打通后核定完整命令）。
5. **只读 Canary 验收通过后放行生产写入**：
   - `[待实装核定 #6]`：解除维护围栏并放行入站/Webmail 写入的精确命令与验收判据（需 **DevOps + QA** 在隔离演练中核定）。

---

## 3. Canary 期入站 MTA 处置（写死口径）

- **默认策略（强制）**：Canary 期间，MX 侧对入站 SMTP 返回 **4xx 暂缓**（远端 MTA 按 SMTP 协议自动重试，实现**零写入、零丢失**）（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §7 S8 行）。
- **备选策略约束**：若采用「旧 PG 栈继续收件」，会引入 PG 侧新写入、直接破坏零丢失语义；**如必须采用，必须在切换单中显式签字弃权，并事先定义该批过渡期信件的人工/增量归属与合流流程**（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §7 S8 行）。
- `[待实装核定 #7]`：Canary 期间 MX/Postfix 4xx 暂缓配置切换与恢复接管的实际命令及验证脚本（需 **DevOps** 在隔离环境实跑验证）。

---

## 4. 零丢失回滚窗口与 14 天保留期口径

- **零丢失回滚窗口 = 首次生产写入放行前**（Canary 为小时级观察窗口）：
  - 窗内（尚无生产写入落入 SQLite）回滚 = 直接切回 PG 配置并拉起旧栈，**零丢失**（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §7 S8 行）。
  - **首次生产写入放行即窗口关闭**：放行后 SQLite 已产生新写入，此后不存在零丢失自动回滚，仅剩人工 break-glass 反向薄通道（SQLite → PG 逆向导出/导入档，**不做 SLA 承诺**）（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §7 S8 行）。
- **14 天 = PG 卷保留期（仅用于磁盘预算与事故取证），不是回滚 SLA**——严禁将 14 天误读为「14 天内可无损回滚」（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §7 S8 行）。

---

## 5. 回滚操作序列与实测口径

- **耗时预期（非秒级）**：QA 实测口径为 **30–60 分钟**（含反向 delta 拷贝 Maildir、排除/重建 Dovecot 索引、停新栈、拉起旧栈）（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4、§4）。
- **硬性约束（违反将导致邮件认证或客户端崩溃）**：
  1. **DKIM 私钥必须原样导入，不得重新生成**（否则 DNS selector 失效）（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4、§4）。
  2. **`dovecot-uidlist` 必须一并拷贝**（否则全部 IMAP 客户端触发全量重同步）（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4、§4）。
  3. **端口 25/80/443 独占与证书双签冲突**：拉起旧栈（systemd + nginx/postfix/dovecot）前必须先完全停止 Compose 新栈释放 25/80/443，且不得同时让 Caddy 与 certbot/nginx 竞争签发证书（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6、§3 Phase 4）。
- **数据库侧序列（按 §9.2 三件套语义）**：
  - 若执行 SQLite 快照恢复或回退，停写 fence 内必须连带处理 `<db>`、`<db>-wal`、`<db>-shm` 三件：`PRAGMA wal_checkpoint(TRUNCATE)` → 确认/清除残留 `-wal`/`-shm` → 校验 `integrity_check=ok`、`user_version=7`、`foreign_key_check=0` → 原子替换（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §9.2；`cmd/urmailctl/restore_database_sqlite.go:27-78`）。
  - 恢复后**强制全量重建 Dovecot auth 快照**（来源：`PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §5.4 第 7 条、§9.2 第 3 条）。
  - `[待实装核定 #8]`：完整 SQLite restore 执行器（`cmd/urmailctl/restore.go:183` 当前对 SQLite fail-closed 拒绝）与 S6 `dovecotauth` 重建命令，以及 30–60 分钟反向回滚全流程演练计时（需 **Backend + DevOps + QA** 在 S6/S7.5 合入后于隔离环境实跑核定）。

---

## 6. 切换后动作（现状处置与遗留清理）

依据 `ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6 现状处置矩阵与 §3 Phase 4：

1. **Maildir 迁移与保留**：
   - 将 `/home/user-data/mail`（约 2.4GB）在 fence 停写后 delta 拷贝对齐至 `/srv/urmail/data/mail`；**旧目录原封保留 14 天**，不得立即删除（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6）。
2. **旧栈停用但保留（可回滚）**：
   - `urmail` deb（`urmaild` / `urmaild-agent` / `urmailctl`）+ 8 个 systemd 单元 + 4 个 timer：执行 `stop + disable`，包与单元保留，purge 延后 14 天（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6）。
   - 容器 PG `urmail-m8-cutover-pg`：切换后立即 `docker stop` 但**不删除**，保留 14 天作为取证/逆向源（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6）。
   - `nginx` / `opendkim` / `opendmarc` / `certbot`：`stop + disable`，观察期后 purge（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6）。
   - `[待实装核定 #9]`：宿主机 8 个 systemd 单元与 4 个 timer 的精确名称清单、`stop`/`disable` 批处理命令及验证结果（需 **DevOps** 在目标宿主机只读核对并签字）。
3. **切换确认成功后清理非 urMail 遗留（回收约 9.4GB 空间）**：
   - 清理崩溃循环中的 portainer 边缘代理、traffmonetizer、6 个退出容器及约 9.4GB 旧构建镜像（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6、§3 Phase 4）。
   - `[待实装核定 #10]`：清理目标容器/镜像 ID 清单与回收后可用磁盘空间实测（需 **DevOps** 在切换成功后核对执行）。
4. **严禁触碰的归档与保留项**：
   - ownCloud 数据、MIAB 遗留（`mailinabox.version`、`settings.yaml` 等）、`/root/upgrade-pre-*`、`/root/urmail-l1-backup` **一律归档不动**（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §1.6）。

---

## 7. 48 小时观察清单

> **计时口径：48 小时自切换成功放行起算；观察期内出现任何 P0/P1 阻断故障或修复重启，48 小时计时归零重新计算**（来源：`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4 DoD）。

| 观察项 | 核验内容与判定口径 | 来源 / 状态 |
|---|---|---|
| Compose 服务与健康探针 | `docker compose ps` 全部核心服务 healthy；`curl -fsS http://127.0.0.1:8080/api/v1/healthz` 返回 200 | `ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4；`install.sh:219,226` |
| SMTP / IMAP 通路 | 25/465/587 入出站投递、993 IMAPS 登录、合成账号收发闭环与线程视图正常 | `ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §3 Phase 4、§5A；`[待实装核定 #11]`（需 **DevOps + QA** 跑通 Compose 协议冒烟） |
| Auth 快照 staleness 与 `snapshot_dirty` | 改密/停用/删域后 `snapshot_dirty` 为 false、mtime 年龄 < 60s（兜底重建周期 24h） | `PHASE1-ISOLATION-DESIGN-20261119-V3.2.md` §5.4 第 6 条；`[待实装核定 #12]`（需 **Backend + DevOps** 待 S6 合入后接入巡检指标） |
| 备份新鲜度与异地同步 | 定时 `local-mail-and-metadata` 完整备份按期成功、`offsite_state='uploaded'`、`verify-offsite` 通过；Litestream 副本无持续落后 | `cmd/urmailctl/backup_offsite.go:244-279`；`compose/litestream.yml:1-9`；`ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §5A |
| Fence 状态 | 无残留活跃升级/恢复围栏（`IsUpgradeHeld == false`），readiness 正常 | `cmd/urmailctl/config_prepare.go:38`；`upgrade.sh:264` |
| 磁盘增长与 WAL 水位 | 数据根与备份目录剩余空间安全、SQLite `-wal` 文件无异常膨胀 | `ARCH-PLAN-SQLITE-COMPOSE-20261119.md` §4、§5A |

---

## 8. `[待实装核定]` 条目汇总

本草稿共标记 **12 项 `[待实装核定]`**，均因 Compose 全栈未完成实装或 S5/S6/S7.5 代码尚未合入，严禁未经实跑直接用于生产：
- `#1` 宿主机 56G 空间逐项体积核算命令与放行阈值（DevOps）
- `#2` release tag 全栈 Compose 镜像构建与 digest 记录（DevOps）
- `#3` 连续停写 ≤60s 与邮件中断 ≤60s 隔离实测计时（DevOps + QA）
- `#4` S5 正式五关迁移器 CLI 命令与 0-diff 校验入口（Backend + DevOps）
- `#5` Compose 全栈 `up -d` 与只读 canary 校验命令（DevOps）
- `#6` 只读 canary 放行生产写入的命令与判据（DevOps + QA）
- `#7` Canary 期 MX/Postfix 4xx 暂缓切换与恢复命令（DevOps）
- `#8` S6/S7.5 SQLite restore 执行器、auth 快照重建及 30–60 分钟回滚计时演练（Backend + DevOps + QA）
- `#9` 宿主机 8 systemd 单元 + 4 timer 精确名称与停用批处理命令（DevOps）
- `#10` 非 urMail 遗留容器/镜像清理 ID 清单与磁盘回收实测（DevOps）
- `#11` Compose 栈 SMTP/IMAPS 协议冒烟脚本与断言（DevOps + QA）
- `#12` S6 auth 快照 staleness 与 `snapshot_dirty` 48h 巡检指标读取命令（Backend + DevOps）
