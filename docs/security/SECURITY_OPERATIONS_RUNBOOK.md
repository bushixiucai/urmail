# urMail 安全交付与运维验证手册

本文件配合安全修复代码使用。以下生产动作仅在相应环境获得明确授权后执行；本轮本地实施不执行这些动作，也不把清单当作现场已通过。

## 1. 发布和回退

- 发布候选绑定完整 Git SHA、go.sum/package-lock 摘要、schema 版本、四个二进制/deb 摘要及签名 manifest。相同 SHA 必须已位于 main 历史，并由 main 的手动 CI 或仍指向该 SHA 的 v* tag push CI 全部成功验证，包含 Security dependencies 和三个 Linux 安装任务；缺失、跳过、失败、取消或其他 SHA 一律拒绝发布。
- CI 仅 v* tag 自动触发，保留 workflow_dispatch；分支 push 不自动触发。CI action 固定 SHA，workflow 默认只读权限；仅发布 job 具备 contents write，签名密钥仅在签名 step 可见。维护签名 key 的人员权限、二次审批和 branch/tag protection 仍需仓库管理员核实。
- 面板密码开始写 Argon2id 后，旧版不支持该格式的二进制不能直接回退。凭据版本/撤销 schema 启用后，旧版忽略版本的应用不能继续运行。保留支持双格式读取和新 schema 的兼容回退包；否则停止写入后按受控恢复计划处理。
- down 迁移仅是结构回退工具，不能恢复已经删除的 session、被清理的未验签正文或撤销事实；不能通过 down 将 uncertain/held 的资金或邮件任务重新激活。先备份最新状态并评估发布后的业务写入。

- 对保留外部 nginx 的受控升级，执行 agent config.apply 前必须确保 nginx 已 active；该服务仍属于就绪探针。维护期间应优先停应用写入并保留入口进程，或在应用配置检查前恢复 nginx。探针失败后的降级须经真实重新应用与完整探针核对，禁止手改状态标记。

## 2. 主密钥和 TOTP 历史数据

- 生产 urmaild 缺少有效原主密钥时拒绝启动。先恢复与数据库密文匹配的原 key，核对 owner/mode；禁止生成新 key 让服务表面启动。
- 检查 `/etc/urmail/master.key` 与运行环境配置只对 root/mailinabox 需要的主体开放；agent.env 为 root 0600；证书 key 0600。验证实际 ACL/符号链接/备份访问权限，不能仅阅读安装模板。
- `urmailctl secrets migrate-totp` 只报告待迁移数量；`urmailctl secrets migrate-totp --apply --batch-size 100` 每次加密一批并记录审计，重复至 remaining=0。整个过程不得打印或导出种子。坏种子使批次回滚，需要授权人员独立核对账号，不能盲删 MFA。
- 历史种子格式转换可能使相关认证代次变化并要求重新登录，安排维护通知。明文旧数据库备份不会随线上转换变成密文；按保留政策加密或安全销毁旧介质。

## 3. 加密主密钥轮换

当前 enc:v1 密文依赖单个主密钥，不能在线直接替换 key 文件。本轮修复不把该格式宣传为支持自动在线多 key 轮换。采用以下可恢复的停写流程：

1. 清点所有 enc:v1 资源：TOTP、DNS 凭据、支付商户配置、AI/通知/营销凭据、定时邮件 secret，以及配置文件中的静态 token。为每项记录表/字段和版本，不输出值。
2. 先保留加密离机备份：数据库、原 key、配置、证书、Maildir/归档/队列和 Sieve source。记录相互绑定的快照标识与摘要，密钥副本与业务归档分开授权保管。
3. 在独立恢复环境同时保管 old/new 两个只读 key 文件。由经过审核的重加密工具逐项用 old 解密、new 加密，以原 ciphertext/版本 CAS 防止覆盖变更；不把 plaintext 写入文件、argv 或日志。未准备并验证该专用工具前，不执行生产轮换。
4. 验证所有旧资源可读、使用新 key 的读取成功、错误 key 拒绝、持久任务不重放、支付/DNS/AI 的引用关系保留；检查零遗漏，形成 counts-only receipt。任一步失败恢复整组旧快照与 old key。
5. 授权停写所有相关 worker、应用及运维写入后执行已验收流程，同步切换 key 和数据库密文，再验证 readiness 与授权行为。回退必须恢复匹配的密文/key 对，不能只回退其中一项。
6. 在确认新备份可恢复前保留 old key；保留期及销毁由责任人确认。未来若需要无停机轮换，需新增带 key ID 的密文版本和经审查的双读/单写迁移，不能把 enc:v1 前缀当成 key ID。

此流程明确当前能力边界；未实现专用全字段重加密工具属于未来真实轮换的先决条件，不影响本次恢复原 key、TOTP 加密及拒绝明文写入修复。

## 4. 发布签名密钥轮换

- 构建签名密钥与数据加密主密钥相互独立。发生泄露时停止发布，撤销 CI secret 和访问权限，保留事件审计。
- 安装器信任公钥内嵌，直接更新 CI 私钥会使旧安装器拒绝新包。先发布包含旧/新信任策略的经旧 key 授权兼容安装器，再切换新 key 签名；旧 key 已不可信时必须通过独立可信分发渠道更新安装器，不能用失陷 key 自证新 key。
- 验证新/旧/缺失/损坏签名、manifest 篡改、错版本/架构和包 hash；临时测试 fixture 使用一次性测试 key，禁止复用生产 signing secret。

## 5. Webhook 和退款维护

- 新未验签回调只保留有界元数据，合法业务 inbox 独立保留。预算耗尽和维护异常应告警，不能以关闭验签或扩大无限日志存储解决。
- `urmailctl secrets compact-rejected-webhooks` 默认仅统计；加 `--apply` 每次处理一批历史未验签正文及到期诊断数据。先审核统计、备份策略与实际表体积，再逐批运行；不清理合法资金幂等事实。
- DELETE/字段缩小不保证 OS 磁盘即时回收；观察 PG dead tuples、autovacuum、WAL 与备份增长。重写表/收缩等高风险维护另行评估和授权，不能在自动迁移中执行。
- approved 退款退避后其他商户仍应执行。查看 provider_unavailable/attempts/next_attempt_at，修复历史商户配置后等待重新调度；processing/uncertain 仅查询核对，不能手工重置成 approved 再次付款。

## 6. 容量、补丁和入口

- 密码计算并发、MFA/vault 容量、归档导出并发、文件/消息上限和维护批量由代码显式限制。监控其拒绝计数，避免仅看进程健康。
- 建立正常并发与故障演练的 RSS、CPU、临时磁盘、PG/WAL、Maildir/归档及队列高水位。根据总量与余量设置 systemd MemoryHigh/MemoryMax、TasksMax 和磁盘告警，先在兼容 Linux 副本验证；不把测试进程 RSS 当作生产全栈内存预算，不用过低 MemoryMax 造成正常邮件处理 OOM。
- `urmailctl config status` 必须证明 desired/applied generation、daemon 与邮件证书探针一致；healthz 不代表邮件就绪。
- 核对 nginx 80/443、SMTP 25/465/587、实际启用 IMAP/POP3/Sieve、NSD 53；后端8080、PG5432、milter/LMTP/metrics仅内部。对 IPv4/IPv6 分别核对防火墙/云安全组；无需主动扫描或把邮件 IP 公开当漏洞。
- 保存 `dpkg-query` 系统包版本清单，核对 Ubuntu 官方安全更新和实际 unattended-upgrades 状态；Go reachable SCA 与 npm audit 绑定锁文件和工具版本。公告命中须复核调用和适用范围，未运行扫描不得记录为零漏洞。
- 新装默认 OpenDKIM 与 DNS SPF/DMARC 引导不等于完整入站反垃圾/病毒检查；v80 profile 保留的 spampd/OpenDMARC/greylist/RBL 需逐个验证。新增外部防护组件需明确需求、维护人与容量预算。
- 归档READ成功只证明授权/审计事务成功，不能证明客户端收到全文；观察deferred stream failed、独立stream_failed审计及warning，数据库故障时补充审计为best-effort，必须配置告警接收与确认。传输槽不等于临时磁盘预留，部署容量预算覆盖同时存在的raw/decoded快照。
- DDoS、防火墙、TLS 公信链/ACME续期、真实支付商户及告警 receiver 是环境事实，需要相应责任人确认。

## 7. 备份恢复验收

- 明确 RPO/RTO、全量/增量策略、离机加密与保留期；备份 key 的恢复权限独立管理。只有归档存在不算可恢复。
- 完整备份采用受支持停写/maintenance 窗口，覆盖数据库、配置/密钥/证书、Maildir/归档/spool、Postfix queue、Sieve 源码及版本清单。
- 在网络隔离且无公开端口的独立实例恢复，核对 tenant/mailbox 归属、RLS/角色、密文解密、原文 hash、队列和定时/资金任务 hold。恢复期间保持 quarantine，确认源实例隔离、外部状态核对和授权后才能 release。
- 演练数据库不可用、磁盘不足、归档写失败、SMTP提交结果未知、provider超时和服务重启；验证没有虚假确认、自动重复退款/发信或跨租户泄露。
- 告警规则、receiver 文件、timer 存在不代表实际送达；使用专门测试 receiver 验证，并保留脱敏 receipt。

## 8. 本轮与生产验收分开

本轮结果文件记录可在本机/隔离环境证明的代码性质。生产升级、实付款、DNS/ACME、云防护和备份介质状态单独列为待验证，不因源码问题 CLOSED 自动通过。
