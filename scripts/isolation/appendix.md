| 表名 | 类别 | 依据 | 标签 | urmail_app 授权 |
|---|---|---|---|---|
| ai_provider_configs | tenant | tenant_id 列 | RLS | 无 |
| ai_usage_requests | tenant | tenant_id 列 | RLS, append-only, needs-no-insert-trigger | 无 |
| announcements | shared | 平台广播 | RLS, RESTRICTIVE, append-only, needs-no-insert-trigger | SELECT |
| api_tokens | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| application_credentials | shared | 平台出站凭据（0020） | RLS, FORCE | 无 |
| archive_approval_requests | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| archive_content | tenant | tenant_id 列 | RLS, FORCE | SELECT |
| archive_entries | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| archive_evidence_exports | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| archive_investigation_members | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| archive_investigation_sources | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| archive_investigations | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| audit_logs | tenant | tenant_id 列 | RLS, FORCE, RESTRICTIVE, append-only, needs-no-insert-trigger | SELECT |
| backup_runs | shared | 备份运维状态（0120） | RLS, FORCE | 无 |
| brand_host_challenges | shared | 平台验证挑战 | RLS, FORCE | 无 |
| brand_hosts | shared | 平台品牌域 | RLS, FORCE | 无 |
| brands | shared | 平台品牌注册表 | RLS | SELECT |
| channel_attribution | tenant | tenant_id 列 | RLS | INSERT, SELECT |
| channels | shared | 平台频道 | RLS, RESTRICTIVE, append-only, needs-no-insert-trigger | SELECT |
| collector_checkpoints | shared | 采集位点（0038） | RLS | 无 |
| commissions | tenant | tenant_id 列 | RLS | SELECT |
| compliance_grants | tenant | tenant_id 列 | RLS | INSERT, SELECT, UPDATE |
| delivery_events | tenant | tenant_id 列 | RLS, append-only | DELETE, INSERT, SELECT, UPDATE |
| delivery_queue_contexts | shared | 投递队列上下文（0038） | RLS | 无 |
| dns_change_plans | tenant | tenant_id 列 | RLS | SELECT |
| dns_credentials | tenant | tenant_id 列 | RLS | 无 |
| dns_record_actions | tenant | tenant_id 列 | RLS | SELECT |
| dns_zone_versions | shared | DNS 区域版本（平台） | — | 无 |
| domain_dns_security | tenant | tenant_id 列 | RLS, FORCE | SELECT |
| domain_tls_reports | tenant | tenant_id 列 | RLS, FORCE | SELECT |
| domain_transport_security | tenant | tenant_id 列 | RLS, FORCE | INSERT, SELECT, UPDATE |
| domains | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT |
| mailbox_addressbooks | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_ai_chunks | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_ai_drafts | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_ai_phishing_findings | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_aliases | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_calendars | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_changes | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_contact_group_members | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_contact_groups | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_contacts | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_eas_collection_state | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_eas_devices | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_eas_folder_state | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_eas_item_state | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_events | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_import_items | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_import_jobs | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_jobs | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_operation_tickets | tenant | tenant_id 列 | RLS | 无 |
| mailbox_scheduled_sends | tenant | tenant_id 列 | RLS, FORCE | 无 |
| mailbox_send_receipts | tenant | tenant_id 列 | RLS, FORCE | SELECT |
| mailbox_sessions | tenant | tenant_id 列 | RLS | 无 |
| mailbox_settings | tenant | tenant_id 列 | RLS | 无 |
| mailbox_snippets | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_spam_feedback | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_spam_senders | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_spam_tokens | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| mailbox_storage_reservations | tenant(chain) | address→mailboxes 链路（触发器维护，平台写） | RLS, FORCE | 无 |
| mailbox_sync_cursors | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailbox_usage | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| mailboxes | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT |
| managed_dns_zones | tenant | tenant_id 列 | RLS | SELECT |
| marketing_campaign_stats | tenant | tenant_id 列 | RLS | SELECT |
| marketing_campaigns | tenant | tenant_id 列 | RLS | SELECT |
| marketing_channels | tenant | tenant_id 列 | RLS | 无 |
| marketing_contacts | tenant | tenant_id 列 | RLS | INSERT, SELECT, UPDATE |
| marketing_feedback | tenant | tenant_id 列 | RLS | SELECT |
| marketing_flow_content_snapshots | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_flow_definitions | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_flow_entries | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_flow_node_executions | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_flow_positions | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_flow_steps | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_flow_versions | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_flows | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_lists | tenant | tenant_id 列 | RLS | SELECT |
| marketing_quota_windows | tenant | tenant_id 列 | RLS | SELECT |
| marketing_recipients | tenant | tenant_id 列 | RLS | SELECT |
| marketing_suppressions | tenant | tenant_id 列 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| marketing_templates | tenant | tenant_id 列 | RLS | SELECT |
| notification_outbox | tenant | tenant_id 列 | RLS | SELECT |
| onboarding_tasks | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| operational_alerts | tenant | tenant_id 列 | RLS | 无 |
| platform_admins | shared | 平台管理员 | RLS | DELETE, INSERT, SELECT, UPDATE |
| platform_marketing_dispatch | tenant | tenant_id 列 | RLS, FORCE | 无 |
| recall_requests | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| restore_quarantine | shared | 恢复隔离标记 | RLS, FORCE | DELETE, INSERT, SELECT, UPDATE |
| retention_policies | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| retention_previews | tenant | tenant_id 列 | RLS, FORCE | INSERT, SELECT, UPDATE |
| roundcube_bridge_requests | shared | roundcube 桥接请求（平台） | RLS | 无 |
| sessions | tenant | tenant_id 列 | RLS | 无 |
| settings | shared | 平台设置 | RLS, RESTRICTIVE | DELETE, INSERT, SELECT, UPDATE |
| tenant_directory_contacts | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| tenant_directory_settings | tenant | tenant_id 列 | RLS | DELETE, INSERT, SELECT, UPDATE |
| tenants | shared | 租户注册表（平台管理） | RLS | SELECT |
| ticket_attachments | tenant | tenant_id 列 | RLS | INSERT, SELECT |
| ticket_messages | tenant | tenant_id 列 | RLS | SELECT |
| tickets | tenant | tenant_id 列 | RLS | SELECT |
| user_tenant_roles | tenant | tenant_id 列 | RLS | SELECT |
| users | shared | 跨租户主体（user_tenant_roles 多租户映射） | RLS | SELECT |
