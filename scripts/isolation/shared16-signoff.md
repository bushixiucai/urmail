# Shared 16 张复核签字（S0，Backend 2026-11-19）

依据：catalog 实测（`data` 无 tenant_id 的 17 张业务表中，
`mailbox_storage_reservations` 归 tenant 链路，其余 16 张为 shared）。
复核方法：tenant_id 缺失为 catalog 机判（pg_attribute 实测）；行归属判定
按下表逐张核对表语义与现有 REVOKE/触发器证据。

| 表 | shared 判定依据 | 复核 |
|---|---|---|
| tenants | 租户注册表，平台管理，行的可见方为平台 | ✓ |
| platform_admins | 平台管理员名单 | ✓ |
| settings | 平台设置（0008 RESTRICTIVE 只读） | ✓ |
| brands | 品牌注册表，跨租户平台数据 | ✓ |
| brand_hosts / brand_host_challenges | 品牌域与验证挑战（0020/0128），平台写 | ✓ |
| announcements | 平台广播（0008 RESTRICTIVE 只读） | ✓ |
| channels | 平台频道（0008 RESTRICTIVE 只读） | ✓ |
| application_credentials | 平台出站凭据（0020，FORCE RLS 但无租户行） | ✓ |
| backup_runs | 备份运维状态（0120），平台角色专属 | ✓ |
| collector_checkpoints | 采集位点（0038），无租户行概念 | ✓ |
| delivery_queue_contexts | 投递队列上下文（0038），平台角色 | ✓ |
| dns_zone_versions | DNS 区域版本（0021/0051），平台操作域 | ✓ |
| restore_quarantine | 恢复隔离标记，运维态 | ✓ |
| roundcube_bridge_requests | roundcube 桥接请求，平台维护 | ✓ |
| users | 跨租户主体：`user_tenant_roles(user_id,tenant_id)` 多租户映射，主体行不属单一租户 | ✓ |

链路特例（非 shared）：`mailbox_storage_reservations(address→mailboxes)`，
行按邮箱归属租户，但由触发器维护、仅平台角色写——矩阵中归 tenant(chain)。

签字：urMail-Backend（S0）；QA 复核栏由 QA 独审时填写。
