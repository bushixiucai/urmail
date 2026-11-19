# 行锁语义损失登记（S2，逐条）

删除 FOR UPDATE/FOR SHARE 的 63 处子句，逐条记录原用途与替代方案；S3/S4 复核清单，不得默默消失。

| 方法 | 文件 | 锁 | 目标 | 用途 | 替代 |
|---|---|---|---|---|---|
| ArchiveCasesLock | archive_case_product.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| ArchiveCasesApproval | archive_case_product.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| LockArchiveRetentionPreview | archive_lifecycle.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| GetArchiveOriginalForRecovery | archive_lifecycle.sql | FOR UPDATE | a | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| CreateSession | auth.sql | FOR UPDATE | u | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| GetComplianceGrant | compliance_and_retention.sql | FOR SHARE | g | 事务内一致性读锁 | WAL 快照读一致性（SQLite 读一致无需行锁） |
| GetEASSend | mailbox_eas_send.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| LockEASSendCopy | mailbox_eas_send.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| LockMailboxImportOwner | mailbox_import.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| LockMailboxImportJob | mailbox_import.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| GetMarketingFlowDefinition | marketing_flow_authoring.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformGetSieveActivationTarget | platform_ai.sql | FOR UPDATE | m | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformLockAIQuotaTenantReader | platform_ai_quota.sql | FOR SHARE | u | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockPlatformAdmin | platform_ai_quota.sql | FOR SHARE | u | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockAliasSenderTarget | platform_alias_senders.sql | FOR UPDATE | a | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockAliasSenderTarget | platform_alias_senders.sql | FOR SHARE | d | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockAliasSenderMailboxes | platform_alias_senders.sql | FOR SHARE | m | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformExpireArchiveApprovals | platform_archive_activity.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformApplyArchiveRetention | platform_archive_lifecycle.sql | FOR UPDATE | a | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockArchivePurgeCandidate | platform_archive_lifecycle.sql | FOR UPDATE | a | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockRetentionPurgePolicy | platform_archive_lifecycle.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| CreateApplicationCredential | platform_auth_application.sql | FOR UPDATE | u | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformRehashPanelPassword | platform_auth_security.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformLockBrandHost | platform_brand_host_verify.sql | FOR UPDATE | h | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockBrandSettingsBrand | platform_brand_settings.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformFindSchedulingEvent | platform_calendar_scheduling.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformTakeReminderScan | platform_calendar_scheduling.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformPublishDueCalendarReminders | platform_calendar_scheduling.sql | FOR UPDATE | j | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformClaimMailboxJob | platform_collaboration.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockCollectorCheckpoint | platform_delivery_collector.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimDNSPlan | platform_dns_onboarding.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformFenceDNSPlan | platform_dns_onboarding.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimOnboarding | platform_dns_onboarding.sql | FOR UPDATE | t | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformFenceOnboarding | platform_dns_onboarding.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformCurrentManagedDNS | platform_dns_onboarding.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformLockLegacyImportTarget | platform_legacy_import.sql | FOR UPDATE | t | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockLegacyImportTarget | platform_legacy_import.sql | FOR SHARE | a | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockLegacyDKIMDomains | platform_legacy_import.sql | FOR UPDATE | d | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformMailboxPasswordForSession | platform_mailbox_password.sql | FOR UPDATE | m | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformGetSieveBridge | platform_mailbox_principal.sql | FOR UPDATE | m | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformCreateVerifiedMailboxSession | platform_mailbox_principal.sql | FOR UPDATE | m | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimMailboxScheduledSend | platform_mailbox_scheduled_sends.sql | FOR UPDATE | s | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimSendCopy | platform_mailbox_send_receipts.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimScheduledSentCopy | platform_mailbox_sent_copy.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformGetLegacyScheduledCopy | platform_mailbox_sent_copy.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformClaimMarketingRecipient | platform_marketing.sql | FOR UPDATE | d | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimMarketingRecipient | platform_marketing.sql | FOR UPDATE | r | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimMarketingFlowPositions | platform_marketing_flows.sql | FOR UPDATE | d | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimMarketingFlowPositions | platform_marketing_flows.sql | FOR UPDATE | p | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformArmMarketingFlowWait | platform_marketing_flows.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformClaimEmailNotification | platform_notifications.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformGetDataImportReceipt | platform_people_product.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformLockCalendarInvitationEvent | platform_people_product.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockRecallAuthority | platform_recall_product.sql | FOR SHARE | r | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockRecallMailbox | platform_recall_product.sql | FOR SHARE | m | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformLockRecallForExpunge | platform_recalls.sql | FOR UPDATE | r | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformMailboxRestoreIdentity | platform_restore_sync.sql | FOR UPDATE | m | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformLockLegacyMFASecrets | platform_secret_maintenance.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| PlatformGetSupportTicket | platform_support.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| PlatformClaimOnboardingTask | platform_tenants.sql | FOR UPDATE | — | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| GetMailboxDailySentForUpdate | platform_usage.sql | FOR UPDATE | m | 队列抢占/租约互斥 | 条件 UPDATE（WHERE 旧状态）+ changes()==1 判定原子抢占；事务 BEGIN IMMEDIATE 串行（S4 批次逐语句落地） |
| GetRecallByID | recalls.sql | FOR UPDATE | — | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
| GetRecallTarget | recalls.sql | FOR UPDATE | a | 事务内一致性读锁 | SQLite 单写者 + 事务串行 |
