# pre-auth / ingress 白名单（初稿，S3 装饰器唯一来源；逐条人工签核）

粒度：**路由 + 方法族**（不只用方法名——入口由 URL/触发条件决定，如 ACME 由 Host、
autoconfig 由 email 参数驱动）。签核栏由 QA+Backend 双签后此表方可被
method_scopes.json 的 preauth_allowlist 消费。

| # | 入口类 | 路由 / 触发 | 方法族 | 当前状态 | 签核 |
|---|---|---|---|---|---|
| 1 | ACME HTTP-01 | `/.well-known/acme-challenge/*`（Host 驱动，证书签发自证） | 无 store 查询（webroot 文件系统应答）；由反代/nginx 直出——**须在 S6 证书链改造时复核是否仍无库路径** | 待复核 | ☐ |
| 2 | autoconfig / autodiscover | `/mail/config-v1.1.xml`、`/.well-known/autoconfig/*`、`/autodiscover/autodiscover.xml`（email 参数驱动） | `internal/autoconfig.Mount` 挂载；email→域配置查询族（按 tenant/domain 读取，无会话） | 草案 | ☐ |
| 3 | dovecot passdb/userdb | dovecot-sqlite `db.conf`（渲染 SQL，**绕过 store**） | 直接库文件读取（S6 auth 快照库 `mode=ro`）——不经 db.Queries，decorator 不覆盖；以快照目录权限+查询渲染白名单约束 | 草案 | ☐ |
| 4 | 反代 / webhook ingress | `/api/v1/billing/webhook/*`（Stripe 签名校验前置）、反代健康与跳转路由 | 签名校验后的 webhook 处理族（写入隔离/幂等表） | 草案 | ☐ |
| 5 | 登录 / 恢复入口 | `/api/v1/auth/*` 认证前子集（login、password reset、mfa challenge 发起） | `PlatformCreateVerifiedMailboxSession`、会话/凭据查询族（pre-auth 授予，逐方法列出） | 待补方法清单 | ☐ |

## 维护规则

1. 进入本表的入口必须在 `method_scopes.json` 的 `preauth_allowlist` 出现，且反向一致
   （scope_lint 双向校验，S3 落地时加门）；
2. 任何入口新增须 PR 附风险说明与双签；
3. dovecot 直连库为**已知不归 decorator 管辖**的入口（S6 auth 快照隔离承担）。
