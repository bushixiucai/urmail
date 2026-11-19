# pre-auth / ingress 白名单（初稿 v2，S3 装饰器唯一来源；逐条人工签核）

粒度：**路由 + 方法族**。入口由 URL/触发条件决定（Host、email 参数、token、无会话进程），
不能只用方法名。qQA 复核修订：死路由已清（/api/v1/billing/webhook/* 因商业化移除不在
router 中），补入三类入口。

| # | 入口类 | 路由 / 触发 | 方法族 | 约束 / 说明 | 签核 |
|---|---|---|---|---|---|
| 1 | ACME HTTP-01 | `/.well-known/acme-challenge/*`（Host 驱动） | 无 store 查询（webroot 文件系统应答，由反代直出）— S6 证书链改造时复核仍无库路径 | | ☐ |
| 2 | autoconfig / autodiscover | `/mail/config-v1.1.xml`、`/.well-known/autoconfig/*`、`/autodiscover/autodiscover.xml`（email 参数驱动，`autoconfig.Mount` router:166） | email→域配置查询族（无会话） | | ☐ |
| 3 | dovecot passdb/userdb | dovecot-sqlite `db.conf`（引擎级，绕过 store/decorator） | 直接读 auth 快照库（`mode=ro`） | **断言：渲染结果只能指向快照路径**（renders 输出 lint + S6 测试），S6 auth 快照隔离承担 | ☐ |
| 4 | **入站 SMTP policy**（QA 补） | SMTP DATA 投递路径（`internal/mail/policy/inbound.go` → `PlatformInboundAllowed`，外部不可信输入、无会话） | postfix policy 回调族 | 不可信输入防护随 S4 装饰器之外的入口专项 | ☐ |
| 5 | **mailbox 凭据认证 IMAP/DAV/EAS**（QA 补） | IMAP/DAV/EAS 的 Basic/mailbox 凭据入口（凭据=邮箱凭据非租户会话） | mailbox 认证与同步族 | 四轴作用域假设不成立——单独建模，pre-auth 以 mailbox 凭据身份授予（S3 设计确认） | ☐ |
| 6 | **退订/回信 token 路由**（QA 补） | `/api/v1/marketing/unsubscribe` POST（OneClick）/ GET（Confirmation），stateless 签名 token（router:523-524） | marketing suppression token 族 | token 校验通过后仅允许 suppression 写入 | ☐ |
| 7 | 登录 / 恢复入口 | `/api/v1/auth/*` 认证前子集（login、password reset、mfa challenge 发起） | `PlatformCreateVerifiedMailboxSession`、会话/凭据查询族（pre-auth 逐方法授予） | 待补方法清单 | ☐ |

死路由清理记录：`/api/v1/billing/webhook/*`（原 v1 草案 row 4）——商业化移除后 router 中无
对应挂载，已从本表移除；反代/webhook 其余入口在需要时逐条新增并双签。

## 维护规则

1. 本表是 `method_scopes.json` 的 `preauth_allowlist` 唯一来源，双向校验门在 S3 落地；
2. 新增入口须 PR 附风险说明与 QA+Backend 双签；
3. dovecot 直连库与 IMAP/DAV/EAS mailbox 凭据入口为**已知不归 tenant/platform decorator 管辖**
   的入口，分别由 S6 auth 快照隔离与 mailbox 作用域专项承担。
