# urMail compose 骨架（Phase 2 · fix/compose-skeleton）

目标（ARCH-PLAN-SQLITE-COMPOSE-20261119 §1）：**任何满足前置条件的 Linux 机器：
装 Docker → checkout 本仓库 → `docker compose build && docker compose up -d` → 完整邮件系统运行。**
本分支是骨架证明（组网可行 + healthz 200），不是生产切换。

## 前置条件（§1 原文口径）

Linux x86_64/arm64、Docker 24+ 与 **docker compose v2.20+**（depends_on `required:false` 需要）、
端口 25/80/443/465/587/993 空闲、DNS 已指向本机、rDNS 已设（healthz 验证采用
`compose exec` 容器内口径；收发端口归配置生成器阶段）。

## 快速开始

```sh
cp .env.example .env                 # 修改 URMAIL_MYHOSTNAME/URMAIL_PRIMARY_DOMAIN 与 URMAIL_PG_PASSWORD
for d in db mail certs config backup; do
  install -d -o 5000 -g 5000 -m 0750 "${URMAIL_DATA_ROOT:-data}/$d"
done
# ↑ 用 install -d 建目录并属主 5000（QA 终判：chown -R 对不存在目录会失败，
#   忽略后 Docker 以 root 建 bind 源目录 → uid5000 写不进去；for 循环保证
#   任意 POSIX shell 可直接粘贴执行）
docker compose build                 # 多阶段构建（web -> go -> debian 运行时）
docker compose up -d                 # pg + urmaild + postfix + dovecot + caddy + prom/alert
docker compose exec urmaild curl -fsS http://127.0.0.1:8080/api/v1/healthz
# 期望 {"status":"ok",...}（现行 config 强制 HTTP 回环绑定，故用 exec 口径而非宿主端口）
```

服务明细见 `compose.yaml` 注释；数据布局见 §1.2：`${URMAIL_DATA_ROOT}` 单根下
`db/ mail/ certs/ config/ backup/`，迁移=拷目录。

## profiles（已定义、延后启用）

| 服务 | profile | 延后原因 |
|---|---|---|
| urmail-signer / urmail-spam | `milter` | 需 `urmail-milter` 二进制（Phase 2 后续拆分；当前 spam milter 内嵌在 urmaild 进程） |
| urmail-agent | `agent` | 需 agent 配置生成器（渲染 postfix/dovecot/caddy 配置，Backend 负责） |
| litestream | `sqlite` | 需 SQLite 数据层（Phase 1；本轮 pg 容器占位） |

启用示例：`docker compose --profile milter --profile agent --profile sqlite up -d`。

## 占位件（Backend 接手后替换）

- 占位件说明：
  - `compose/caddy/Caddyfile`：静态占位（`:80` 应答文本）；渲染归 Go 配置生成器。
  - `compose/postfix/main.cf`、`compose/dovecot/dovecot.conf`：最小占位（loopback/ssl=no，安全优先），真实证书/端口/映射由渲染卷替换。
  - `pg` 容器已按 §1.1「退役」清单移除，默认数据层已切至 SQLite（`URMAIL_DB_ENGINE=sqlite`、`URMAIL_SQLITE_PATH=/srv/urmail/data/db/urmail.sqlite`）；`litestream` 已从 `profiles: ["sqlite"]` 转正为默认启用服务。

## Phase 1 SQLite / Litestream / Canary 新增变量（DRAFT）

| 变量名 | 默认值 | 说明 |
|---|---|---|
| `DB_ENGINE` / `URMAIL_DB_ENGINE` | `sqlite` | 显式启用 SQLite 单库引擎 |
| `URMAIL_SQLITE_PATH` | `/srv/urmail/data/db/urmail.sqlite` | SQLite 平台库路径（位于 `data/db` 挂载卷） |
| `LITESTREAM_S3_BUCKET` | 空 | Litestream 异地 S3 兼容桶名（凭据通过环境变量注入，不写仓库） |
| `LITESTREAM_S3_REGION` | 空 | Litestream S3 区域 |
| `LITESTREAM_S3_ENDPOINT` | 空 | Litestream S3 兼容端点 |
| `LITESTREAM_S3_PATH` | `urmail-db` | Litestream S3 对象前缀 |
| `URMAIL_RENDERED_CONFIG` | `0` | 设为 `1` 时，`postfix`/`dovecot` entrypoint 对 `/srv/urmail/data/config/<svc>` 渲染产物做非空 fail-closed 校验（`DEPENDS-GENERATOR`） |
| `URMAIL_CANARY_INBOUND_DEFER` | `0` | Canary 期入站 4xx 暂缓单一开关（默认关闭）；开启后若缺少 `canary-defer.cf` 拒绝启动（fail-closed） |

与 `compose.test.local.yaml` 差异：`compose.test.local.yaml` 仅用于本机非特权端口映射测试（`2025/2465/2587/8080/8443/1993/1143`、`litestream-local.yml` 与 `urmail-signer-test`），不包含生产 `host` 网络与 S3 异地参数。

## 构建代理（受限网络）

构建需要外网（npm/go 模块/deb 源）。受限环境在 `.env` 设置：
`COMPOSE_BUILD_https_proxy` / `COMPOSE_BUILD_http_proxy`（Go 模块默认走 goproxy.cn，
无需代理）。运行期流量不使用这些代理。

## 构建溯源边界（§1.8 待收口）

构建上下文含真实 `.git` 目录时，前端 stamp 与 Go VCS 元数据为真实 HEAD；
在 git worktree 检出中 `.git` 是指针文件，stamp 回退 `compose-skeleton`、Go 不嵌 VCS——
`upgrade.sh` 门禁（后续）将强制真实 clone + tag 一致。

## install.sh / upgrade.sh（§1.3–§1.4，骨架）

> **警示（QA 终判 + PM C-B 修订 2026-11-20）**：compose 升级链在 readiness receipt
> 方案落地前**不具备生产升级能力**——原生 compose fence 不产生 urmailctl v2
> hold+receipt，readiness-record 默认 fail-closed；且 C-B 修订要求的
> P1-a DB 单写者探针、P1-b 主机侧运行集白名单 + `--writers-stopped` 全等断言、
> P2-a hold 单一事实源、P2-b witness 审计均未实现。本轮 upgrade.sh 为骨架
> （原生 compose fence：hold 标记在数据根 + 停写者容器）。

```sh
./install.sh --domain mail.example.test --primary-domain example.test \
             --admin-email admin@example.test [--data-root /srv/urmail/data] [--dry-run]
./upgrade.sh v0.6.27 [--dry-run]
```

- `install.sh`：前置检查（Linux/Docker≥24/Compose≥v2.20/端口空闲/rDNS 提示）→
  `.env` 生成（0600，密钥只落文件不进日志）→ 数据目录 `install -d` 5000:5000 0750 →
  `compose build` → `up -d` → 等健康 → 打印管理员引导与 DNS 清单。
- `upgrade.sh vX`：干净树强制（dirty 即拒）→ **原生 compose fence**（hold 标记写数据根 +
  `compose stop urmaild postfix dovecot`，pg 保留；不用宿主 systemd/urmailctl）→
  新鲜备份 pre-flight 用一次性 urmaild 容器跑（复用二进制内建门禁，拒绝即迁移前拦住）→
  checkout tag → `compose build` → `URMAIL_AUTO_MIGRATE=0 up -d` →
  fence 内一次性容器 `urmailctl migrate up` 受控迁移 → 就绪探针（healthz）→
  `readiness-record` → 启动写者 → 清 hold；fence 后任何失败按阶段处置：
  `up -d` 之前失败=重启写者+清 hold（旧栈继续服务），`up -d` 之后失败=
  **hold 保持不回滚**并打印回滚 runbook（绝不自动 release 未验证新栈）。
- `URMAIL_AUTO_MIGRATE` 环境变量控制 urmaild 首启是否自动迁移
  （默认 1=首次引导；upgrade.sh 走 0+受控迁移）。
- `URMAIL_WRITERS`（默认 "postfix dovecot"）是 fence 停止/恢复的写者服务清单。
- `holds/`（数据根下专设子目录，QA 复审 CONDITION）：upgrade fence 的 hold 标记
  只放这里；postfix/dovecot 的 entrypoint **仅 ro 挂载该子目录**检查 hold——
  绝不挂载整个数据根（master.key 与 db/ 不进 MTA 容器，避免权限暴露面）。
  urmaild 特例：`URMAIL_HOLD_GUARD=0`（hold 期间探针与 readiness-record 需要
  它在位）；手工 `docker compose start` 无法绕过 hold 门。
- **pre-flight 文案契约**：upgrade.sh 以 urmailctl 输出中的固定字样判定门禁结果，
  这些字样是版本握手契约（要求 urmailctl ≥ v0.6.26 R1；措辞变更须同步 upgrade.sh）：
  - 放行判定（任一即视为通过）：`newest complete successful run` /
    `not provisioned on this installation; proceeding`
  - 拒绝判定（中止，原文进 evidence）：`refusing to fence.*fresh-backup pre-flight` /
    `preflight degraded`（**C2 fail-closed，2026-11-20 起**：preflightFreshBackup
    四条降级路径——config.Load 失败/平台 DSN 空/open 失败/读失败——不得放行；
    SQLite 切换撤掉 PG URL 后每次升级都必然降级，放行即门禁失效）
  - 无法解析判定：中止（宁可误停不误放）。
- 两者均支持 `--dry-run`（只读检查+打印计划，不写任何状态），需通过 shellcheck；
  升级链的 fence/release 已不再依赖宿主 systemd urmailctl（QA 终判口径），
  但**在 compose 形态完成 Phase 3 验收前不可用于生产**。

## CI 验收套件（停止迭代，不可作门禁）

`scripts/upgrade-rig-acceptance.sh` 及其场景 A–E **不可作为升级链门禁**
（PM 决定 2026-11-20：停止迭代、不再改/重跑）。根因：

1. **假绿**：mock/桩路径与真实 urmailctl 语义不一致（如 R1 mock_row 让
   preflight 通过而 readiness-record 走 recordless 旁路），套件通过≠链路正确；
2. **种子静默失败**：fixture 种子失败不被断言捕获，场景可能在空数据上"通过"；
3. **证据覆盖不全**：部分失败路径不落 evidence，无法事后审计。

待 Phase 3 隔离环境落地并修复上述三类问题后方可回归门禁。

**`URMAIL_ALLOW_RECORDLESS_RIG=1` 仅限 rig/验收环境**（无 v2 receipt 的场景 D/D2），
**禁止在生产设置**：生产路径 readiness-record 必须 fail-closed，
recordless 旁路等于跳过升级凭证落盘，未验证新栈即放行写者。

## 与 §1.1 规格差异（本轮口径）

- urmaild 的 spam/frombind milter 当前内嵌进程内（127.0.0.1:12302 等），未拆独立服务；
- postfix/dovecot/caddy 以同一 urmail 镜像的不同 command 运行（§5 倾向口径），
  后续可换官方镜像（仅改 image 行）；
- 数据卷挂点已按 §1.2 预留，placeholder 配置的替换不改挂载点。

## 运行口径（QA P2，升级 runbook 落地时逐条收口）

- **发布路径必须关闭自动迁移**：`upgrade.sh` 在 `compose up` 前设
  `URMAIL_AUTO_MIGRATE=0`，迁移仅允许在升级 fence 内经 `urmailctl migrate` 受控执行；
  首次引导（全新安装）才使用默认 `URMAIL_AUTO_MIGRATE=1`。
- **禁止多副本**：全部服务单副本（SQLite 单写者 + Maildir 单挂载 +
  host 网络端口独占），`docker compose up --scale` 不得使用。
- **milter profile 启用时**，postfix 对 urmail-signer/urmail-spam 的
  `depends_on (required=false)` 变为硬依赖（此时才允许把 milter 服务写进
  postfix 的 smtpd_milters 渲染配置）。
- **caddy host 网络 80/443 独占**：与现网 nginx（MIAB 遗留栈）切换时必须先
  `stop+disable` nginx（写进 S8/切换 runbook；端口冲突会导致 caddy 反复重启）。
- prometheus/alertmanager 使用官方镜像 `prom/prometheus` / `prom/alertmanager`。

## 首次启动：`.env` 与集群 bootstrap

compose 形态不是“起容器就完事”：PostgreSQL 首次初始化依赖集群角色脚本，迁移 0001
在角色缺失时会直接失败（`schema_migrations` 记 `1|dirty`）。

1. **`.env`**：`cp .env.example .env` 后至少替换 `URMAIL_PG_PASSWORD`（`openssl rand -hex 24`），
   并按部署调整 `URMAIL_DATA_ROOT` / `URMAIL_MYHOSTNAME` / `URMAIL_PRIMARY_DOMAIN`；
   `.env` 权限 0600，不得入库。缺 `URMAIL_PG_PASSWORD` 时 compose 会以
   `required variable URMAIL_PG_PASSWORD is missing a value` 拒绝插值，`upgrade.sh` 的 preflight
   随之报 `pre-flight verdict unreadable` 并中止（fail-closed）。
2. **bootstrap.sql**：`compose.yaml` 把它挂到 `/docker-entrypoint-initdb.d/01-bootstrap.sql`，
   **仅在新数据目录的首次初始化时执行**。PostgreSQL 容器以 uid 70 运行，宿主文件必须可读：
   检出 umask 为 077 时 `internal/db/bootstrap.sql` 会变成 0600，初始化会失败并以
   `psql: … Permission denied` → `urMail cluster roles are missing` 告警。构建镜像时已用
   `COPY --chmod=0644` 固定镜像内副本；**bind mount 的宿主文件仍须 `chmod 0644`**（或把仓库检出
   umask 调至 022）。
3. **健康检查**：`URMAIL_LISTEN_ADDR` 只接受回环地址，urmaild 不发布宿主端口。探活在容器内：
   `docker compose exec -T urmaild curl -fsS http://127.0.0.1:8080/healthz`；
   宿主 `127.0.0.1:8080` 属 caddy，不是 urmaild。
4. bootstrap 是一次性的：角色已存在时重复执行是幂等的；若 `schema_migrations` 已变 `dirty`，
   先诊断失败迁移，不要直接删表重来。
