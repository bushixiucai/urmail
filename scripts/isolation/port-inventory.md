# 移植面三分类底表（S0，脚本生成）

函数条目 119（distinct 118，urmail_record_audit 双签名）；触发器 60。

分类计数：{"sqlite-trigger": 30, "app-assume": 73, "deprecated": 15}

| 函数 | 类别 | 替代物/依据 | 调用证据 |
|---|---|---|---|
| domains_touch_updated_at | sqlite-trigger | BEFORE UPDATE 触碰 updated_at | 库内（迁移/触发器链） |
| revoke_renewal_on_subscription_cancel | deprecated | subscriptions | 库内（迁移/触发器链） |
| urmail_abandon_dns_plan | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_ai_cloud_authorized | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_ai.sql.go, internal/store/queries/platform_ai.sql |
| urmail_ai_configuration_consent_guard | sqlite-trigger | 非 platform 租户 consent 禁改 | 库内（迁移/触发器链） |
| urmail_alias_sender_logins_guard | sqlite-trigger | sender_logins 身份不可变 | 库内（迁移/触发器链） |
| urmail_alias_would_cycle | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_postfix.sql.go, internal/store/queries/platform_postfix.sql |
| urmail_archive_activity_notice | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_archive_approval_bound | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_archive_case_changed | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_archive_case_grant | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/archive_case_product.sql.go, internal/store/queries/archive_case_product.sql |
| urmail_archive_case_manager | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_archive_case_member | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_archive_case_snapshot | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/archive_case_product.sql.go, internal/store/queries/archive_case_product.sql |
| urmail_archive_export_visible | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/archive_case_product.sql.go, internal/store/queries/archive_case_product.sql |
| urmail_archive_notification_visible | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/notifications.sql.go, internal/store/queries/notifications.sql |
| urmail_archive_purge_guard | sqlite-trigger | legal_hold 期间禁删/改 | 库内（迁移/触发器链） |
| urmail_authorize_marketing_send | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing.sql.go, internal/store/queries/platform_marketing.sql |
| urmail_begin_marketing_flow_send | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing_flow_authoring.sql.go, internal/store/queries/platform_marketing_flow_authoring.sql |
| urmail_begin_restore_quarantine | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | cmd/urmailctl/restore.go |
| urmail_billing_calendar_guard | deprecated | billing_calendars | 库内（迁移/触发器链） |
| urmail_billing_profile_write_guard | sqlite-trigger | billing 写权限守卫 | 库内（迁移/触发器链） |
| urmail_bump_marketing_bounce_unknown | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_bump_marketing_campaign_stats | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_business_timezone | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_mail_and_ai_gate.sql.go, internal/store/queries/platform_mail_and_ai_gate.sql |
| urmail_cancel_campaign_tasks | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_cancel_dns_plan | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_change_panel_mfa | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_auth_security.sql.go, internal/store/queries/platform_auth_security.sql |
| urmail_commit_marketing_flow_fenced | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing_flow_authoring.sql.go, internal/store/queries/platform_marketing_flow_authoring.sql |
| urmail_commit_marketing_flow_step | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/marketing/flow_store.go, internal/store/platform_marketing_flows.sql.go, internal/store/queries/platform_marketing_flows.sql |
| urmail_configure_tenant_ai | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_ai.sql.go, internal/store/queries/platform_ai.sql |
| urmail_configure_tenant_ai_governance | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_ai.sql.go, internal/store/queries/platform_ai.sql |
| urmail_confirm_dns_plan | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_consent_tenant_ai | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_ai.sql.go, internal/store/queries/platform_ai.sql |
| urmail_contact_group_member_guard | sqlite-trigger | 成员域守卫 | 库内（迁移/触发器链） |
| urmail_coupon_reissue_immutable | deprecated | coupons | 库内（迁移/触发器链） |
| urmail_coupon_release_authority | deprecated | coupon_redemptions | 库内（迁移/触发器链） |
| urmail_create_archive_case | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/archive_case_product.sql.go, internal/store/queries/archive_case_product.sql |
| urmail_create_dns_plan | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_decide_archive_approval | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_delete_contact_group | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_collaboration.sql.go, internal/store/queries/platform_collaboration.sql |
| urmail_dns_actor | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_domain_mail_ready | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/mail/dovecot/render.go, internal/sieve/testdata/legacy-protocol.py, internal/store/platform_sieve_gate.sql.go, internal/store/platform_usage.sql.go, internal/store/queries/platform_sieve_gate.sql, internal/store/queries/platform_usage.sql |
| urmail_domain_provisioning_guard | sqlite-trigger | provisioning 状态守卫 | 库内（迁移/触发器链） |
| urmail_enqueue_verified_domain | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_evidence_manifest_visible | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_archive_evidence.sql.go, internal/store/queries/platform_archive_evidence.sql |
| urmail_finish_marketing_send | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing.sql.go, internal/store/queries/platform_marketing.sql |
| urmail_freeze_subscription_plan | deprecated | subscriptions | 库内（迁移/触发器链） |
| urmail_guard_host_namespace | sqlite-trigger | hostname 命名空间守卫 | 库内（迁移/触发器链） |
| urmail_guard_marketing_snapshot | sqlite-trigger | 快照不可变守卫 | 库内（迁移/触发器链） |
| urmail_guard_nonplatform_ai_quota | sqlite-trigger | nonplatform AI 配额守卫 | 库内（迁移/触发器链） |
| urmail_guard_published_marketing_step | sqlite-trigger | 已发布步骤守卫 | 库内（迁移/触发器链） |
| urmail_hold_guard | sqlite-trigger | hold 守卫（domains/mailbox 家族复用） | 库内（迁移/触发器链） |
| urmail_invoice_document_immutable | deprecated | invoices | 库内（迁移/触发器链） |
| urmail_mail_allowed | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/domain/mail/marketing.go, internal/sieve/testdata/legacy-protocol.py, internal/store/platform_ai.sql.go, internal/store/platform_mail_access.sql.go, internal/store/platform_mail_and_ai_gate.sql.go, internal/store/platform_marketing.sql.go, internal/store/platform_marketing_flow_authoring.sql.go, internal/store/platform_marketing_flows.sql.go, internal/store/platform_sieve_gate.sql.go, internal/store/platform_usage.sql.go, internal/store/queries/platform_ai.sql, internal/store/queries/platform_mail_access.sql, internal/store/queries/platform_mail_and_ai_gate.sql, internal/store/queries/platform_marketing.sql, internal/store/queries/platform_marketing_flow_authoring.sql, internal/store/queries/platform_marketing_flows.sql, internal/store/queries/platform_sieve_gate.sql, internal/store/queries/platform_usage.sql, scripts/sieve-runtime/journey.py, scripts/sieve-runtime/personal_capability.py |
| urmail_mail_readable | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/mail/dovecot/render.go, internal/store/platform_usage.sql.go, internal/store/queries/platform_usage.sql |
| urmail_mailbox_identity_guard | sqlite-trigger | 邮箱身份不可变守卫 | 库内（迁移/触发器链） |
| urmail_mailbox_password_version_guard | sqlite-trigger | 密码版本守卫 | 库内（迁移/触发器链） |
| urmail_mailbox_sieve_mode_guard | sqlite-trigger | sieve 模式守卫 | 库内（迁移/触发器链） |
| urmail_mark_marketing_flow_uncertain | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing_flows.sql.go, internal/store/queries/platform_marketing_flows.sql |
| urmail_mark_marketing_recipient_revoked | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing.sql.go, internal/store/queries/platform_marketing.sql |
| urmail_marketing_flow_may_advance | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing_flow_authoring.sql.go, internal/store/platform_marketing_flows.sql.go, internal/store/queries/platform_marketing_flow_authoring.sql, internal/store/queries/platform_marketing_flows.sql |
| urmail_marketing_flow_sendable | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing_flows.sql.go, internal/store/queries/platform_marketing_flows.sql |
| urmail_marketing_limits | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_migration_entitlement_allowed | deprecated | plans | 库内（迁移/触发器链） |
| urmail_migration_plan_guard | deprecated | plans | 库内（迁移/触发器链） |
| urmail_migration_subscription_guard | deprecated | subscriptions | 库内（迁移/触发器链） |
| urmail_order_intention_guard | deprecated | orders | 库内（迁移/触发器链） |
| urmail_panel_credential_version | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_payment_inbox_alert | deprecated | payment_webhook_inbox | 库内（迁移/触发器链） |
| urmail_plan_snapshot_fits | deprecated | plans | 库内（迁移/触发器链） |
| urmail_quota_day | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_usage.sql.go, internal/store/queries/platform_usage.sql |
| urmail_read_evidence_body | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_archive_evidence.sql.go, internal/store/queries/platform_archive_evidence.sql |
| urmail_recall_guard | sqlite-trigger | recall 状态守卫 | 库内（迁移/触发器链） |
| urmail_recall_message_identity_guard | sqlite-trigger | recall 消息身份守卫 | 库内（迁移/触发器链） |
| urmail_record_audit | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/audit.sql.go, internal/store/queries/audit.sql |
| urmail_record_marketing_feedback | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing.sql.go, internal/store/queries/platform_marketing.sql |
| urmail_record_marketing_flow_audit | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/marketing_flow_authoring.sql.go, internal/store/queries/marketing_flow_authoring.sql |
| urmail_record_rejected_webhook | deprecated | payment_webhook_rejections | 库内（迁移/触发器链） |
| urmail_release_marketing_flow_reservation | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing_flow_authoring.sql.go, internal/store/queries/platform_marketing_flow_authoring.sql |
| urmail_release_restore_quarantine | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | cmd/urmailctl/restore.go |
| urmail_request_onboarding | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_require_content_owner | sqlite-trigger | owner 角色要求 | 库内（迁移/触发器链） |
| urmail_require_encrypted_totp | sqlite-trigger | TOTP 加密要求 | 库内（迁移/触发器链） |
| urmail_require_grant_independence | sqlite-trigger | grant 独立性要求 | 库内（迁移/触发器链） |
| urmail_require_marketing_admin | sqlite-trigger | marketing admin 要求 | 库内（迁移/触发器链） |
| urmail_require_open_ticket | sqlite-trigger | open ticket 要求 | 库内（迁移/触发器链） |
| urmail_require_tenant_content_admin | sqlite-trigger | content admin 要求（0028 guard 链） | 库内（迁移/触发器链） |
| urmail_reserve_mailbox_storage | sqlite-trigger | 存储预约（预约行写入+核对，SQLite 触发器可等价） | 库内（迁移/触发器链） |
| urmail_resolve_marketing_attempt | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing.sql.go, internal/store/queries/platform_marketing.sql |
| urmail_resource_guard | sqlite-trigger | 资源域守卫（多表复用） | 库内（迁移/触发器链） |
| urmail_restore_quarantine_active | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_mailbox_scheduled_sends.sql.go, internal/store/platform_mailbox_send_receipts.sql.go, internal/store/platform_mailbox_sent_copy.sql.go, internal/store/queries/platform_mailbox_scheduled_sends.sql, internal/store/queries/platform_mailbox_send_receipts.sql, internal/store/queries/platform_mailbox_sent_copy.sql |
| urmail_restore_quarantine_guard | sqlite-trigger | 恢复隔离守卫 | 库内（迁移/触发器链） |
| urmail_restore_sync_epoch | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_restore_sync.sql.go, internal/store/queries/platform_restore_sync.sql |
| urmail_retention_authorized | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/compliance_and_retention.sql.go, internal/store/platform_archive_lifecycle.sql.go, internal/store/queries/compliance_and_retention.sql, internal/store/queries/platform_archive_lifecycle.sql |
| urmail_retention_confirmation_guard | sqlite-trigger | 保留确认守卫 | 库内（迁移/触发器链） |
| urmail_retention_management_guard | sqlite-trigger | 保留管理守卫 | 库内（迁移/触发器链） |
| urmail_retry_marketing_flow_instance | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/marketing_flow_authoring.sql.go, internal/store/queries/marketing_flow_authoring.sql |
| urmail_review_dns_plan | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_revoke_dns_credential | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_revoke_mailbox_sessions | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_rollback_dns_plan | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_rotate_panel_password | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_auth_security.sql.go, internal/store/queries/platform_auth_security.sql |
| urmail_save_dns_credential | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/dns_onboarding.sql.go, internal/store/queries/dns_onboarding.sql |
| urmail_schedule_marketing | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/marketing_campaigns.sql.go, internal/store/queries/marketing_campaigns.sql |
| urmail_seed_marketing_dispatch | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_setting_limit | sqlite-trigger | settings 上限守卫（0158 guard 链） | 库内（迁移/触发器链） |
| urmail_skip_marketing_recipient | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_marketing.sql.go, internal/store/queries/platform_marketing.sql |
| urmail_soft_delete_archive | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/compliance.sql.go, internal/store/queries/compliance.sql |
| urmail_stop_revoked_flow_contacts | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_stop_suppressed_marketing_flows | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_store_evidence_body | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_store_evidence_manifest | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_subscription_guard | deprecated | subscriptions | 库内（迁移/触发器链） |
| urmail_subscription_term_guard | deprecated | subscription_terms | 库内（迁移/触发器链） |
| urmail_support_reply_notification | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | 库内（迁移/触发器链） |
| urmail_suppression_no_weaken | sqlite-trigger | suppression 不可弱化 | 库内（迁移/触发器链） |
| urmail_take_mailbox_quota | app-assume | 跨行/副作用/函数链 → 应用层或迁移器承担 | internal/store/platform_usage.sql.go, internal/store/queries/platform_usage.sql |

| 触发器 | 表 | 宿主函数 | 分类 |
|---|---|---|---|
| ai_configuration_consent_guard | ai_provider_configs | urmail_ai_configuration_consent_guard | sqlite-trigger |
| nonplatform_ai_quota_guard | ai_provider_configs | urmail_guard_nonplatform_ai_quota | sqlite-trigger |
| archive_approval_activity | archive_approval_requests | urmail_archive_activity_notice | app-assume |
| archive_product_approval_insert | archive_approval_requests | urmail_archive_approval_bound | app-assume |
| archive_product_approval_update | archive_approval_requests | urmail_archive_approval_bound | app-assume |
| archive_physical_purge_guard | archive_content | urmail_archive_purge_guard | sqlite-trigger |
| archive_hold_guard | archive_entries | urmail_hold_guard | sqlite-trigger |
| archive_physical_hold_guard | archive_entries | urmail_archive_purge_guard | sqlite-trigger |
| archive_case_members_changed | archive_investigation_members | urmail_archive_case_changed | app-assume |
| archive_case_sources_changed | archive_investigation_sources | urmail_archive_case_changed | app-assume |
| archive_case_version | archive_investigations | urmail_archive_case_changed | app-assume |
| brand_hosts_namespace_guard | brand_hosts | urmail_guard_host_namespace | sqlite-trigger |
| compliance_grants_content_owner_guard | compliance_grants | urmail_require_content_owner | sqlite-trigger |
| compliance_grants_independence_guard | compliance_grants | urmail_require_grant_independence | sqlite-trigger |
| domain_hold_guard | domains | urmail_hold_guard | sqlite-trigger |
| domain_provisioning_guard | domains | urmail_domain_provisioning_guard | sqlite-trigger |
| domain_resource_guard | domains | urmail_resource_guard | sqlite-trigger |
| domain_restore_quarantine_guard | domains | urmail_restore_quarantine_guard | sqlite-trigger |
| domains_host_namespace_guard | domains | urmail_guard_host_namespace | sqlite-trigger |
| domains_updated_at_touch | domains | domains_touch_updated_at | sqlite-trigger |
| enqueue_verified_domain | domains | urmail_enqueue_verified_domain | app-assume |
| revoke_domain_mailbox_sessions | domains | urmail_revoke_mailbox_sessions | app-assume |
| alias_sender_logins_guard | mailbox_aliases | urmail_alias_sender_logins_guard | sqlite-trigger |
| contact_group_member_scope | mailbox_contact_group_members | urmail_contact_group_member_guard | sqlite-trigger |
| mailbox_hold_guard | mailboxes | urmail_hold_guard | sqlite-trigger |
| mailbox_password_version_guard | mailboxes | urmail_mailbox_password_version_guard | sqlite-trigger |
| mailbox_resource_guard | mailboxes | urmail_resource_guard | sqlite-trigger |
| mailbox_restore_quarantine_guard | mailboxes | urmail_restore_quarantine_guard | sqlite-trigger |
| mailbox_sieve_mode_guard | mailboxes | urmail_mailbox_sieve_mode_guard | sqlite-trigger |
| mailbox_storage_identity | mailboxes | urmail_mailbox_identity_guard | sqlite-trigger |
| reserve_mailbox_storage | mailboxes | urmail_reserve_mailbox_storage | sqlite-trigger |
| revoke_mailbox_sessions | mailboxes | urmail_revoke_mailbox_sessions | app-assume |
| marketing_admin_guard | marketing_campaigns | urmail_require_marketing_admin | sqlite-trigger |
| marketing_campaign_cancel | marketing_campaigns | urmail_cancel_campaign_tasks | app-assume |
| marketing_admin_guard | marketing_contacts | urmail_require_marketing_admin | sqlite-trigger |
| stop_revoked_flow_contacts | marketing_contacts | urmail_stop_revoked_flow_contacts | app-assume |
| marketing_feedback_bounce_unknown | marketing_feedback | urmail_bump_marketing_bounce_unknown | app-assume |
| guard_marketing_content | marketing_flow_content_snapshots | urmail_guard_marketing_snapshot | sqlite-trigger |
| guard_published_marketing_step | marketing_flow_steps | urmail_guard_published_marketing_step | sqlite-trigger |
| guard_marketing_version | marketing_flow_versions | urmail_guard_marketing_snapshot | sqlite-trigger |
| marketing_admin_guard | marketing_lists | urmail_require_marketing_admin | sqlite-trigger |
| marketing_campaign_stats_bump | marketing_recipients | urmail_bump_marketing_campaign_stats | app-assume |
| marketing_suppressions_no_weaken | marketing_suppressions | urmail_suppression_no_weaken | sqlite-trigger |
| stop_suppressed_marketing_flows | marketing_suppressions | urmail_stop_suppressed_marketing_flows | app-assume |
| marketing_admin_guard | marketing_templates | urmail_require_marketing_admin | sqlite-trigger |
| archive_recall_activity | recall_requests | urmail_archive_activity_notice | app-assume |
| recall_message_identity_guard | recall_requests | urmail_recall_message_identity_guard | sqlite-trigger |
| recall_state_guard | recall_requests | urmail_recall_guard | sqlite-trigger |
| retention_confirmation_guard | retention_policies | urmail_retention_confirmation_guard | sqlite-trigger |
| retention_management_guard | retention_policies | urmail_retention_management_guard | sqlite-trigger |
| retention_preview_management_guard | retention_previews | urmail_retention_management_guard | sqlite-trigger |
| billing_profile_write_guard | settings | urmail_billing_profile_write_guard | sqlite-trigger |
| revoke_tenant_mailbox_sessions | tenants | urmail_revoke_mailbox_sessions | app-assume |
| tenant_hold_guard | tenants | urmail_hold_guard | sqlite-trigger |
| tenant_marketing_dispatch | tenants | urmail_seed_marketing_dispatch | app-assume |
| tenant_restore_quarantine_guard | tenants | urmail_restore_quarantine_guard | sqlite-trigger |
| support_reply_notification | ticket_messages | urmail_support_reply_notification | app-assume |
| ticket_messages_open_guard | ticket_messages | urmail_require_open_ticket | sqlite-trigger |
| panel_credential_version | users | urmail_panel_credential_version | app-assume |
| totp_encrypted_writes | users | urmail_require_encrypted_totp | sqlite-trigger |
