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

- `compose/caddy/Caddyfile`：静态占位（`:80` 应答文本）；渲染归 Go 配置生成器。
- `compose/postfix/main.cf`、`compose/dovecot/dovecot.conf`：最小占位（loopback/ssl=no，
  安全优先），真实证书/端口/映射由渲染卷替换。
- pg 服务：SQLite Phase 1 接入后退役（§1.1 退役清单），数据卷 `data/db/pg` 一并移除。

## 构建代理（受限网络）

构建需要外网（npm/go 模块/deb 源）。受限环境在 `.env` 设置：
`COMPOSE_BUILD_https_proxy` / `COMPOSE_BUILD_http_proxy`（Go 模块默认走 goproxy.cn，
无需代理）。运行期流量不使用这些代理。

## 构建溯源边界（§1.8 待收口）

构建上下文含真实 `.git` 目录时，前端 stamp 与 Go VCS 元数据为真实 HEAD；
在 git worktree 检出中 `.git` 是指针文件，stamp 回退 `compose-skeleton`、Go 不嵌 VCS——
`upgrade.sh` 门禁（后续）将强制真实 clone + tag 一致。

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
