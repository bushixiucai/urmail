# urMail 安全修复实施与验收结果

日期：2026-10-26（Asia/Shanghai）  
基线提交：`3322bbf26805dcb17f7289e602bebe4d0771b204`  
交付形式：初始本地验收以未提交工作区交付；用户随后授权将全部修复与安全文档提交、合并 main，再执行 CI、生产升级和验收。本文既有本地证据仍按其原时点记录，后续生产结果另行追加。  
状态：SEC-01 至 SEC-09 源码发现全部 **CLOSED**；主分支完整 CI 与本轮受控生产升级验收 **PASS**。第 1–10 节保留初始本地交付时点的证据与边界，最新生产事实见第 11 节；实付款、全机恢复等运营门禁独立保留。

依据：`CODE_SECURITY_REVIEW.md`、`SECURITY_REMEDIATION_PLAN.md`。原始审查报告保留为历史基线；本文记录修复后的代码和实测证据，不改写原审查时点的结论。

## 1. 范围与结论

本轮实施 SEC-01 至 SEC-09 全部源码修复，并覆盖计划中的面板密码现代化、最低密码策略、MFA 状态撤销、CI/发布约束、依赖审计和更新、资源边界、密钥/备份交付流程及本地隔离恢复验证。认证、支付退款、邮件归档由子代理并行实施，主代理统一 SQL 生成、入口接入、前端/契约/双语、验证和证据；随后交叉审查并补修发现的认证竞态。

生产服务、生产数据库、DNS、ACME、真实支付退款和 GitHub 远端 CI 没有操作。完整 systemd PID1 安装及生产部署事实仍单独保留门禁；代码修复闭环不能替代这些验证。

最终源码身份与逐文件摘要见 `docs/reviews/evidence/security-remediation/source-manifest.json`；命令、运行结果及证据范围见同目录 `verification-summary.md`。HEAD 不含本轮修改，必须同时使用该工作区摘要定位代码，不能只引用基线提交作为修复版本。

## 2. 九项修复对照

| 编号 | 状态 | 实际修复 | 主要文件/迁移 | 关键验证 |
|---|---|---|---|---|
| SEC-01 | CLOSED | 新密码至少12个 Unicode 码点、最多256 UTF-8字节；历史验证最多4096字节，绝不截断；共享哈希4并发、取消/饱和拒绝；公开面板/原生登录请求预算；SHA512 rounds上限100000 | `internal/domain/passwordwork/`、`internal/domain/mail/password.go`、全部实际密码入口、`internal/api/router.go` | 超限在算法前拒绝、Unicode边界、容量饱和/取消、未知用户与历史凭据兼容；前端策略回归 |
| SEC-02 | CLOSED | 用户/session/application credential持久代次；改密/MFA在同事务核对当前会话、更新、撤销并审计；登录发放锁用户并复验hash/version；pending MFA绑定代次 | 0080、`queries/auth.sql`、`platform_auth_security.sql`、`platform_auth_application.sql`、`handlers/auth*.go` | 真实PG审计故障回滚、旧会话及迟到证明拒绝、当前会话保留、MFA撤销、真实行锁交错 |
| SEC-03 | CLOSED | 生产主密钥失效在业务启动前拒绝；MFA新写拒绝nil加密器；数据库禁止新增或变动的明文种子；受控历史迁移CLI | 0081、`cmd/urmaild/main.go`、`cmd/urmailctl/secrets.go`、`auth_mfa.go` | 缺失/损坏key拒绝、TOTP批次加密/幂等/审计、错误与回滚、恢复原key解密 |
| SEC-04 | CLOSED | MFA期限/每用户3/全局10000、每分钟清理；vault绑定实际session和版本，每邮箱5/全局1000、每60秒触发PG复验（单轮10秒预算）、失败清理；调度缓存一分钟期限 | `auth_mfa.go`、`internal/webmail/session.go`、mailbox auth/改密、main reaper接入 | 可控时钟、无人访问回收、容量拒绝、邮箱统一失效、终态/缓存及并发测试；无身份标签容量拒绝计数；没有把清理当授权检查 |
| SEC-05 | CLOSED | 未验签正文不进入资金inbox，只写hash/长度/原因/小时聚合计数；全局120次/分钟写预算、10000桶上限、7天保留；显式维护历史正文 | 0082、`internal/payment/webhook_maintenance.go`、`platform_webhook_maintenance.sql`、`payment_registry.go` | 真实PG预算/桶上限/TTL、真实HTTP无效签名、历史清理保留合法与未决资金事实 |
| SEC-06 | CLOSED | approved退款持久退避、公平候选及CAS；核对和权益应用逐项隔离失败；metadata更新不能绕过审批/资金guard | 0083、`internal/refund/service.go`、`platform_refund_execution.sql` | 坏商户不堵正常租户、双worker物理调用一次、权益审计失败隔离、app角色/金额状态变更拒绝 |
| SEC-07 | CLOSED | SMTP大小显式化，v80保留128MiB queue/SIZE合同；归档raw129MiB、流式持久化及校验/fsync/原子发布；MIME提取独立64MiB降级；导出和附件私有快照、审计提交后流式响应、2并发 | `internal/archive/`、`archive_content.go`、`middleware/stream.go`、事务中间件、Postfix生成器及golden | 真实Postfix近上界BCC归档、超限SIZE拒绝；真实PG/HTTP大文件hash；rollback/commit失败/取消/替换/symlink及许可释放 |
| SEC-08 | CLOSED | `net/mail`严格解析单一邮箱，拒绝列表、group、非法结构与不支持的SMTPUTF8 local-part，再按规范域授权；保留既有同域代发策略 | `internal/mail/frombind/`、milter runtime测试 | 隔离587真实SMTP AUTH→milter六项；465/25/profile配置及DKIM链为静态/golden保留，实际完整运行矩阵待验 |
| SEC-09 | CLOSED | 所有模式强制明确loopback IP和1–65535端口；禁止空host、通配、公网及模糊hostname监听 | `internal/config/config.go`、`listener_security_test.go` | IPv4/IPv6/端口/默认值拒绝矩阵；打包二进制实际拒绝公网bind |

## 3. 交叉审查补修

- 原生邮箱登录将验证过的 password hash 传给单独的 verified-session SQL，锁定邮箱行并复验，防止旧密码证明在并发改密后迟到取得新版本有效token；桥接授权保留独立查询。
- `GetLegacySession` 显式要求 `kind='legacy'`，fresh-MFA panel token不能直接充当legacy token；legacy token同样不能进入普通panel API。
- 改密和MFA变更在数据库函数内重新检查当前panel session存在、未撤销/过期、用户及代次一致，覆盖“middleware认证后会话被撤销”的顺序。
- panel/native logout在数据库撤销失败时返回503，不再伪报成功；native先清除本机vault，数据库失败仍向调用者明确报告。
- 通用流式响应的after-commit替换先重置旧响应，再设置新headers，覆盖旧流关闭和Content-Length/type保留。

- 补齐MFA/vault容量拒绝Prometheus计数，均不包含用户/邮箱标签；成功接纳不增计数。vault每60秒触发一轮检查，单轮数据库预算10秒，外部状态变更的后台回收不是严格60秒上界，请求仍实时复核。

以上与原九项根因一起验收，不新增未经验证的高危告警。恢复码在后续步骤失败时可能损失一次已消费码的既有语义仍存在；没有授权绕过证据，不以此宣称恢复码与会话创建具有跨语句原子性。

## 4. 密码与升级兼容

- 面板新写采用Argon2id：32MiB、t=2、p=1、随机16字节salt；解析时只接受受控参数。邮箱继续使用Dovecot兼容SHA512-CRYPT。
- 历史短密码及257–4096字节密码仍可验证；面板成功验证后CAS rehash，纯格式升级不递增认证代次、不误撤销正常会话。真实换密不能被迟到rehash覆盖。
- 历史超过4096字节的密码、显式SHA512 rounds超过100000的摘要需要授权重置；没有扫描、记录或截断密码明文。新建/重置入口及前端策略统一，邮箱管理表单不再trim密码。
- password或MFA代次变化使其他panel session、pending MFA和application credential失效。单独撤销一个panel session不撤销无关application credential，前端与OpenAPI说明已同步。
- 旧应用不识别Argon2id或忽略credential version，不能在新写开启后直接回退。兼容回退必须具备双格式读取和新schema支持；down不能重建已撤销session或恢复已清理正文。
- 两个locale共1398个key，新增/更新文案与错误契约保持key+params模式。

## 5. 数据库与维护命令

新增0080–0083四组up/down，未改写已有迁移；sqlc v1.30.0重生成可重复，75个store Go文件再次生成前后摘要相同。

```text
urmailctl secrets migrate-totp
urmailctl secrets migrate-totp --apply --batch-size 100
urmailctl secrets compact-rejected-webhooks
urmailctl secrets compact-rejected-webhooks --apply
```

均为root运维命令，默认仅数量dry-run，apply每次有限批次；不会打印secret/原文。历史数据清理不是DDL迁移中的全表重写。旧明文备份不会因线上加密自动消除；DELETE也不保证PG/WAL磁盘即时回收。

运行时只有到期诊断元数据的有限清理自动启动；历史正文清理需要显式命令。quarantine下不启动修改型支付维护。正常TOTP历史读取兼容与新增明文写拒绝分开处理。

## 6. 依赖与发布加固

完整扫描发现原定向审查未覆盖的命中，已更新：`golang.org/x/net 0.59.0→0.60.0`、`quic-go 0.59.0→0.59.1`、`zod 3.25.76→4.6.7`、`vitest 3.2.7→5.0.5`、`@playwright/test 1.55.0→1.64.1`及相应锁定传递依赖。

- npm生产/开发完整锁文件审计结果为0命中，前端127项测试、类型及lint通过；开发工具公告不被描述为已证实的生产RCE。
- 原govulncheck v1.1.4的解析依赖不支持当前Go，失败记录保留。改用Go1.27.3构建的govulncheck v1.9.0，并在CI固定该版本。
- 修复后Go可达符号/导入包命中为0。保留一个module-only公告GO-2026-5932，针对未维护的OpenPGP包；本项目不导入这些包，不能误报成实际可达漏洞，也不能省略该结果。
- 通用CI actions固定不可变SHA并设置只读默认权限；只有发布job具有必要写权限。初始本地实现以 main push 为假设；后续按用户的 tag-only 策略修正为 main 历史可达、精确 SHA 的 main 手动 CI 或有效 v* tag CI，错误 SHA、外来来源、遗漏、跳过或失败均拒绝。远端执行结果见第 11 节。
- 签名、APT补丁、密钥轮换、备份和资源预算的具体操作边界见 `SECURITY_OPERATIONS_RUNBOOK.md`。没有凭未经测量的数值给生产强加低MemoryMax，也没有实现/宣称无停机多key自动轮换。

## 7. 实测范围

### 7.1 本地与隔离环境

最终命令结果以 `verification-summary.md` 为准。容量计数补强后再次完成全仓库真实PG race、构建、lint、vet及SCA；最后新增的中途断流测试另以真实PG、race、count=1通过。1MiB独占tmpfs真实ENOSPC测试非skip通过；最终邮件runtime四阶段和六项From身份用例再次通过。本轮执行构建、lint、i18n、sqlc确定性、真实本地PG全量race、补充故障/并发/大小回归、前端、浏览器、OpenAPI、Go/npm依赖扫描和本地Postman支付契约模拟。

真实邮件runtime为arm64、Docker network=none、无宿主发布端口，临时容器已清理。Postfix3.6.4、Dovecot2.3.16、PG14.24测试组合完成IMAP/SMTP/LMTP、PG/HTTP定时发送、不确定状态/终态重放、附件/Bcc、From及大归档验证。近上界样本：逻辑正文133806373、wire正文133940582、durable raw133806756字节，完整hash一致。声明SIZE134217729在正文前552拒绝；128MiB指Postfix SIZE/queue-file预算，不能解读为任意行布局的128MiB逻辑正文均可提交。

归档HTTP补强回归串行通过16/64/128MiB的生产Spool、真实PG索引、grant/审计/事务及真实HTTP客户端流式hash。大原件提取可明确降级，完整raw仍保留和可导出。

成功READ审计证明授权及事务提交，不证明客户端完整接收。正文已开始后中断时HTTP可能已经为200，另记stream_failed/502审计、失败计数和warning；独立补充审计采用2秒best-effort预算，数据库故障时依靠counter/log，真实告警送达未验。导出2个进程内槽和129MiB单文件边界限制临时资源；附件准备可同时持有raw/decoded快照，保守峰值约516MiB，不是预留空间或全机磁盘配额。磁盘不足必须拒绝准备/接收，不能确认完整。

本地Postman simulation调用真实Stripe HTTP客户端，验证正常退款响应、JSON503、HTML503；仅使用loopback与测试凭据，没有真实支付。模拟器注入失败可能不产生member request事件，必须结合客户端对实际HTTP503的断言，不能把静默当成调用已发生。

### 7.2 安装包与恢复

Ubuntu22.04/24.04 arm64独占network-none容器完成：本轮源码快照Linux四二进制/deb构建、签名下载9路径、真实解包/前置权限、新装schema83、83→79→历史数据→83、packaged TOTP迁移/幂等、真实mailinabox用户HTTP登录、缺key/公网bind拒绝、typed agent和nobody socket拒绝。

独占Ubuntu22/PG14进一步完成4类合法enc:v1（TOTP、Stripe merchant、DNS、AI）真实私有0600 pg_dump→第二库pg_restore；原key4/4解密一致、错key4/4拒绝，实际payment.Registry.Load验证，并调用HoldRestoredSideEffects将待发email置uncertain。仅输出计数，不包含密钥或密文；这不是全机恢复演练。

该包构建早于最后MFA/vault纯计数器补强和部分测试；最终差异有独立清单，不能把该包称作包含最终所有文件。修复逻辑与安装边界分别有对应回归，交付工作区通过最终构建；正式发布必须对最终提交重新构建和执行远端门禁。

**完整dpkg configure/systemd安装为BLOCKED：** 当前容器不能以systemd PID1运行。没有伪造systemctl、使用force-depends或放宽namespace来包装通过。已测层与未测层分别保留，详见 `package-validation/` receipt与说明。原生amd64运行矩阵、公网ACME、实付款、生产防火墙/文件ACL、备份介质/RPO/RTO和真实告警送达均未扩展授权。

## 8. 中间失败与证据纪律

- 并行实施期间sqlc接口暂未生成，首次全量在middleware编译失败；修复生成顺序后重新执行冻结源码完整验收，原日志保留。
- 第一版大邮件样本忽略DotWriter的CRLF和Postfix queue-record开销，超限被552正确拒绝。随后fixture的pipe使用Postfix owner、命令尾`$`被当宏，又导致归档未投递；均修复测试夹具后重跑，没有降低128MiB配置或改业务pipe为高权限用户。
- 中间lint格式/注释、OpenAPI YAML语法和旧扫描器失败已保留；证据helper改存为`.go.txt`，避免审查资料被Go工具当业务包编译，最终通过结果单独记录。没有continue-on-error掩盖测试失败。
- 初始本地验收时源码未提交，来源清单包含当时未跟踪的新业务/测试文件；后续已纳入 Git 并合并主分支。原有 `.impeccable/`、`scripts/w13/__pycache__/` 等无关内容始终保留。

## 9. 最终就绪判断

本地源码修复与环境验证分开判断。九项发现的实现、回归和跨组件复核均通过，已逐项关闭；生产发布仍为 **CONDITIONAL**，需要完整systemd安装/运行矩阵和上述授权环境验证。静态审查、隔离真实邮件、数据库恢复和本地mock证据都不等于全面生产安全验证。


## 10. 完成审计与保留门禁

| 计划交付 | 最终状态 |
|---|---|
| A/B：密码、认证代次、密钥、临时状态、loopback | 已实现、真实PG/并发/拒绝/容量指标回归通过 |
| C：拒绝回调及公平退款 | 已实现、预算/TTL/幂等/公平与资金guard回归通过；真实支付未执行 |
| D：邮件From及归档 | 已实现、真实587/milter及近上界归档、ENOSPC、分层HTTP、断流/审计/许可释放通过 |
| E：Argon2id/双语/契约/依赖/CI发布约束 | 已实现、本地检查通过；远端CI及仓库保护配置未执行/核实 |
| E：轮换、备份、补丁、资源与反垃圾交付 | 运维流程和能力边界已交付；四类密文恢复通过；生产轮换工具、实际系统补丁/容量配置、真实告警及全机恢复仍按环境门禁处理 |
| F：统一验收与交叉审查 | 最终构建、全量race、lint/vet、前端/浏览器、sqlc、SCA及真实隔离邮件通过；本地必要项无待执行任务 |
| 完整systemd新装/升级与原生amd64 | 未完成环境验证；容器PID1限制已记录，禁止以局部安装证据代替 |
| 全profile邮件运行矩阵、生产ACME/DNS/防火墙/ACL、实付款、RPO/RTO及全机恢复 | 未验证，不属于本轮已授权的生产操作，仍阻止直接宣称可上线 |

不将一般运维流程的交付描述为生产配置已经生效。源代码发现CLOSED只适用于本文绑定的工作区；正式上线必须使用最终提交的新构建、签名和对应CI/环境验收。


## 11. 主分支合并、完整 CI 与受控生产验收

### 11.1 源码与文档已保存并合并

用户明确授权包含全部未提交业务更新，并要求确认安全修复和文档存在后合并主分支，再执行更新流程。

- `355779787a7c7c67015ac4d7e56b1eefb33048e6` 纳入 SEC-01–09 全部源码、迁移 80–83、测试、四份安全文档和验证索引/来源清单，共 131 个文件；从 `ci/tag-only-trigger` fast-forward 合并到 main 并推送。
- 真实安装 CI 发现 bootstrap 迁移命令未传生产必需的 policy 开关，追加 `10866a39d095f6c39e7ad703b13a6eb885029da5`，仅补 `URMAIL_POLICY_ENABLED=true`，保留生产拒绝缺失安全配置的校验。
- CI 保留仅 v* tag 自动触发，使用 main 的 workflow_dispatch 完成此次验证；没有创建发行 tag、触发 Package 或发布 release。
- 本次生产构建绑定 `10866a39d095f6c39e7ad703b13a6eb885029da5` 的精确 Git archive，版本 `0.4.1-rc.2+security.10866a39`。最终补充文档提交不改变该二进制身份。

### 11.2 完整远端 CI

[CI 40323412662](https://github.com/bushixiucai/urmail/actions/runs/40323412662) 为 main/workflow_dispatch，head SHA 为上述生产构建提交，**12/12 jobs success**，没有 failure/cancelled step：Go、Frontend、PostgreSQL integration、真实 Postfix/Dovecot、embedded frontend、package smoke、Shell、开发栈浏览器、Security dependencies，以及 Ubuntu 22.04 amd64、24.04 amd64、24.04 arm64 三个完整安装任务。

发布检查另外实际读取 GitHub，验证精确 SHA、同仓库 main 来源、main 历史可达与全部必要 job 成功，结果 PASS。旧 `35577978` 安装失败及日志保留；没有把修复前 CI 当作成功。

冻结 `35577978` 的隔离真实 PG/HTTP/Postfix/Dovecot/milter/archive 专项再次通过，包含 16/64/128 MiB HTTP、事务/审计断流及独占 1 MiB tmpfs 的真实 ENOSPC。新 `10866a39` 与该冻结候选的 880 个 runtime 输入全部相同，整仓仅 installer 一行不同；保留逐文件比对收据。新 SHA 的完整 CI 仍独立执行，不以旧收据替代。

### 11.3 生产部署与备份

服务器私有工作区：`/root/urmail-m8-staging/20261026-security-10866a39`。更新了四个实际运行文件：`/usr/bin/urmaild`、`/usr/bin/urmaild-agent`、`/usr/bin/urmailctl`、`/usr/lib/urmail/archive-pipe`。归档 pipe 此前未安装，本轮按标准路径补齐；没有为验收全局启用生产归档。

正式停写前先在网络隔离、无宿主端口的 PostgreSQL 16 容器恢复真实数据库副本，验证 schema 79→83、原有邮箱和面板凭据摘要、初始认证代次，以及临时面板主体创建/清理。正式停写备份后又独立重复恢复与迁移验证，随后才迁移生产与替换二进制。没有运行新装安装器，没有生成或替换 master key。

`rollback/` 保存数据库、配置/原 key/证书/系统单元、Maildir/Sieve/归档/Postfix spool 和旧三个二进制；清单明确记录第四个旧文件原本不存在。

| 备份 | 字节数 | SHA-256 |
| --- | ---: | --- |
| database.dump | 698809 | `d8db2b3d963e4606ffd4445a456810bd9985a1426da241fd160c8a0cd42e1b8d` |
| config.tar.gz | 263197 | `c918fe222a128fcfc71dfab216b724b0c9bffd6f27ac384a3994bbff6282723c` |
| mail-and-spool.tar | 2424576000 | `e56d9f9e6c5f4f072fbe9edf185033a927930f7b9e909818265ee70d02c3ed22` |

发布协调曾因主动停止 nginx 而触发 agent 的 nginx 探针及遗留降级状态；原始失败日志保留。恢复入口后，通过重启 agent 并执行真实 config.apply 与完整探针恢复状态，没有手改状态文件或恢复旧数据库/旧二进制。最终 desired/applied 均为 `1db0d932ca60aad9ee7bccd7685c2ae40491347fb89b4412050009608d2329ae`，ready=true、degraded=false；schema 83、dirty=false；urmaild、agent、Postfix、Dovecot、nginx 全部 active，公开 HTTPS 显示新版本。

### 11.4 生产功能与安全验收

**临时面板主体 11 项检查 PASS**：独立临时 tenant/user、legacy SHA512 首次登录自动转 Argon2id 且代次不误增长；两会话改密保留当前并撤销其他；旧密码拒绝；MFA setup/enable 加密与代次变化；真实 TOTP 登录；普通用户创建特权应用凭据 403；MFA disable 撤销其他会话及旧 challenge；注销；停用主体、关闭租户和移除私有凭据。

失效 challenge 按实现返回 HTTP 400/MFA_TOKEN_INVALID。首轮脚本误期望 401，已依接口定义修正并完整重跑；两轮主体均已清理，最终保留两个 disabled user/closed tenant、零有效会话和应用凭据，没有增加平台管理员。没有访问原有用户邮箱内容或更改原用户密码；没有冒充覆盖管理员凭据发放的生产验收。

**操作员电脑经公网执行 16 项邮件检查 PASS**：465/587 严格 TLS；外域、缺失、重复、list/group/trailing From 拒绝；quoted display name 内含冒号/@ 的合法地址通过；同连接连续合法发送及拒绝后恢复；25 在 TLS 前后均不提供 AUTH；2 MiB 草稿替换与原生附件；10 MiB SMTP 附件实际收取/下载哈希一致；Bcc/HTML；定时创建幂等、跨邮箱拒绝、重复取消、单次投递、终态重放与冲突；改密撤销旧密码/会话；测试邮件与会话清理。

客户端使用 certifi 公信 CA，保持证书和主机名校验。收件检查包含 Spam，保留现有反垃圾分拣事实；未绕过规则或把 SMTP 接受等同于 INBOX。

**实际安装 amd64 archive-pipe ELF 隔离执行 PASS**：以真实 mailinabox UID/GID，在 network=none、无端口、只读 root、cap-drop ALL、独立 tmpfs 容器运行，唯一宿主绑定为只读产品 ELF。流式处理 68,161,536 字节合成邮件并核对 raw/manifest 完整性；持久化后因刻意缺失数据库配置退出 75，没有虚假投递成功。未连接生产数据库或 live spool，容器已删除。完整 SMTP→pipe→PG 和大文件 HTTP 由另列的真实隔离 runtime 证明；不将这些结果表述为已启用生产全局归档。

### 11.5 最终数据核对与边界

- 原有 **55 条邮箱记录**的身份、租户、域名、密码摘要、配额、状态与发件权限摘要一致；active 仍为 **43**。最终总记录 57、deleted 14，增加的两条测试邮箱已软删除，消息清除、会话撤销。
- 原有面板账号凭据摘要一致；两轮测试 user/tenant 均已停用/关闭，测试密码、TOTP 和 recovery code 已从私有 JSON 清除。
- 测试定时任务 13/14 分别 sent/cancelled，secret/lease 清空且无待运行测试任务；原有任务的归属、状态和 token 摘要一致，原有资金记录计数一致。
- master key 内容摘要与 nginx、NSD、OpenDKIM、证书目录摘要保持一致；原 Postfix 全局策略与 v80 的 128 MiB 大小配置保持；用量 timer active/enabled。全部备份与四个安装文件的摘要最终再次校验。
- 未发起真实支付/退款，未执行整机生产切回、在线全字段 key 轮换或全局归档启用。既有 DNS/ACME 交接、防火墙/告警、离机备份和 RPO/RTO 等运营边界不因本次通过而自动关闭。
- 生产已可能产生 Argon2id/新认证代次及新业务写入。旧 beed798f 不支持这些安全事实，禁止直接换旧二进制或覆盖旧数据库/Maildir/队列；故障应优先向前修复，数据恢复需独立停写并核对增量。

四个安装文件 SHA-256：

- urmaild：`88fd6eda662a19b37995aedd956572ec3f6aab596a8ff6bf5696e274c7c06721`
- urmaild-agent：`bcf9022778c61742fad0b7ec210c45a0bc5729d228678f02534d253bf502deb7`
- urmailctl：`b632359aff855d0cbe374e90a87061e7a711de39b49f4311555bff8814da38c0`
- urmail-archive：`146f51fbf2f690e9b8ae4bc8067f8351194dedbee78a0099812cec5a9cd81f4c`

本地证据 `tmp/security-deploy-2026-10-26/` 保存精确 archive、manifest、完整 CI/gate、runtime 输入比对、发布尝试/恢复、面板与公网脚本/日志以及最终 receipts；关键证据在 `release-final/final-verification.json`、`deployment-receipt.json`、`panel-smoke.json`、`public-probe/production-smoke.json`、`installed-pipe-isolated-receipt.json`。本机临时邮箱明文凭据已移除；本次数据库备份与原 key 的备份副本只留服务器 root 私有目录。
