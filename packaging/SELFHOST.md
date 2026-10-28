# urMail 自托管安装、升级与恢复

支持 Ubuntu 22.04/24.04，amd64/arm64。产品的支持矩阵与某台机器已经通过验收是两件事：发行记录必须附对应架构/系统的真实证据，Rosetta/QEMU 不等于原生 amd64 验收。

## 依赖与网络

安装器安装 PostgreSQL、Postfix、Dovecot（IMAP/POP3/LMTP/Sieve/PG）、nginx、OpenDKIM、可选权威 DNS 的 nsd，以及 age、OpenSSH/SFTP 客户端、Python3、证书与系统工具。前端已嵌入二进制，目标机器不需要 Node/Go。系统服务和证书操作由 root agent 执行，业务 daemon 为 mailinabox 用户。

公网端口：25、465、587、993、995/TCP；80/TCP 用于 HTTP/ACME，443/TCP 用于面板、Webmail 和三类同步。只有启用权威 DNS 时才需要开放 53/TCP+UDP。8080、PG、策略 socket 和所有 milter 仅限回环/Unix socket，不能公开暴露。具体机器可监听地址以受控配置为准，不要为“群件”另开放任意端口。

域名需归属验证，MX/SPF/DKIM/DMARC 与实际主机一致，PTR 由 IP 提供商配置；公开客户端使用证书前验证名称、信任链、有效期及续期路径。自签名证书仅引导，不能作为公网验收。

邮件存储默认 `/var/mail`；业务/归档和配置目录由安装配置与备份 manifest 明示。运营者在上线前测量并固定容量、备份窗口、保留、RPO/RTO 和资源预算；磁盘不足不能以“安装成功”掩盖。

## 新装与部署开关

设置 `URMAIL_MYHOSTNAME`、`URMAIL_PRIMARY_DOMAIN`、`URMAIL_PUBLIC_IPV4`，执行经过审查的安装脚本。新装默认启用四个入口，升级保留已有配置：

```bash
URMAIL_WEBMAIL_NATIVE=true
URMAIL_CARDDAV_ENABLED=true
URMAIL_CALDAV_ENABLED=true
URMAIL_EAS_ENABLED=true
```

使用显式 `false` 关闭相应入口。所有值必须严格为 `true` 或 `false`；语法错误在写配置前拒绝。旧安装未保存 Webmail 开关时保留原先的关闭行为，既有 DAV/EAS 缺省行为保留。协议关闭后不发布相应可用信号，配置收敛与路由按实际启用项验证，数据库保留不删除。

`URMAIL_POLICY_ENABLED=true` 保持发信资格/欠费规则强制执行，不允许安装重跑将其关闭。AI、支付、营销服务可未配置，但必须显示不可用/未配置，不能拖垮基本邮件和协作。

## 包来源与签名

远端安装只信任仓库固定 Ed25519 公钥签署的 `manifest-ARCH.json`，再校验其绑定的 deb SHA256。发布流水线验证签名与固定公钥匹配后才上传。镜像地址不是新的信任根。有效签名清单绑定版本、架构、文件名和包哈希；错误签名、篡改清单、架构错误或包被修改均拒绝。

`URMAIL_PACKAGE` 是运营人员显式指定本地包的受控路径，不表示安装器替该包证明来源；线下分发应随包带原始签名清单，先按固定公钥验证。不要将未签名开发包上传为稳定版本。

## 幂等重跑与升级

重复安装保留主密钥、已有证书、数据库口令、非托管操作员配置及现有账号密码/权益。默认值只用于新键，操作员显式传入的设置优先；不会执行旧 env 文件中的 shell 字符串。原权限和 mode 由安装器重新固定。

升级前安装器将旧 env、agent env 和数据库快照写入 root-only `/root/urmail-upgrade-backups/`。这只是升级 checkpoint，不替代完整包含 Maildir/归档/协作/密钥的异地备份。正式升级前按 `OPS002_JOURNEY.md` 完成停写、一致快照、清单与独立密钥核验。

迁移完成后重启两个 daemon、配置收敛并检查 `urmailctl config status --wait 240s`。进程启动、SQL迁移 no-op 或 healthz 不是邮件可用证明。真实升级记录必须验证原有邮箱、联系人/日历及设备映射，保留数据数量/标识与实际客户端流程。

## 兼容矩阵与失败处理

| 源状态 | 目标 | 必须验证 | 失败路径 |
|---|---|---|---|
| 已有邮件基线 / schema 0077 或 0083 | 当前发行 schema | 用户/邮箱/角色/密钥/权益保留，新增协作迁移成功 | 停止写入，保存错误与 dirty 状态；仅在证明失败迁移事务未提交且旧版本完整时按受控修复前滚 |
| 已有群件 / schema 0118 | 当前发行 schema | 联系人、日历、EAS device/folder 映射保留 | 禁止 worker 外部副作用，完成一致性审查后前滚或完整独立恢复 |
| 当前发行重装 | 同版同schema | 幂等、不换密码/密钥、不重复扣款/投递 | 保留原数据与配置，不清空schema/目录 |
| 当前发行回到旧版 | 经验证的旧产物+旧schema+旧配置+对应备份 | 精确版本元组匹配及新数据处置决定 | 不直接运行 SQL down 后启动旧二进制；隔离恢复旧快照，不能覆盖还在写入的生产数据 |

一次迁移失败不能靠无条件 `force`、删表或改版本号“修好”。先记录失败点、判断事务是否已提交和数据是否变化，保留围栏；恢复必须在隔离目标验证并显式放行。单元测试中的故障注入修复只对其已证明的完整事务回滚场景成立。

完整恢复顺序：停写/停外部 worker → 独立目标安装匹配版本 → 验证清单、密钥与备份链 → 解密/恢复 → 校验RLS/角色/数据 → 重新建立同步epoch和待审外部工作 → 人工核对 → 解除围栏并分批启动。不能清空幂等键后盲目重放旧扣款、营销或邀请。

自动卸载不删除邮件数据。旧 MIAB/SQLite 安装禁止原地覆盖，按标准导入导出和受控迁移指南保全原数据。

## Explicit recurring billing

Stripe card payments have a separate `stripe_off_session_v1` recurring adapter. Configure the merchant account and its merchant-specific webhook URL, then explicitly verify the account capability from the platform merchant page. Checkout credentials alone never enroll a tenant. The existing worker periodically revalidates already approved, unchanged accounts; it cannot approve new accounts or undo a revocation.

A tenant owner, administrator or billing member starts the official Stripe-hosted setup, returns to verify the saved authorization, then separately confirms the current frozen renewal quote. Canceling an enrollment prevents unstarted charges; an operation already in flight remains visible for reconciliation. Uncertain charges are queried with GET only, including after provider idempotency retention expires. Missing, truncated or ambiguous observations stay uncertain. Other checkout providers remain available for active payments without claiming recurring support.

## Reviewed mail migration

The native mailbox supports EML and mboxrd preview, per-message validation, duplicate handling and explicit confirmation. The source limit is 20 MiB, each message is at most 8 MiB, and one preview contains at most 100 items. Text line endings are canonicalized for IMAP; MIME bodies and decoded attachments are verified in the supported round-trip tests. Binary MIME that cannot be represented losslessly in mboxrd is refused explicitly; use EML for that content.

Import acceptance and local receipts are durable. After a lost APPEND acknowledgment, use the read-only reconciliation action before any reviewed retry. An incomplete scan never establishes absence. Canceling, losing a lease or restoring a backup cannot turn an unknown operation into permission to append again. Restored previews and provider setup proofs require new review.

## Cloud AI configuration and consent

`URMAIL_AI_CLOUD_ENDPOINTS` is an exact operator allowlist, not permission to send mailbox content. Save provider credentials through the authenticated configuration API so they remain encrypted. Cloud processing requires an explicit current configuration digest and content-scope consent from an active tenant owner or administrator. Revocation, a provider/model/scope change or removal of the granting role invalidates that consent. Native AI requests remain scoped to the authenticated mailbox; configuring a provider does not authorize a background export of customer mail.


## Root-managed automatic backups

The fixed `urmail-backup-auto.timer` runs an independent root oneshot. Complete
maintenance backups stop and restore their original writer services using a
private durable receipt; the job never runs inside the daemon or agent cgroup.
Store the age public recipient, offsite destination and credentials in the
root-only `/etc/urmail/agent.env`. An unconfigured job performs no maintenance.
Use `systemctl start urmail-backup-auto.service` for the configured root job;
review the journal and `backup_runs` outcome, not just timer activation.

A deployment using an existing loopback-published PostgreSQL container can set
`URMAIL_BACKUP_POSTGRES_CONTAINER` in that root-only file. The backup binds the
migration URL's loopback host port to container port 5432 and pins its immutable
container ID before fixed `pg_dump` execution. It does not create another
database or expose a caller-controlled shell command. Standalone manual backup
commands may pass that same nonsecret container selector in their environment.
Default native installations continue using the managed local peer connection.
