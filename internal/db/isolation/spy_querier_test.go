// Code generated for S3 702-method Querier matrix tests.
package isolation

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

type spyQuerier struct {
	calls      int
	lastMethod string
	err        error
}

var _ store.Querier = (*spyQuerier)(nil)

func (s *spyQuerier) record(name string) error {
	s.calls++
	s.lastMethod = name
	return s.err
}

func (s *spyQuerier) AICloudAuthorized(ctx context.Context, arg store.AICloudAuthorizedParams) (bool, error) {
	var zero bool
	return zero, s.record("AICloudAuthorized")
}

func (s *spyQuerier) AbandonDNSPlan(ctx context.Context, arg store.AbandonDNSPlanParams) error {
	return s.record("AbandonDNSPlan")
}

func (s *spyQuerier) ActivateArchiveRetention(ctx context.Context, arg store.ActivateArchiveRetentionParams) error {
	return s.record("ActivateArchiveRetention")
}

func (s *spyQuerier) AddMarketingSuppression(ctx context.Context, arg store.AddMarketingSuppressionParams) (store.MarketingSuppressions, error) {
	var zero store.MarketingSuppressions
	return zero, s.record("AddMarketingSuppression")
}

func (s *spyQuerier) AddSupportAttachment(ctx context.Context, arg store.AddSupportAttachmentParams) (store.AddSupportAttachmentRow, error) {
	var zero store.AddSupportAttachmentRow
	return zero, s.record("AddSupportAttachment")
}

func (s *spyQuerier) AddSupportMessage(ctx context.Context, arg store.AddSupportMessageParams) (store.TicketMessages, error) {
	var zero store.TicketMessages
	return zero, s.record("AddSupportMessage")
}

func (s *spyQuerier) AliasWouldCycle(ctx context.Context, arg store.AliasWouldCycleParams) (bool, error) {
	var zero bool
	return zero, s.record("AliasWouldCycle")
}

func (s *spyQuerier) ArchiveCasesAddMember(ctx context.Context, arg store.ArchiveCasesAddMemberParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesAddMember")
}

func (s *spyQuerier) ArchiveCasesAddSource(ctx context.Context, arg store.ArchiveCasesAddSourceParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesAddSource")
}

func (s *spyQuerier) ArchiveCasesApproval(ctx context.Context, arg store.ArchiveCasesApprovalParams) (store.ArchiveCasesApprovalRow, error) {
	var zero store.ArchiveCasesApprovalRow
	return zero, s.record("ArchiveCasesApproval")
}

func (s *spyQuerier) ArchiveCasesApprovals(ctx context.Context, arg store.ArchiveCasesApprovalsParams) ([]store.ArchiveCasesApprovalsRow, error) {
	var zero []store.ArchiveCasesApprovalsRow
	return zero, s.record("ArchiveCasesApprovals")
}

func (s *spyQuerier) ArchiveCasesClose(ctx context.Context, arg store.ArchiveCasesCloseParams) (store.ArchiveInvestigations, error) {
	var zero store.ArchiveInvestigations
	return zero, s.record("ArchiveCasesClose")
}

func (s *spyQuerier) ArchiveCasesCreate(ctx context.Context, arg store.ArchiveCasesCreateParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesCreate")
}

func (s *spyQuerier) ArchiveCasesDecideApproval(ctx context.Context, arg store.ArchiveCasesDecideApprovalParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesDecideApproval")
}

func (s *spyQuerier) ArchiveCasesEligibleMembers(ctx context.Context, tenantID int64) ([]store.ArchiveCasesEligibleMembersRow, error) {
	var zero []store.ArchiveCasesEligibleMembersRow
	return zero, s.record("ArchiveCasesEligibleMembers")
}

func (s *spyQuerier) ArchiveCasesExportVisible(ctx context.Context, arg store.ArchiveCasesExportVisibleParams) (bool, error) {
	var zero bool
	return zero, s.record("ArchiveCasesExportVisible")
}

func (s *spyQuerier) ArchiveCasesGet(ctx context.Context, arg store.ArchiveCasesGetParams) (store.ArchiveInvestigations, error) {
	var zero store.ArchiveInvestigations
	return zero, s.record("ArchiveCasesGet")
}

func (s *spyQuerier) ArchiveCasesGetEvidence(ctx context.Context, arg store.ArchiveCasesGetEvidenceParams) (store.ArchiveEvidenceExports, error) {
	var zero store.ArchiveEvidenceExports
	return zero, s.record("ArchiveCasesGetEvidence")
}

func (s *spyQuerier) ArchiveCasesGetPendingApproval(ctx context.Context, arg store.ArchiveCasesGetPendingApprovalParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesGetPendingApproval")
}

func (s *spyQuerier) ArchiveCasesGrantEligible(ctx context.Context, arg store.ArchiveCasesGrantEligibleParams) (bool, error) {
	var zero bool
	return zero, s.record("ArchiveCasesGrantEligible")
}

func (s *spyQuerier) ArchiveCasesInsertApproval(ctx context.Context, arg store.ArchiveCasesInsertApprovalParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesInsertApproval")
}

func (s *spyQuerier) ArchiveCasesList(ctx context.Context, tenantID int64) ([]store.ArchiveInvestigations, error) {
	var zero []store.ArchiveInvestigations
	return zero, s.record("ArchiveCasesList")
}

func (s *spyQuerier) ArchiveCasesLock(ctx context.Context, arg store.ArchiveCasesLockParams) (store.ArchiveInvestigations, error) {
	var zero store.ArchiveInvestigations
	return zero, s.record("ArchiveCasesLock")
}

func (s *spyQuerier) ArchiveCasesMembers(ctx context.Context, arg store.ArchiveCasesMembersParams) ([]store.ArchiveCasesMembersRow, error) {
	var zero []store.ArchiveCasesMembersRow
	return zero, s.record("ArchiveCasesMembers")
}

func (s *spyQuerier) ArchiveCasesRemoveMember(ctx context.Context, arg store.ArchiveCasesRemoveMemberParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesRemoveMember")
}

func (s *spyQuerier) ArchiveCasesRemoveSource(ctx context.Context, arg store.ArchiveCasesRemoveSourceParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesRemoveSource")
}

func (s *spyQuerier) ArchiveCasesRequestApproval(ctx context.Context, arg store.ArchiveCasesRequestApprovalParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesRequestApproval")
}

func (s *spyQuerier) ArchiveCasesSaveEvidence(ctx context.Context, arg store.ArchiveCasesSaveEvidenceParams) (store.ArchiveEvidenceExports, error) {
	var zero store.ArchiveEvidenceExports
	return zero, s.record("ArchiveCasesSaveEvidence")
}

func (s *spyQuerier) ArchiveCasesSnapshot(ctx context.Context, arg store.ArchiveCasesSnapshotParams) ([]byte, error) {
	var zero []byte
	return zero, s.record("ArchiveCasesSnapshot")
}

func (s *spyQuerier) ArchiveCasesSnapshotDigest(ctx context.Context, arg store.ArchiveCasesSnapshotDigestParams) (string, error) {
	var zero string
	return zero, s.record("ArchiveCasesSnapshotDigest")
}

func (s *spyQuerier) ArchiveCasesSources(ctx context.Context, arg store.ArchiveCasesSourcesParams) ([]store.ArchiveCasesSourcesRow, error) {
	var zero []store.ArchiveCasesSourcesRow
	return zero, s.record("ArchiveCasesSources")
}

func (s *spyQuerier) ArchiveCasesWithdrawApproval(ctx context.Context, arg store.ArchiveCasesWithdrawApprovalParams) (int64, error) {
	var zero int64
	return zero, s.record("ArchiveCasesWithdrawApproval")
}

func (s *spyQuerier) ArchiveRetentionCandidateDigest(ctx context.Context, arg store.ArchiveRetentionCandidateDigestParams) (store.ArchiveRetentionCandidateDigestRow, error) {
	var zero store.ArchiveRetentionCandidateDigestRow
	return zero, s.record("ArchiveRetentionCandidateDigest")
}

func (s *spyQuerier) AuditLegacyCredentialUse(ctx context.Context, arg store.AuditLegacyCredentialUseParams) error {
	return s.record("AuditLegacyCredentialUse")
}

func (s *spyQuerier) AuthenticateApplicationCredential(ctx context.Context, arg store.AuthenticateApplicationCredentialParams) (store.AuthenticateApplicationCredentialRow, error) {
	var zero store.AuthenticateApplicationCredentialRow
	return zero, s.record("AuthenticateApplicationCredential")
}

func (s *spyQuerier) CancelDNSPlan(ctx context.Context, arg store.CancelDNSPlanParams) error {
	return s.record("CancelDNSPlan")
}

func (s *spyQuerier) CancelQueuedMarketingRecipients(ctx context.Context, arg store.CancelQueuedMarketingRecipientsParams) (int64, error) {
	var zero int64
	return zero, s.record("CancelQueuedMarketingRecipients")
}

func (s *spyQuerier) ChangeMarketingCampaignState(ctx context.Context, arg store.ChangeMarketingCampaignStateParams) (store.MarketingCampaigns, error) {
	var zero store.MarketingCampaigns
	return zero, s.record("ChangeMarketingCampaignState")
}

func (s *spyQuerier) ChangeSupportTicketStatus(ctx context.Context, arg store.ChangeSupportTicketStatusParams) (store.Tickets, error) {
	var zero store.Tickets
	return zero, s.record("ChangeSupportTicketStatus")
}

func (s *spyQuerier) CheckMarketingSuppression(ctx context.Context, arg store.CheckMarketingSuppressionParams) (bool, error) {
	var zero bool
	return zero, s.record("CheckMarketingSuppression")
}

func (s *spyQuerier) CheckPlatformAdmin(ctx context.Context, userID int64) (bool, error) {
	var zero bool
	return zero, s.record("CheckPlatformAdmin")
}

func (s *spyQuerier) ClaimEASSend(ctx context.Context, arg store.ClaimEASSendParams) (int64, error) {
	var zero int64
	return zero, s.record("ClaimEASSend")
}

func (s *spyQuerier) ClearArchiveEntryLegalHold(ctx context.Context, arg store.ClearArchiveEntryLegalHoldParams) (store.ArchiveEntries, error) {
	var zero store.ArchiveEntries
	return zero, s.record("ClearArchiveEntryLegalHold")
}

func (s *spyQuerier) ClearExpiredMailboxImportPayloads(ctx context.Context, tenantID int64) error {
	return s.record("ClearExpiredMailboxImportPayloads")
}

func (s *spyQuerier) ConfirmArchiveRetentionPreview(ctx context.Context, arg store.ConfirmArchiveRetentionPreviewParams) error {
	return s.record("ConfirmArchiveRetentionPreview")
}

func (s *spyQuerier) ConfirmDNSPlan(ctx context.Context, arg store.ConfirmDNSPlanParams) error {
	return s.record("ConfirmDNSPlan")
}

func (s *spyQuerier) ConfirmRecallReview(ctx context.Context, arg store.ConfirmRecallReviewParams) (store.RecallRequests, error) {
	var zero store.RecallRequests
	return zero, s.record("ConfirmRecallReview")
}

func (s *spyQuerier) ConsumeRecoveryCode(ctx context.Context, arg store.ConsumeRecoveryCodeParams) (int64, error) {
	var zero int64
	return zero, s.record("ConsumeRecoveryCode")
}

func (s *spyQuerier) ConsumeUserRecoveryCode(ctx context.Context, userID int64, hash string) (int64, error) {
	var zero int64
	return zero, s.record("ConsumeUserRecoveryCode")
}

func (s *spyQuerier) CountDeliveryEventsByStatus(ctx context.Context, arg store.CountDeliveryEventsByStatusParams) ([]store.CountDeliveryEventsByStatusRow, error) {
	var zero []store.CountDeliveryEventsByStatusRow
	return zero, s.record("CountDeliveryEventsByStatus")
}

func (s *spyQuerier) CountDomainsByTenant(ctx context.Context, tenantID int64) (int64, error) {
	var zero int64
	return zero, s.record("CountDomainsByTenant")
}

func (s *spyQuerier) CountMailboxesByTenant(ctx context.Context, tenantID int64) (int64, error) {
	var zero int64
	return zero, s.record("CountMailboxesByTenant")
}

func (s *spyQuerier) CountMarketingFlowDefinitions(ctx context.Context, tenantID int64) (int64, error) {
	var zero int64
	return zero, s.record("CountMarketingFlowDefinitions")
}

func (s *spyQuerier) CountMarketingFlowLiveInstances(ctx context.Context, tenantID int64) (int64, error) {
	var zero int64
	return zero, s.record("CountMarketingFlowLiveInstances")
}

func (s *spyQuerier) CreateAlias(ctx context.Context, arg store.CreateAliasParams) (store.MailboxAliases, error) {
	var zero store.MailboxAliases
	return zero, s.record("CreateAlias")
}

func (s *spyQuerier) CreateApplicationCredential(ctx context.Context, arg store.CreateApplicationCredentialParams) (store.CreateApplicationCredentialRow, error) {
	var zero store.CreateApplicationCredentialRow
	return zero, s.record("CreateApplicationCredential")
}

func (s *spyQuerier) CreateAuditLog(ctx context.Context, arg store.CreateAuditLogParams) (store.AuditLogs, error) {
	var zero store.AuditLogs
	return zero, s.record("CreateAuditLog")
}

func (s *spyQuerier) CreateChannelAttribution(ctx context.Context, arg store.CreateChannelAttributionParams) (store.ChannelAttribution, error) {
	var zero store.ChannelAttribution
	return zero, s.record("CreateChannelAttribution")
}

func (s *spyQuerier) CreateCommission(ctx context.Context, arg store.CreateCommissionParams) (store.Commissions, error) {
	var zero store.Commissions
	return zero, s.record("CreateCommission")
}

func (s *spyQuerier) CreateComplianceGrant(ctx context.Context, arg store.CreateComplianceGrantParams) (store.ComplianceGrants, error) {
	var zero store.ComplianceGrants
	return zero, s.record("CreateComplianceGrant")
}

func (s *spyQuerier) CreateDNSPlan(ctx context.Context, arg store.CreateDNSPlanParams) (pgtype.UUID, error) {
	var zero pgtype.UUID
	return zero, s.record("CreateDNSPlan")
}

func (s *spyQuerier) CreateDomain(ctx context.Context, arg store.CreateDomainParams) (store.CreateDomainRow, error) {
	var zero store.CreateDomainRow
	return zero, s.record("CreateDomain")
}

func (s *spyQuerier) CreateMailbox(ctx context.Context, arg store.CreateMailboxParams) (store.Mailboxes, error) {
	var zero store.Mailboxes
	return zero, s.record("CreateMailbox")
}

func (s *spyQuerier) CreateMailboxImportItem(ctx context.Context, arg store.CreateMailboxImportItemParams) error {
	return s.record("CreateMailboxImportItem")
}

func (s *spyQuerier) CreateMailboxImportJob(ctx context.Context, arg store.CreateMailboxImportJobParams) (store.MailboxImportJobs, error) {
	var zero store.MailboxImportJobs
	return zero, s.record("CreateMailboxImportJob")
}

func (s *spyQuerier) CreateMarketingCampaign(ctx context.Context, arg store.CreateMarketingCampaignParams) (store.MarketingCampaigns, error) {
	var zero store.MarketingCampaigns
	return zero, s.record("CreateMarketingCampaign")
}

func (s *spyQuerier) CreateMarketingFlowContentSnapshot(ctx context.Context, arg store.CreateMarketingFlowContentSnapshotParams) error {
	return s.record("CreateMarketingFlowContentSnapshot")
}

func (s *spyQuerier) CreateMarketingFlowDefinition(ctx context.Context, arg store.CreateMarketingFlowDefinitionParams) (store.MarketingFlowDefinitions, error) {
	var zero store.MarketingFlowDefinitions
	return zero, s.record("CreateMarketingFlowDefinition")
}

func (s *spyQuerier) CreateMarketingFlowEntry(ctx context.Context, arg store.CreateMarketingFlowEntryParams) (store.MarketingFlowEntries, error) {
	var zero store.MarketingFlowEntries
	return zero, s.record("CreateMarketingFlowEntry")
}

func (s *spyQuerier) CreateMarketingFlowInstance(ctx context.Context, arg store.CreateMarketingFlowInstanceParams) (int64, error) {
	var zero int64
	return zero, s.record("CreateMarketingFlowInstance")
}

func (s *spyQuerier) CreateMarketingFlowVersion(ctx context.Context, arg store.CreateMarketingFlowVersionParams) (store.MarketingFlowVersions, error) {
	var zero store.MarketingFlowVersions
	return zero, s.record("CreateMarketingFlowVersion")
}

func (s *spyQuerier) CreateMarketingList(ctx context.Context, arg store.CreateMarketingListParams) (store.MarketingLists, error) {
	var zero store.MarketingLists
	return zero, s.record("CreateMarketingList")
}

func (s *spyQuerier) CreateMarketingRuntimeFlow(ctx context.Context, arg store.CreateMarketingRuntimeFlowParams) (int64, error) {
	var zero int64
	return zero, s.record("CreateMarketingRuntimeFlow")
}

func (s *spyQuerier) CreateMarketingRuntimeStep(ctx context.Context, arg store.CreateMarketingRuntimeStepParams) error {
	return s.record("CreateMarketingRuntimeStep")
}

func (s *spyQuerier) CreateMarketingTemplate(ctx context.Context, arg store.CreateMarketingTemplateParams) (store.MarketingTemplates, error) {
	var zero store.MarketingTemplates
	return zero, s.record("CreateMarketingTemplate")
}

func (s *spyQuerier) CreateOnboardingTask(ctx context.Context, arg store.CreateOnboardingTaskParams) (store.OnboardingTasks, error) {
	var zero store.OnboardingTasks
	return zero, s.record("CreateOnboardingTask")
}

func (s *spyQuerier) CreateOrderOnboardingTask(ctx context.Context, arg store.CreateOrderOnboardingTaskParams) (store.OnboardingTasks, error) {
	var zero store.OnboardingTasks
	return zero, s.record("CreateOrderOnboardingTask")
}

func (s *spyQuerier) CreateRecall(ctx context.Context, arg store.CreateRecallParams) (store.RecallRequests, error) {
	var zero store.RecallRequests
	return zero, s.record("CreateRecall")
}

func (s *spyQuerier) CreateSession(ctx context.Context, arg store.CreateSessionParams) (store.Sessions, error) {
	var zero store.Sessions
	return zero, s.record("CreateSession")
}

func (s *spyQuerier) CreateSupportTicket(ctx context.Context, arg store.CreateSupportTicketParams) (store.Tickets, error) {
	var zero store.Tickets
	return zero, s.record("CreateSupportTicket")
}

func (s *spyQuerier) DeleteAlias(ctx context.Context, arg store.DeleteAliasParams) error {
	return s.record("DeleteAlias")
}

func (s *spyQuerier) DeleteDomain(ctx context.Context, arg store.DeleteDomainParams) error {
	return s.record("DeleteDomain")
}

func (s *spyQuerier) DeleteExpiredSessions(ctx context.Context) error {
	return s.record("DeleteExpiredSessions")
}

func (s *spyQuerier) DeleteMailbox(ctx context.Context, arg store.DeleteMailboxParams) error {
	return s.record("DeleteMailbox")
}

func (s *spyQuerier) DeleteMailboxAIChunkFolder(ctx context.Context, arg store.DeleteMailboxAIChunkFolderParams) (int64, error) {
	var zero int64
	return zero, s.record("DeleteMailboxAIChunkFolder")
}

func (s *spyQuerier) DeleteMailboxAIChunkMessages(ctx context.Context, arg store.DeleteMailboxAIChunkMessagesParams) (int64, error) {
	var zero int64
	return zero, s.record("DeleteMailboxAIChunkMessages")
}

func (s *spyQuerier) DeleteOtherUserSessions(ctx context.Context, arg store.DeleteOtherUserSessionsParams) error {
	return s.record("DeleteOtherUserSessions")
}

func (s *spyQuerier) DeleteSession(ctx context.Context, id string) error {
	return s.record("DeleteSession")
}

func (s *spyQuerier) DeleteTenantSetting(ctx context.Context, arg store.DeleteTenantSettingParams) error {
	return s.record("DeleteTenantSetting")
}

func (s *spyQuerier) DeleteUserSession(ctx context.Context, arg store.DeleteUserSessionParams) (int64, error) {
	var zero int64
	return zero, s.record("DeleteUserSession")
}

func (s *spyQuerier) DisableDomainTransportSecurity(ctx context.Context, arg store.DisableDomainTransportSecurityParams) (int64, error) {
	var zero int64
	return zero, s.record("DisableDomainTransportSecurity")
}

func (s *spyQuerier) DisableTenantRetentionPolicy(ctx context.Context, tenantID int64) error {
	return s.record("DisableTenantRetentionPolicy")
}

func (s *spyQuerier) DisableUserMFA(ctx context.Context, id int64) error {
	return s.record("DisableUserMFA")
}

func (s *spyQuerier) EnableUserMFA(ctx context.Context, arg store.EnableUserMFAParams) error {
	return s.record("EnableUserMFA")
}

func (s *spyQuerier) ExportAuditLogsByTenant(ctx context.Context, arg store.ExportAuditLogsByTenantParams) ([]store.AuditLogs, error) {
	var zero []store.AuditLogs
	return zero, s.record("ExportAuditLogsByTenant")
}

func (s *spyQuerier) FinishEASSend(ctx context.Context, arg store.FinishEASSendParams) error {
	return s.record("FinishEASSend")
}

func (s *spyQuerier) GetAliasByDomainAndLocalPart(ctx context.Context, arg store.GetAliasByDomainAndLocalPartParams) (store.GetAliasByDomainAndLocalPartRow, error) {
	var zero store.GetAliasByDomainAndLocalPartRow
	return zero, s.record("GetAliasByDomainAndLocalPart")
}

func (s *spyQuerier) GetAliasByID(ctx context.Context, arg store.GetAliasByIDParams) (store.GetAliasByIDRow, error) {
	var zero store.GetAliasByIDRow
	return zero, s.record("GetAliasByID")
}

func (s *spyQuerier) GetArchiveContent(ctx context.Context, arg store.GetArchiveContentParams) (store.GetArchiveContentRow, error) {
	var zero store.GetArchiveContentRow
	return zero, s.record("GetArchiveContent")
}

func (s *spyQuerier) GetArchiveEntryByID(ctx context.Context, arg store.GetArchiveEntryByIDParams) (store.ArchiveEntries, error) {
	var zero store.ArchiveEntries
	return zero, s.record("GetArchiveEntryByID")
}

func (s *spyQuerier) GetArchiveEntryLegalHold(ctx context.Context, arg store.GetArchiveEntryLegalHoldParams) (bool, error) {
	var zero bool
	return zero, s.record("GetArchiveEntryLegalHold")
}

func (s *spyQuerier) GetArchiveOriginalForRecovery(ctx context.Context, arg store.GetArchiveOriginalForRecoveryParams) (store.GetArchiveOriginalForRecoveryRow, error) {
	var zero store.GetArchiveOriginalForRecoveryRow
	return zero, s.record("GetArchiveOriginalForRecovery")
}

func (s *spyQuerier) GetAuditCursorAnchor(ctx context.Context, arg store.GetAuditCursorAnchorParams) (int64, error) {
	var zero int64
	return zero, s.record("GetAuditCursorAnchor")
}

func (s *spyQuerier) GetBillingBusinessTimezone(ctx context.Context, dollar_1 int64) (string, error) {
	var zero string
	return zero, s.record("GetBillingBusinessTimezone")
}

func (s *spyQuerier) GetChannelAttributionByTenant(ctx context.Context, tenantID int64) (store.ChannelAttribution, error) {
	var zero store.ChannelAttribution
	return zero, s.record("GetChannelAttributionByTenant")
}

func (s *spyQuerier) GetChannelByCode(ctx context.Context, code string) (store.Channels, error) {
	var zero store.Channels
	return zero, s.record("GetChannelByCode")
}

func (s *spyQuerier) GetComplianceGrant(ctx context.Context, arg store.GetComplianceGrantParams) (store.ComplianceGrants, error) {
	var zero store.ComplianceGrants
	return zero, s.record("GetComplianceGrant")
}

func (s *spyQuerier) GetDNSPlan(ctx context.Context, arg store.GetDNSPlanParams) (store.DnsChangePlans, error) {
	var zero store.DnsChangePlans
	return zero, s.record("GetDNSPlan")
}

func (s *spyQuerier) GetDeliveryEventTenantByQueueID(ctx context.Context, arg store.GetDeliveryEventTenantByQueueIDParams) (store.GetDeliveryEventTenantByQueueIDRow, error) {
	var zero store.GetDeliveryEventTenantByQueueIDRow
	return zero, s.record("GetDeliveryEventTenantByQueueID")
}

func (s *spyQuerier) GetDomainByFQDN(ctx context.Context, arg store.GetDomainByFQDNParams) (store.GetDomainByFQDNRow, error) {
	var zero store.GetDomainByFQDNRow
	return zero, s.record("GetDomainByFQDN")
}

func (s *spyQuerier) GetDomainByID(ctx context.Context, arg store.GetDomainByIDParams) (store.GetDomainByIDRow, error) {
	var zero store.GetDomainByIDRow
	return zero, s.record("GetDomainByID")
}

func (s *spyQuerier) GetDomainProvisioning(ctx context.Context, arg store.GetDomainProvisioningParams) (store.GetDomainProvisioningRow, error) {
	var zero store.GetDomainProvisioningRow
	return zero, s.record("GetDomainProvisioning")
}

func (s *spyQuerier) GetDomainTransportSecurity(ctx context.Context, arg store.GetDomainTransportSecurityParams) (store.DomainTransportSecurity, error) {
	var zero store.DomainTransportSecurity
	return zero, s.record("GetDomainTransportSecurity")
}

func (s *spyQuerier) GetEASSend(ctx context.Context, arg store.GetEASSendParams) (store.GetEASSendRow, error) {
	var zero store.GetEASSendRow
	return zero, s.record("GetEASSend")
}

func (s *spyQuerier) GetLegacySession(ctx context.Context, id string) (store.GetLegacySessionRow, error) {
	var zero store.GetLegacySessionRow
	return zero, s.record("GetLegacySession")
}

func (s *spyQuerier) GetMailboxAIChunk(ctx context.Context, arg store.GetMailboxAIChunkParams) (store.GetMailboxAIChunkRow, error) {
	var zero store.GetMailboxAIChunkRow
	return zero, s.record("GetMailboxAIChunk")
}

func (s *spyQuerier) GetMailboxByAddress(ctx context.Context, arg store.GetMailboxByAddressParams) (store.Mailboxes, error) {
	var zero store.Mailboxes
	return zero, s.record("GetMailboxByAddress")
}

func (s *spyQuerier) GetMailboxByID(ctx context.Context, arg store.GetMailboxByIDParams) (store.Mailboxes, error) {
	var zero store.Mailboxes
	return zero, s.record("GetMailboxByID")
}

func (s *spyQuerier) GetMailboxDailySent(ctx context.Context, address string) (store.GetMailboxDailySentRow, error) {
	var zero store.GetMailboxDailySentRow
	return zero, s.record("GetMailboxDailySent")
}

func (s *spyQuerier) GetMailboxDailySentForUpdate(ctx context.Context, address string) (store.GetMailboxDailySentForUpdateRow, error) {
	var zero store.GetMailboxDailySentForUpdateRow
	return zero, s.record("GetMailboxDailySentForUpdate")
}

func (s *spyQuerier) GetMailboxEASPingConfig(ctx context.Context, arg store.GetMailboxEASPingConfigParams) (store.GetMailboxEASPingConfigRow, error) {
	var zero store.GetMailboxEASPingConfigRow
	return zero, s.record("GetMailboxEASPingConfig")
}

func (s *spyQuerier) GetMailboxImportByKey(ctx context.Context, arg store.GetMailboxImportByKeyParams) (store.MailboxImportJobs, error) {
	var zero store.MailboxImportJobs
	return zero, s.record("GetMailboxImportByKey")
}

func (s *spyQuerier) GetMailboxImportJob(ctx context.Context, arg store.GetMailboxImportJobParams) (store.MailboxImportJobs, error) {
	var zero store.MailboxImportJobs
	return zero, s.record("GetMailboxImportJob")
}

func (s *spyQuerier) GetMarketingCampaign(ctx context.Context, arg store.GetMarketingCampaignParams) (store.MarketingCampaigns, error) {
	var zero store.MarketingCampaigns
	return zero, s.record("GetMarketingCampaign")
}

func (s *spyQuerier) GetMarketingCampaignReport(ctx context.Context, arg store.GetMarketingCampaignReportParams) (store.GetMarketingCampaignReportRow, error) {
	var zero store.GetMarketingCampaignReportRow
	return zero, s.record("GetMarketingCampaignReport")
}

func (s *spyQuerier) GetMarketingFlowDefinition(ctx context.Context, arg store.GetMarketingFlowDefinitionParams) (store.MarketingFlowDefinitions, error) {
	var zero store.MarketingFlowDefinitions
	return zero, s.record("GetMarketingFlowDefinition")
}

func (s *spyQuerier) GetMarketingFlowEntry(ctx context.Context, arg store.GetMarketingFlowEntryParams) (store.MarketingFlowEntries, error) {
	var zero store.MarketingFlowEntries
	return zero, s.record("GetMarketingFlowEntry")
}

func (s *spyQuerier) GetMarketingFlowInstance(ctx context.Context, arg store.GetMarketingFlowInstanceParams) (int64, error) {
	var zero int64
	return zero, s.record("GetMarketingFlowInstance")
}

func (s *spyQuerier) GetMarketingFlowList(ctx context.Context, arg store.GetMarketingFlowListParams) (int64, error) {
	var zero int64
	return zero, s.record("GetMarketingFlowList")
}

func (s *spyQuerier) GetMarketingFlowTemplate(ctx context.Context, arg store.GetMarketingFlowTemplateParams) (store.GetMarketingFlowTemplateRow, error) {
	var zero store.GetMarketingFlowTemplateRow
	return zero, s.record("GetMarketingFlowTemplate")
}

func (s *spyQuerier) GetMarketingFlowVersion(ctx context.Context, arg store.GetMarketingFlowVersionParams) (store.MarketingFlowVersions, error) {
	var zero store.MarketingFlowVersions
	return zero, s.record("GetMarketingFlowVersion")
}

func (s *spyQuerier) GetMarketingFlowVersionForRevision(ctx context.Context, arg store.GetMarketingFlowVersionForRevisionParams) (store.MarketingFlowVersions, error) {
	var zero store.MarketingFlowVersions
	return zero, s.record("GetMarketingFlowVersionForRevision")
}

func (s *spyQuerier) GetMarketingSuppression(ctx context.Context, arg store.GetMarketingSuppressionParams) (store.MarketingSuppressions, error) {
	var zero store.MarketingSuppressions
	return zero, s.record("GetMarketingSuppression")
}

func (s *spyQuerier) GetOnboardingTaskByDomain(ctx context.Context, arg store.GetOnboardingTaskByDomainParams) (store.OnboardingTasks, error) {
	var zero store.OnboardingTasks
	return zero, s.record("GetOnboardingTaskByDomain")
}

func (s *spyQuerier) GetRecallByEntry(ctx context.Context, arg store.GetRecallByEntryParams) (store.RecallRequests, error) {
	var zero store.RecallRequests
	return zero, s.record("GetRecallByEntry")
}

func (s *spyQuerier) GetRecallByID(ctx context.Context, arg store.GetRecallByIDParams) (store.RecallRequests, error) {
	var zero store.RecallRequests
	return zero, s.record("GetRecallByID")
}

func (s *spyQuerier) GetRecallReviewPreview(ctx context.Context, arg store.GetRecallReviewPreviewParams) (store.RecallRequests, error) {
	var zero store.RecallRequests
	return zero, s.record("GetRecallReviewPreview")
}

func (s *spyQuerier) GetRecallTarget(ctx context.Context, arg store.GetRecallTargetParams) (store.GetRecallTargetRow, error) {
	var zero store.GetRecallTargetRow
	return zero, s.record("GetRecallTarget")
}

func (s *spyQuerier) GetRetentionPolicy(ctx context.Context, tenantID int64) (store.RetentionPolicies, error) {
	var zero store.RetentionPolicies
	return zero, s.record("GetRetentionPolicy")
}

func (s *spyQuerier) GetRetentionPolicyAuthority(ctx context.Context, arg store.GetRetentionPolicyAuthorityParams) (bool, error) {
	var zero bool
	return zero, s.record("GetRetentionPolicyAuthority")
}

func (s *spyQuerier) GetSessionByID(ctx context.Context, id string) (store.Sessions, error) {
	var zero store.Sessions
	return zero, s.record("GetSessionByID")
}

func (s *spyQuerier) GetSupportAttachment(ctx context.Context, arg store.GetSupportAttachmentParams) (store.TicketAttachments, error) {
	var zero store.TicketAttachments
	return zero, s.record("GetSupportAttachment")
}

func (s *spyQuerier) GetSupportTicket(ctx context.Context, arg store.GetSupportTicketParams) (store.Tickets, error) {
	var zero store.Tickets
	return zero, s.record("GetSupportTicket")
}

func (s *spyQuerier) GetTenantByID(ctx context.Context, id int64) (store.Tenants, error) {
	var zero store.Tenants
	return zero, s.record("GetTenantByID")
}

func (s *spyQuerier) GetTenantBySlug(ctx context.Context, arg store.GetTenantBySlugParams) (store.Tenants, error) {
	var zero store.Tenants
	return zero, s.record("GetTenantBySlug")
}

func (s *spyQuerier) GetTenantIDBySenderDomain(ctx context.Context, fqdn string) (int64, error) {
	var zero int64
	return zero, s.record("GetTenantIDBySenderDomain")
}

func (s *spyQuerier) GetTenantSetting(ctx context.Context, arg store.GetTenantSettingParams) (store.Settings, error) {
	var zero store.Settings
	return zero, s.record("GetTenantSetting")
}

func (s *spyQuerier) GetTenantStorageUsage(ctx context.Context, tenantID int64) (store.GetTenantStorageUsageRow, error) {
	var zero store.GetTenantStorageUsageRow
	return zero, s.record("GetTenantStorageUsage")
}

func (s *spyQuerier) GetUserByEmail(ctx context.Context, email string) (store.GetUserByEmailRow, error) {
	var zero store.GetUserByEmailRow
	return zero, s.record("GetUserByEmail")
}

func (s *spyQuerier) GetUserByID(ctx context.Context, id int64) (store.GetUserByIDRow, error) {
	var zero store.GetUserByIDRow
	return zero, s.record("GetUserByID")
}

func (s *spyQuerier) GetUserRoleForTenant(ctx context.Context, arg store.GetUserRoleForTenantParams) (string, error) {
	var zero string
	return zero, s.record("GetUserRoleForTenant")
}

func (s *spyQuerier) GetUserTenants(ctx context.Context, userID int64) ([]store.GetUserTenantsRow, error) {
	var zero []store.GetUserTenantsRow
	return zero, s.record("GetUserTenants")
}

func (s *spyQuerier) IncrementMailboxDailySent(ctx context.Context, arg store.IncrementMailboxDailySentParams) (int32, error) {
	var zero int32
	return zero, s.record("IncrementMailboxDailySent")
}

func (s *spyQuerier) InsertArchiveEntry(ctx context.Context, arg store.InsertArchiveEntryParams) (store.ArchiveEntries, error) {
	var zero store.ArchiveEntries
	return zero, s.record("InsertArchiveEntry")
}

func (s *spyQuerier) InsertMailboxAIChunk(ctx context.Context, arg store.InsertMailboxAIChunkParams) (int64, error) {
	var zero int64
	return zero, s.record("InsertMailboxAIChunk")
}

func (s *spyQuerier) ListAliasesByTenant(ctx context.Context, tenantID int64) ([]store.ListAliasesByTenantRow, error) {
	var zero []store.ListAliasesByTenantRow
	return zero, s.record("ListAliasesByTenant")
}

func (s *spyQuerier) ListApplicationCredentials(ctx context.Context, userID int64) ([]store.ListApplicationCredentialsRow, error) {
	var zero []store.ListApplicationCredentialsRow
	return zero, s.record("ListApplicationCredentials")
}

func (s *spyQuerier) ListArchiveEntriesByTenant(ctx context.Context, arg store.ListArchiveEntriesByTenantParams) ([]store.ArchiveEntries, error) {
	var zero []store.ArchiveEntries
	return zero, s.record("ListArchiveEntriesByTenant")
}

func (s *spyQuerier) ListAuditLogsByTenant(ctx context.Context, arg store.ListAuditLogsByTenantParams) ([]store.AuditLogs, error) {
	var zero []store.AuditLogs
	return zero, s.record("ListAuditLogsByTenant")
}

func (s *spyQuerier) ListComplianceGrantRecipients(ctx context.Context, tenantID int64) ([]store.ListComplianceGrantRecipientsRow, error) {
	var zero []store.ListComplianceGrantRecipientsRow
	return zero, s.record("ListComplianceGrantRecipients")
}

func (s *spyQuerier) ListComplianceGrantsByTenant(ctx context.Context, tenantID int64) ([]store.ComplianceGrants, error) {
	var zero []store.ComplianceGrants
	return zero, s.record("ListComplianceGrantsByTenant")
}

func (s *spyQuerier) ListDNSActions(ctx context.Context, arg store.ListDNSActionsParams) ([]store.DnsRecordActions, error) {
	var zero []store.DnsRecordActions
	return zero, s.record("ListDNSActions")
}

func (s *spyQuerier) ListDNSCredentials(ctx context.Context, arg store.ListDNSCredentialsParams) ([]store.ListDNSCredentialsRow, error) {
	var zero []store.ListDNSCredentialsRow
	return zero, s.record("ListDNSCredentials")
}

func (s *spyQuerier) ListDNSPlans(ctx context.Context, arg store.ListDNSPlansParams) ([]store.DnsChangePlans, error) {
	var zero []store.DnsChangePlans
	return zero, s.record("ListDNSPlans")
}

func (s *spyQuerier) ListDeliveryEventsByTenant(ctx context.Context, tenantID pgtype.Int8) ([]store.DeliveryEvents, error) {
	var zero []store.DeliveryEvents
	return zero, s.record("ListDeliveryEventsByTenant")
}

func (s *spyQuerier) ListDomainTLSReports(ctx context.Context, arg store.ListDomainTLSReportsParams) ([]store.ListDomainTLSReportsRow, error) {
	var zero []store.ListDomainTLSReportsRow
	return zero, s.record("ListDomainTLSReports")
}

func (s *spyQuerier) ListDomainsByTenant(ctx context.Context, tenantID int64) ([]store.ListDomainsByTenantRow, error) {
	var zero []store.ListDomainsByTenantRow
	return zero, s.record("ListDomainsByTenant")
}

func (s *spyQuerier) ListMailboxImportItems(ctx context.Context, arg store.ListMailboxImportItemsParams) ([]store.MailboxImportItems, error) {
	var zero []store.MailboxImportItems
	return zero, s.record("ListMailboxImportItems")
}

func (s *spyQuerier) ListMailboxImportJobs(ctx context.Context, arg store.ListMailboxImportJobsParams) ([]store.MailboxImportJobs, error) {
	var zero []store.MailboxImportJobs
	return zero, s.record("ListMailboxImportJobs")
}

func (s *spyQuerier) ListMailboxesByDomain(ctx context.Context, arg store.ListMailboxesByDomainParams) ([]store.Mailboxes, error) {
	var zero []store.Mailboxes
	return zero, s.record("ListMailboxesByDomain")
}

func (s *spyQuerier) ListMailboxesByTenant(ctx context.Context, tenantID int64) ([]store.Mailboxes, error) {
	var zero []store.Mailboxes
	return zero, s.record("ListMailboxesByTenant")
}

func (s *spyQuerier) ListMailboxesWithLegalHold(ctx context.Context, tenantID int64) ([]pgtype.Int8, error) {
	var zero []pgtype.Int8
	return zero, s.record("ListMailboxesWithLegalHold")
}

func (s *spyQuerier) ListMarketingCampaigns(ctx context.Context, tenantID int64) ([]store.MarketingCampaigns, error) {
	var zero []store.MarketingCampaigns
	return zero, s.record("ListMarketingCampaigns")
}

func (s *spyQuerier) ListMarketingChannels(ctx context.Context, tenantID int64) ([]store.ListMarketingChannelsRow, error) {
	var zero []store.ListMarketingChannelsRow
	return zero, s.record("ListMarketingChannels")
}

func (s *spyQuerier) ListMarketingContacts(ctx context.Context, arg store.ListMarketingContactsParams) ([]store.MarketingContacts, error) {
	var zero []store.MarketingContacts
	return zero, s.record("ListMarketingContacts")
}

func (s *spyQuerier) ListMarketingFlowDefinitions(ctx context.Context, tenantID int64) ([]store.ListMarketingFlowDefinitionsRow, error) {
	var zero []store.ListMarketingFlowDefinitionsRow
	return zero, s.record("ListMarketingFlowDefinitions")
}

func (s *spyQuerier) ListMarketingFlowEligibleContacts(ctx context.Context, arg store.ListMarketingFlowEligibleContactsParams) ([]string, error) {
	var zero []string
	return zero, s.record("ListMarketingFlowEligibleContacts")
}

func (s *spyQuerier) ListMarketingFlowInstances(ctx context.Context, arg store.ListMarketingFlowInstancesParams) ([]store.ListMarketingFlowInstancesRow, error) {
	var zero []store.ListMarketingFlowInstancesRow
	return zero, s.record("ListMarketingFlowInstances")
}

func (s *spyQuerier) ListMarketingFlowNodeExecutions(ctx context.Context, arg store.ListMarketingFlowNodeExecutionsParams) ([]store.ListMarketingFlowNodeExecutionsRow, error) {
	var zero []store.ListMarketingFlowNodeExecutionsRow
	return zero, s.record("ListMarketingFlowNodeExecutions")
}

func (s *spyQuerier) ListMarketingFlowPublishedContent(ctx context.Context, arg store.ListMarketingFlowPublishedContentParams) ([]store.ListMarketingFlowPublishedContentRow, error) {
	var zero []store.ListMarketingFlowPublishedContentRow
	return zero, s.record("ListMarketingFlowPublishedContent")
}

func (s *spyQuerier) ListMarketingFlowVersions(ctx context.Context, arg store.ListMarketingFlowVersionsParams) ([]store.ListMarketingFlowVersionsRow, error) {
	var zero []store.ListMarketingFlowVersionsRow
	return zero, s.record("ListMarketingFlowVersions")
}

func (s *spyQuerier) ListMarketingLists(ctx context.Context, tenantID int64) ([]store.MarketingLists, error) {
	var zero []store.MarketingLists
	return zero, s.record("ListMarketingLists")
}

func (s *spyQuerier) ListMarketingRecipients(ctx context.Context, arg store.ListMarketingRecipientsParams) ([]store.MarketingRecipients, error) {
	var zero []store.MarketingRecipients
	return zero, s.record("ListMarketingRecipients")
}

func (s *spyQuerier) ListMarketingSuppressions(ctx context.Context, tenantID int64) ([]store.MarketingSuppressions, error) {
	var zero []store.MarketingSuppressions
	return zero, s.record("ListMarketingSuppressions")
}

func (s *spyQuerier) ListMarketingTemplates(ctx context.Context, tenantID int64) ([]store.MarketingTemplates, error) {
	var zero []store.MarketingTemplates
	return zero, s.record("ListMarketingTemplates")
}

func (s *spyQuerier) ListOnboardingTasksByTenant(ctx context.Context, tenantID int64) ([]store.OnboardingTasks, error) {
	var zero []store.OnboardingTasks
	return zero, s.record("ListOnboardingTasksByTenant")
}

func (s *spyQuerier) ListRecalls(ctx context.Context, tenantID int64) ([]store.RecallRequests, error) {
	var zero []store.RecallRequests
	return zero, s.record("ListRecalls")
}

func (s *spyQuerier) ListSessionsForUser(ctx context.Context, userID int64) ([]store.ListSessionsForUserRow, error) {
	var zero []store.ListSessionsForUserRow
	return zero, s.record("ListSessionsForUser")
}

func (s *spyQuerier) ListSettingsByScopes(ctx context.Context, arg store.ListSettingsByScopesParams) ([]store.Settings, error) {
	var zero []store.Settings
	return zero, s.record("ListSettingsByScopes")
}

func (s *spyQuerier) ListSupportAttachments(ctx context.Context, arg store.ListSupportAttachmentsParams) ([]store.ListSupportAttachmentsRow, error) {
	var zero []store.ListSupportAttachmentsRow
	return zero, s.record("ListSupportAttachments")
}

func (s *spyQuerier) ListSupportMessages(ctx context.Context, arg store.ListSupportMessagesParams) ([]store.TicketMessages, error) {
	var zero []store.TicketMessages
	return zero, s.record("ListSupportMessages")
}

func (s *spyQuerier) ListSupportTickets(ctx context.Context, arg store.ListSupportTicketsParams) ([]store.Tickets, error) {
	var zero []store.Tickets
	return zero, s.record("ListSupportTickets")
}

func (s *spyQuerier) ListTenantNotifications(ctx context.Context, arg store.ListTenantNotificationsParams) ([]store.ListTenantNotificationsRow, error) {
	var zero []store.ListTenantNotificationsRow
	return zero, s.record("ListTenantNotifications")
}

func (s *spyQuerier) ListVisibleAnnouncements(ctx context.Context, tenantID int64) ([]store.Announcements, error) {
	var zero []store.Announcements
	return zero, s.record("ListVisibleAnnouncements")
}

func (s *spyQuerier) LockArchiveRetentionPreview(ctx context.Context, arg store.LockArchiveRetentionPreviewParams) (store.RetentionPreviews, error) {
	var zero store.RetentionPreviews
	return zero, s.record("LockArchiveRetentionPreview")
}

func (s *spyQuerier) LockEASSendCopy(ctx context.Context, arg store.LockEASSendCopyParams) (store.LockEASSendCopyRow, error) {
	var zero store.LockEASSendCopyRow
	return zero, s.record("LockEASSendCopy")
}

func (s *spyQuerier) LockMailboxEASSync(ctx context.Context, p1 store.LockMailboxEASSyncParams) error {
	return s.record("LockMailboxEASSync")
}

func (s *spyQuerier) LockMailboxImportJob(ctx context.Context, arg store.LockMailboxImportJobParams) (store.MailboxImportJobs, error) {
	var zero store.MailboxImportJobs
	return zero, s.record("LockMailboxImportJob")
}

func (s *spyQuerier) LockMailboxImportOwner(ctx context.Context, arg store.LockMailboxImportOwnerParams) (store.LockMailboxImportOwnerRow, error) {
	var zero store.LockMailboxImportOwnerRow
	return zero, s.record("LockMailboxImportOwner")
}

func (s *spyQuerier) LockMailboxImportTenantBudget(ctx context.Context, p1 int64) error {
	return s.record("LockMailboxImportTenantBudget")
}

func (s *spyQuerier) MailServiceAllowed(ctx context.Context, dollar_1 int64) (bool, error) {
	var zero bool
	return zero, s.record("MailServiceAllowed")
}

func (s *spyQuerier) MailboxAIChunkMessageStates(ctx context.Context, arg store.MailboxAIChunkMessageStatesParams) ([]store.MailboxAIChunkMessageStatesRow, error) {
	var zero []store.MailboxAIChunkMessageStatesRow
	return zero, s.record("MailboxAIChunkMessageStates")
}

func (s *spyQuerier) MailboxEASPingCollectionChanged(ctx context.Context, arg store.MailboxEASPingCollectionChangedParams) (bool, error) {
	var zero bool
	return zero, s.record("MailboxEASPingCollectionChanged")
}

func (s *spyQuerier) MailboxImportItemViews(ctx context.Context, arg store.MailboxImportItemViewsParams) ([]byte, error) {
	var zero []byte
	return zero, s.record("MailboxImportItemViews")
}

func (s *spyQuerier) MailboxImportRecoverExpiredLeases(ctx context.Context, arg store.MailboxImportRecoverExpiredLeasesParams) error {
	return s.record("MailboxImportRecoverExpiredLeases")
}

func (s *spyQuerier) MailboxImportReservedBytes(ctx context.Context, arg store.MailboxImportReservedBytesParams) (int64, error) {
	var zero int64
	return zero, s.record("MailboxImportReservedBytes")
}

func (s *spyQuerier) MailboxImportStorageBudget(ctx context.Context, arg store.MailboxImportStorageBudgetParams) (store.MailboxImportStorageBudgetRow, error) {
	var zero store.MailboxImportStorageBudgetRow
	return zero, s.record("MailboxImportStorageBudget")
}

func (s *spyQuerier) MailboxSpamApplyTokenDelta(ctx context.Context, arg store.MailboxSpamApplyTokenDeltaParams) (int64, error) {
	var zero int64
	return zero, s.record("MailboxSpamApplyTokenDelta")
}

func (s *spyQuerier) MailboxSpamCountFeedback(ctx context.Context, arg store.MailboxSpamCountFeedbackParams) (int64, error) {
	var zero int64
	return zero, s.record("MailboxSpamCountFeedback")
}

func (s *spyQuerier) MailboxSpamInsertFeedback(ctx context.Context, arg store.MailboxSpamInsertFeedbackParams) (int64, error) {
	var zero int64
	return zero, s.record("MailboxSpamInsertFeedback")
}

func (s *spyQuerier) MailboxSpamListSenderRules(ctx context.Context, arg store.MailboxSpamListSenderRulesParams) ([]store.MailboxSpamListSenderRulesRow, error) {
	var zero []store.MailboxSpamListSenderRulesRow
	return zero, s.record("MailboxSpamListSenderRules")
}

func (s *spyQuerier) MailboxSpamRemoveSenderList(ctx context.Context, arg store.MailboxSpamRemoveSenderListParams) (int64, error) {
	var zero int64
	return zero, s.record("MailboxSpamRemoveSenderList")
}

func (s *spyQuerier) MailboxSpamSetSenderList(ctx context.Context, arg store.MailboxSpamSetSenderListParams) (int64, error) {
	var zero int64
	return zero, s.record("MailboxSpamSetSenderList")
}

func (s *spyQuerier) MailboxSpamTokenStats(ctx context.Context, arg store.MailboxSpamTokenStatsParams) ([]store.MailboxSpamTokenStatsRow, error) {
	var zero []store.MailboxSpamTokenStatsRow
	return zero, s.record("MailboxSpamTokenStats")
}

func (s *spyQuerier) MarkSessionLegacyOnly(ctx context.Context, id string) error {
	return s.record("MarkSessionLegacyOnly")
}

func (s *spyQuerier) MarkSessionMFAVerified(ctx context.Context, id string) error {
	return s.record("MarkSessionMFAVerified")
}

func (s *spyQuerier) MarketingCampaignReportSnapshot(ctx context.Context, arg store.MarketingCampaignReportSnapshotParams) ([]byte, error) {
	var zero []byte
	return zero, s.record("MarketingCampaignReportSnapshot")
}

func (s *spyQuerier) MarketingFlowReportSnapshot(ctx context.Context, arg store.MarketingFlowReportSnapshotParams) ([]byte, error) {
	var zero []byte
	return zero, s.record("MarketingFlowReportSnapshot")
}

func (s *spyQuerier) ObserveDomainTransportSecurity(ctx context.Context, arg store.ObserveDomainTransportSecurityParams) (int64, error) {
	var zero int64
	return zero, s.record("ObserveDomainTransportSecurity")
}

func (s *spyQuerier) PauseMarketingFlowDefinition(ctx context.Context, arg store.PauseMarketingFlowDefinitionParams) error {
	return s.record("PauseMarketingFlowDefinition")
}

func (s *spyQuerier) PauseMarketingRuntimeFlows(ctx context.Context, arg store.PauseMarketingRuntimeFlowsParams) error {
	return s.record("PauseMarketingRuntimeFlows")
}

func (s *spyQuerier) PlatformAIQuotaUsage(ctx context.Context, arg store.PlatformAIQuotaUsageParams) (store.PlatformAIQuotaUsageRow, error) {
	var zero store.PlatformAIQuotaUsageRow
	return zero, s.record("PlatformAIQuotaUsage")
}

func (s *spyQuerier) PlatformAIUsage(ctx context.Context, tenantID int64) ([]store.PlatformAIUsageRow, error) {
	var zero []store.PlatformAIUsageRow
	return zero, s.record("PlatformAIUsage")
}

func (s *spyQuerier) PlatformAcceptMailboxScheduledSend(ctx context.Context, arg store.PlatformAcceptMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformAcceptMailboxScheduledSend")
}

func (s *spyQuerier) PlatformAckMailboxPasswordRevocation(ctx context.Context, arg store.PlatformAckMailboxPasswordRevocationParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAckMailboxPasswordRevocation")
}

func (s *spyQuerier) PlatformAcknowledgeCalendarReminder(ctx context.Context, arg store.PlatformAcknowledgeCalendarReminderParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAcknowledgeCalendarReminder")
}

func (s *spyQuerier) PlatformAcknowledgeCollectorGap(ctx context.Context, arg store.PlatformAcknowledgeCollectorGapParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAcknowledgeCollectorGap")
}

func (s *spyQuerier) PlatformAcknowledgeUncertainScheduledSend(ctx context.Context, arg store.PlatformAcknowledgeUncertainScheduledSendParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformAcknowledgeUncertainScheduledSend")
}

func (s *spyQuerier) PlatformAcknowledgeUncertainSend(ctx context.Context, arg store.PlatformAcknowledgeUncertainSendParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAcknowledgeUncertainSend")
}

func (s *spyQuerier) PlatformAcquireDraftSessionLock(ctx context.Context, arg store.PlatformAcquireDraftSessionLockParams) error {
	return s.record("PlatformAcquireDraftSessionLock")
}

func (s *spyQuerier) PlatformActivateLegacyDelegatedMail(ctx context.Context, arg store.PlatformActivateLegacyDelegatedMailParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformActivateLegacyDelegatedMail")
}

func (s *spyQuerier) PlatformAddInvestigationSource(ctx context.Context, arg store.PlatformAddInvestigationSourceParams) error {
	return s.record("PlatformAddInvestigationSource")
}

func (s *spyQuerier) PlatformAdvanceCollectorCheckpoint(ctx context.Context, arg store.PlatformAdvanceCollectorCheckpointParams) error {
	return s.record("PlatformAdvanceCollectorCheckpoint")
}

func (s *spyQuerier) PlatformAdvanceDNSPlan(ctx context.Context, arg store.PlatformAdvanceDNSPlanParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAdvanceDNSPlan")
}

func (s *spyQuerier) PlatformAdvanceMarketingFlow(ctx context.Context, arg store.PlatformAdvanceMarketingFlowParams) (store.PlatformAdvanceMarketingFlowRow, error) {
	var zero store.PlatformAdvanceMarketingFlowRow
	return zero, s.record("PlatformAdvanceMarketingFlow")
}

func (s *spyQuerier) PlatformAdvanceOnboarding(ctx context.Context, arg store.PlatformAdvanceOnboardingParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAdvanceOnboarding")
}

func (s *spyQuerier) PlatformAdvanceOnboardingTask(ctx context.Context, arg store.PlatformAdvanceOnboardingTaskParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAdvanceOnboardingTask")
}

func (s *spyQuerier) PlatformApplyArchiveRetention(ctx context.Context) ([]store.PlatformApplyArchiveRetentionRow, error) {
	var zero []store.PlatformApplyArchiveRetentionRow
	return zero, s.record("PlatformApplyArchiveRetention")
}

func (s *spyQuerier) PlatformApplyArchiveRetentionCandidates(ctx context.Context) ([]store.PlatformApplyArchiveRetentionCandidatesRow, error) {
	var zero []store.PlatformApplyArchiveRetentionCandidatesRow
	return zero, s.record("PlatformApplyArchiveRetentionCandidates")
}

func (s *spyQuerier) PlatformApplyArchiveRetentionMarkContent(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformApplyArchiveRetentionMarkContent")
}

func (s *spyQuerier) PlatformApplyArchiveRetentionMarkDeleted(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformApplyArchiveRetentionMarkDeleted")
}

func (s *spyQuerier) PlatformApplyManagedDNS(ctx context.Context, arg store.PlatformApplyManagedDNSParams) error {
	return s.record("PlatformApplyManagedDNS")
}

func (s *spyQuerier) PlatformArchiveCopyLifecycle(ctx context.Context, arg store.PlatformArchiveCopyLifecycleParams) (store.PlatformArchiveCopyLifecycleRow, error) {
	var zero store.PlatformArchiveCopyLifecycleRow
	return zero, s.record("PlatformArchiveCopyLifecycle")
}

func (s *spyQuerier) PlatformArchivePathIndexed(ctx context.Context, path string) (bool, error) {
	var zero bool
	return zero, s.record("PlatformArchivePathIndexed")
}

func (s *spyQuerier) PlatformArmMarketingFlowWait(ctx context.Context, arg store.PlatformArmMarketingFlowWaitParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformArmMarketingFlowWait")
}

func (s *spyQuerier) PlatformAttributeAuthenticatedDeliveryLifecycle(ctx context.Context, arg store.PlatformAttributeAuthenticatedDeliveryLifecycleParams) error {
	return s.record("PlatformAttributeAuthenticatedDeliveryLifecycle")
}

func (s *spyQuerier) PlatformAuthorizeMarketingFlowSend(ctx context.Context, arg store.PlatformAuthorizeMarketingFlowSendParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformAuthorizeMarketingFlowSend")
}

func (s *spyQuerier) PlatformAuthorizeMarketingSend(ctx context.Context, arg store.PlatformAuthorizeMarketingSendParams) (string, error) {
	var zero string
	return zero, s.record("PlatformAuthorizeMarketingSend")
}

func (s *spyQuerier) PlatformAuthorizeScheduledCopy(ctx context.Context, arg store.PlatformAuthorizeScheduledCopyParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAuthorizeScheduledCopy")
}

func (s *spyQuerier) PlatformAuthorizeSendCopy(ctx context.Context, arg store.PlatformAuthorizeSendCopyParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformAuthorizeSendCopy")
}

func (s *spyQuerier) PlatformBackupFreshness(ctx context.Context) (store.PlatformBackupFreshnessRow, error) {
	var zero store.PlatformBackupFreshnessRow
	return zero, s.record("PlatformBackupFreshness")
}

func (s *spyQuerier) PlatformBeginArchivePurge(ctx context.Context, arg store.PlatformBeginArchivePurgeParams) error {
	return s.record("PlatformBeginArchivePurge")
}

func (s *spyQuerier) PlatformBeginMailboxScheduledSendSubmission(ctx context.Context, arg store.PlatformBeginMailboxScheduledSendSubmissionParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformBeginMailboxScheduledSendSubmission")
}

func (s *spyQuerier) PlatformBeginMarketingFlowSend(ctx context.Context, arg store.PlatformBeginMarketingFlowSendParams) (string, error) {
	var zero string
	return zero, s.record("PlatformBeginMarketingFlowSend")
}

func (s *spyQuerier) PlatformBeginSendReceipt(ctx context.Context, arg store.PlatformBeginSendReceiptParams) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformBeginSendReceipt")
}

func (s *spyQuerier) PlatformBindBrandHost(ctx context.Context, arg store.PlatformBindBrandHostParams) (store.PlatformBindBrandHostRow, error) {
	var zero store.PlatformBindBrandHostRow
	return zero, s.record("PlatformBindBrandHost")
}

func (s *spyQuerier) PlatformBindOnboardingDomain(ctx context.Context, arg store.PlatformBindOnboardingDomainParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformBindOnboardingDomain")
}

func (s *spyQuerier) PlatformBindOwnedCalendarOrganizer(ctx context.Context, arg store.PlatformBindOwnedCalendarOrganizerParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformBindOwnedCalendarOrganizer")
}

func (s *spyQuerier) PlatformBindSchedulingEvent(ctx context.Context, arg store.PlatformBindSchedulingEventParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformBindSchedulingEvent")
}

func (s *spyQuerier) PlatformBrandHostCertEligible(ctx context.Context, hostname string) (bool, error) {
	var zero bool
	return zero, s.record("PlatformBrandHostCertEligible")
}

func (s *spyQuerier) PlatformBrandHostCertificateTransition(ctx context.Context, arg store.PlatformBrandHostCertificateTransitionParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformBrandHostCertificateTransition")
}

func (s *spyQuerier) PlatformBrandHostReserved(ctx context.Context, fqdn string) (bool, error) {
	var zero bool
	return zero, s.record("PlatformBrandHostReserved")
}

func (s *spyQuerier) PlatformBumpMarketingFlowAttempt(ctx context.Context, arg store.PlatformBumpMarketingFlowAttemptParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformBumpMarketingFlowAttempt")
}

func (s *spyQuerier) PlatformCalendarRestoreCutoff(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCalendarRestoreCutoff")
}

func (s *spyQuerier) PlatformCancelArchivePurgeIntent(ctx context.Context, arg store.PlatformCancelArchivePurgeIntentParams) error {
	return s.record("PlatformCancelArchivePurgeIntent")
}

func (s *spyQuerier) PlatformCancelMailboxScheduledSend(ctx context.Context, arg store.PlatformCancelMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformCancelMailboxScheduledSend")
}

func (s *spyQuerier) PlatformChangeMailboxPasswordCAS(ctx context.Context, arg store.PlatformChangeMailboxPasswordCASParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformChangeMailboxPasswordCAS")
}

func (s *spyQuerier) PlatformChangePanelMFA(ctx context.Context, arg store.PlatformChangePanelMFAParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformChangePanelMFA")
}

func (s *spyQuerier) PlatformChangeSupportTicket(ctx context.Context, arg store.PlatformChangeSupportTicketParams) (store.Tickets, error) {
	var zero store.Tickets
	return zero, s.record("PlatformChangeSupportTicket")
}

func (s *spyQuerier) PlatformCheckMarketingEmailSuppressed(ctx context.Context, arg store.PlatformCheckMarketingEmailSuppressedParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformCheckMarketingEmailSuppressed")
}

func (s *spyQuerier) PlatformCheckMarketingFlowLease(ctx context.Context, arg store.PlatformCheckMarketingFlowLeaseParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformCheckMarketingFlowLease")
}

func (s *spyQuerier) PlatformClaimCalendarInvitation(ctx context.Context, arg store.PlatformClaimCalendarInvitationParams) error {
	return s.record("PlatformClaimCalendarInvitation")
}

func (s *spyQuerier) PlatformClaimCalendarResponse(ctx context.Context, arg store.PlatformClaimCalendarResponseParams) error {
	return s.record("PlatformClaimCalendarResponse")
}

func (s *spyQuerier) PlatformClaimDNSPlan(ctx context.Context, leaseOwner pgtype.Text) (store.DnsChangePlans, error) {
	var zero store.DnsChangePlans
	return zero, s.record("PlatformClaimDNSPlan")
}

func (s *spyQuerier) PlatformClaimDraftSave(ctx context.Context, arg store.PlatformClaimDraftSaveParams) error {
	return s.record("PlatformClaimDraftSave")
}

func (s *spyQuerier) PlatformClaimEmailNotification(ctx context.Context) (store.NotificationOutbox, error) {
	var zero store.NotificationOutbox
	return zero, s.record("PlatformClaimEmailNotification")
}

func (s *spyQuerier) PlatformClaimMailTransfer(ctx context.Context, arg store.PlatformClaimMailTransferParams) error {
	return s.record("PlatformClaimMailTransfer")
}

func (s *spyQuerier) PlatformClaimMailboxJob(ctx context.Context) (store.MailboxJobs, error) {
	var zero store.MailboxJobs
	return zero, s.record("PlatformClaimMailboxJob")
}

func (s *spyQuerier) PlatformClaimMailboxScheduledSend(ctx context.Context) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformClaimMailboxScheduledSend")
}

func (s *spyQuerier) PlatformClaimMarketingFlowPositions(ctx context.Context, arg store.PlatformClaimMarketingFlowPositionsParams) ([]store.PlatformClaimMarketingFlowPositionsRow, error) {
	var zero []store.PlatformClaimMarketingFlowPositionsRow
	return zero, s.record("PlatformClaimMarketingFlowPositions")
}

func (s *spyQuerier) PlatformClaimMarketingRecipient(ctx context.Context, leaseOwner string) (store.MarketingRecipients, error) {
	var zero store.MarketingRecipients
	return zero, s.record("PlatformClaimMarketingRecipient")
}

func (s *spyQuerier) PlatformClaimOnboarding(ctx context.Context, leasedBy pgtype.Text) (store.OnboardingTasks, error) {
	var zero store.OnboardingTasks
	return zero, s.record("PlatformClaimOnboarding")
}

func (s *spyQuerier) PlatformClaimOnboardingTask(ctx context.Context, arg store.PlatformClaimOnboardingTaskParams) ([]store.PlatformClaimOnboardingTaskRow, error) {
	var zero []store.PlatformClaimOnboardingTaskRow
	return zero, s.record("PlatformClaimOnboardingTask")
}

func (s *spyQuerier) PlatformClaimOwnedScheduledSentCopy(ctx context.Context, arg store.PlatformClaimOwnedScheduledSentCopyParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformClaimOwnedScheduledSentCopy")
}

func (s *spyQuerier) PlatformClaimOwnedSendCopy(ctx context.Context, arg store.PlatformClaimOwnedSendCopyParams) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformClaimOwnedSendCopy")
}

func (s *spyQuerier) PlatformClaimScheduledSentCopy(ctx context.Context) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformClaimScheduledSentCopy")
}

func (s *spyQuerier) PlatformClaimSendCopy(ctx context.Context) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformClaimSendCopy")
}

func (s *spyQuerier) PlatformCollectorHealth(ctx context.Context) ([]store.CollectorCheckpoints, error) {
	var zero []store.CollectorCheckpoints
	return zero, s.record("PlatformCollectorHealth")
}

func (s *spyQuerier) PlatformCommissionRate(ctx context.Context, id int64) (int32, error) {
	var zero int32
	return zero, s.record("PlatformCommissionRate")
}

func (s *spyQuerier) PlatformCommitMarketingFlowFenced(ctx context.Context, arg store.PlatformCommitMarketingFlowFencedParams) (string, error) {
	var zero string
	return zero, s.record("PlatformCommitMarketingFlowFenced")
}

func (s *spyQuerier) PlatformCommitMarketingFlowWait(ctx context.Context, arg store.PlatformCommitMarketingFlowWaitParams) (string, error) {
	var zero string
	return zero, s.record("PlatformCommitMarketingFlowWait")
}

func (s *spyQuerier) PlatformCompleteDNSAction(ctx context.Context, arg store.PlatformCompleteDNSActionParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCompleteDNSAction")
}

func (s *spyQuerier) PlatformCompleteDraftSave(ctx context.Context, arg store.PlatformCompleteDraftSaveParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCompleteDraftSave")
}

func (s *spyQuerier) PlatformCompleteMailTransfer(ctx context.Context, arg store.PlatformCompleteMailTransferParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCompleteMailTransfer")
}

func (s *spyQuerier) PlatformCompleteMarketingCampaigns(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCompleteMarketingCampaigns")
}

func (s *spyQuerier) PlatformCompleteOnboardingTask(ctx context.Context, arg store.PlatformCompleteOnboardingTaskParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCompleteOnboardingTask")
}

func (s *spyQuerier) PlatformCompleteRecall(ctx context.Context, arg store.PlatformCompleteRecallParams) (store.PlatformCompleteRecallRow, error) {
	var zero store.PlatformCompleteRecallRow
	return zero, s.record("PlatformCompleteRecall")
}

func (s *spyQuerier) PlatformCompleteScheduledSentCopy(ctx context.Context, arg store.PlatformCompleteScheduledSentCopyParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformCompleteScheduledSentCopy")
}

func (s *spyQuerier) PlatformCompleteSendCopy(ctx context.Context, arg store.PlatformCompleteSendCopyParams) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformCompleteSendCopy")
}

func (s *spyQuerier) PlatformConfigureTenantAI(ctx context.Context, arg store.PlatformConfigureTenantAIParams) error {
	return s.record("PlatformConfigureTenantAI")
}

func (s *spyQuerier) PlatformConfigureTenantAIGovernance(ctx context.Context, arg store.PlatformConfigureTenantAIGovernanceParams) error {
	return s.record("PlatformConfigureTenantAIGovernance")
}

func (s *spyQuerier) PlatformConfirmDomainRecovery(ctx context.Context, arg store.PlatformConfirmDomainRecoveryParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformConfirmDomainRecovery")
}

func (s *spyQuerier) PlatformConsumeBrandHostChallenge(ctx context.Context, hostname string) (int64, error) {
	var zero int64
	return zero, s.record("PlatformConsumeBrandHostChallenge")
}

func (s *spyQuerier) PlatformConsumeMailboxOperationTickets(ctx context.Context, arg store.PlatformConsumeMailboxOperationTicketsParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformConsumeMailboxOperationTickets")
}

func (s *spyQuerier) PlatformConsumeMailboxTicket(ctx context.Context, arg store.PlatformConsumeMailboxTicketParams) (store.PlatformConsumeMailboxTicketRow, error) {
	var zero store.PlatformConsumeMailboxTicketRow
	return zero, s.record("PlatformConsumeMailboxTicket")
}

func (s *spyQuerier) PlatformContactImportDuplicate(ctx context.Context, arg store.PlatformContactImportDuplicateParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformContactImportDuplicate")
}

func (s *spyQuerier) PlatformCountLegacyMFASecrets(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCountLegacyMFASecrets")
}

func (s *spyQuerier) PlatformCountNewNotifications(ctx context.Context, afterID int64) ([]store.PlatformCountNewNotificationsRow, error) {
	var zero []store.PlatformCountNewNotificationsRow
	return zero, s.record("PlatformCountNewNotifications")
}

func (s *spyQuerier) PlatformCountRecentTLSReports(ctx context.Context, arg store.PlatformCountRecentTLSReportsParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCountRecentTLSReports")
}

func (s *spyQuerier) PlatformCreateAnnouncement(ctx context.Context, arg store.PlatformCreateAnnouncementParams) (store.Announcements, error) {
	var zero store.Announcements
	return zero, s.record("PlatformCreateAnnouncement")
}

func (s *spyQuerier) PlatformCreateDNSAction(ctx context.Context, arg store.PlatformCreateDNSActionParams) error {
	return s.record("PlatformCreateDNSAction")
}

func (s *spyQuerier) PlatformCreateInvestigation(ctx context.Context, arg store.PlatformCreateInvestigationParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCreateInvestigation")
}

func (s *spyQuerier) PlatformCreateLegacyAlias(ctx context.Context, arg store.PlatformCreateLegacyAliasParams) (store.MailboxAliases, error) {
	var zero store.MailboxAliases
	return zero, s.record("PlatformCreateLegacyAlias")
}

func (s *spyQuerier) PlatformCreateLegacyDomain(ctx context.Context, arg store.PlatformCreateLegacyDomainParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCreateLegacyDomain")
}

func (s *spyQuerier) PlatformCreateLegacyMailbox(ctx context.Context, arg store.PlatformCreateLegacyMailboxParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCreateLegacyMailbox")
}

func (s *spyQuerier) PlatformCreateMailboxScheduledSend(ctx context.Context, arg store.PlatformCreateMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformCreateMailboxScheduledSend")
}

func (s *spyQuerier) PlatformCreateMailboxSession(ctx context.Context, arg store.PlatformCreateMailboxSessionParams) (store.PlatformCreateMailboxSessionRow, error) {
	var zero store.PlatformCreateMailboxSessionRow
	return zero, s.record("PlatformCreateMailboxSession")
}

func (s *spyQuerier) PlatformCreateMailboxTicket(ctx context.Context, arg store.PlatformCreateMailboxTicketParams) (store.MailboxOperationTickets, error) {
	var zero store.MailboxOperationTickets
	return zero, s.record("PlatformCreateMailboxTicket")
}

func (s *spyQuerier) PlatformCreateManagedTenant(ctx context.Context, arg store.PlatformCreateManagedTenantParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCreateManagedTenant")
}

func (s *spyQuerier) PlatformCreateManagedUser(ctx context.Context, arg store.PlatformCreateManagedUserParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformCreateManagedUser")
}

func (s *spyQuerier) PlatformCreateMarketingChannel(ctx context.Context, arg store.PlatformCreateMarketingChannelParams) (store.PlatformCreateMarketingChannelRow, error) {
	var zero store.PlatformCreateMarketingChannelRow
	return zero, s.record("PlatformCreateMarketingChannel")
}

func (s *spyQuerier) PlatformCreateSendReceipt(ctx context.Context, arg store.PlatformCreateSendReceiptParams) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformCreateSendReceipt")
}

func (s *spyQuerier) PlatformCreateSieveBridge(ctx context.Context, arg store.PlatformCreateSieveBridgeParams) error {
	return s.record("PlatformCreateSieveBridge")
}

func (s *spyQuerier) PlatformCreateVerifiedMailboxSession(ctx context.Context, arg store.PlatformCreateVerifiedMailboxSessionParams) (store.MailboxSessions, error) {
	var zero store.MailboxSessions
	return zero, s.record("PlatformCreateVerifiedMailboxSession")
}

func (s *spyQuerier) PlatformCurrentManagedDNS(ctx context.Context, arg store.PlatformCurrentManagedDNSParams) (store.ManagedDnsZones, error) {
	var zero store.ManagedDnsZones
	return zero, s.record("PlatformCurrentManagedDNS")
}

func (s *spyQuerier) PlatformCustomWebEnabled(ctx context.Context, fqdn string) (bool, error) {
	var zero bool
	return zero, s.record("PlatformCustomWebEnabled")
}

func (s *spyQuerier) PlatformDNSDomain(ctx context.Context, arg store.PlatformDNSDomainParams) (store.PlatformDNSDomainRow, error) {
	var zero store.PlatformDNSDomainRow
	return zero, s.record("PlatformDNSDomain")
}

func (s *spyQuerier) PlatformDataIssues(ctx context.Context) ([]store.PlatformDataIssuesRow, error) {
	var zero []store.PlatformDataIssuesRow
	return zero, s.record("PlatformDataIssues")
}

func (s *spyQuerier) PlatformDeferScheduledSentCopy(ctx context.Context, arg store.PlatformDeferScheduledSentCopyParams) error {
	return s.record("PlatformDeferScheduledSentCopy")
}

func (s *spyQuerier) PlatformDeferSendCopy(ctx context.Context, arg store.PlatformDeferSendCopyParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformDeferSendCopy")
}

func (s *spyQuerier) PlatformDeleteBrand(ctx context.Context, id int64) (int64, error) {
	var zero int64
	return zero, s.record("PlatformDeleteBrand")
}

func (s *spyQuerier) PlatformDeleteBrandHostChallenge(ctx context.Context, hostname string) error {
	return s.record("PlatformDeleteBrandHostChallenge")
}

func (s *spyQuerier) PlatformDeleteBrandSetting(ctx context.Context, arg store.PlatformDeleteBrandSettingParams) error {
	return s.record("PlatformDeleteBrandSetting")
}

func (s *spyQuerier) PlatformDeleteContact(ctx context.Context, arg store.PlatformDeleteContactParams) (store.PlatformDeleteContactRow, error) {
	var zero store.PlatformDeleteContactRow
	return zero, s.record("PlatformDeleteContact")
}

func (s *spyQuerier) PlatformDeleteContactGroup(ctx context.Context, arg store.PlatformDeleteContactGroupParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformDeleteContactGroup")
}

func (s *spyQuerier) PlatformDeleteEvent(ctx context.Context, arg store.PlatformDeleteEventParams) (store.PlatformDeleteEventRow, error) {
	var zero store.PlatformDeleteEventRow
	return zero, s.record("PlatformDeleteEvent")
}

func (s *spyQuerier) PlatformDeleteMailboxSnippet(ctx context.Context, arg store.PlatformDeleteMailboxSnippetParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformDeleteMailboxSnippet")
}

func (s *spyQuerier) PlatformDeleteSetting(ctx context.Context, key string) error {
	return s.record("PlatformDeleteSetting")
}

func (s *spyQuerier) PlatformDeleteTenantDirectoryContact(ctx context.Context, arg store.PlatformDeleteTenantDirectoryContactParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformDeleteTenantDirectoryContact")
}

func (s *spyQuerier) PlatformDemotePreviousOwners(ctx context.Context, arg store.PlatformDemotePreviousOwnersParams) error {
	return s.record("PlatformDemotePreviousOwners")
}

func (s *spyQuerier) PlatformDiscardStaleCalendarReminders(ctx context.Context) error {
	return s.record("PlatformDiscardStaleCalendarReminders")
}

func (s *spyQuerier) PlatformDraftDeleteLocked(ctx context.Context, arg store.PlatformDraftDeleteLockedParams) (pgtype.Bool, error) {
	var zero pgtype.Bool
	return zero, s.record("PlatformDraftDeleteLocked")
}

func (s *spyQuerier) PlatformDraftReplacementPending(ctx context.Context, arg store.PlatformDraftReplacementPendingParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformDraftReplacementPending")
}

func (s *spyQuerier) PlatformDraftSendLocked(ctx context.Context, arg store.PlatformDraftSendLockedParams) (pgtype.Bool, error) {
	var zero pgtype.Bool
	return zero, s.record("PlatformDraftSendLocked")
}

func (s *spyQuerier) PlatformEncryptLegacyMFASecret(ctx context.Context, arg store.PlatformEncryptLegacyMFASecretParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformEncryptLegacyMFASecret")
}

func (s *spyQuerier) PlatformEnqueueAIAlerts(ctx context.Context) error {
	return s.record("PlatformEnqueueAIAlerts")
}

func (s *spyQuerier) PlatformEnqueueDNSOnboardingAlerts(ctx context.Context) error {
	return s.record("PlatformEnqueueDNSOnboardingAlerts")
}

func (s *spyQuerier) PlatformEnqueueNotification(ctx context.Context, arg store.PlatformEnqueueNotificationParams) error {
	return s.record("PlatformEnqueueNotification")
}

func (s *spyQuerier) PlatformEnqueueObservabilityAlerts(ctx context.Context) error {
	return s.record("PlatformEnqueueObservabilityAlerts")
}

func (s *spyQuerier) PlatformEnsureAddressbook(ctx context.Context, arg store.PlatformEnsureAddressbookParams) (store.MailboxAddressbooks, error) {
	var zero store.MailboxAddressbooks
	return zero, s.record("PlatformEnsureAddressbook")
}

func (s *spyQuerier) PlatformEnsureCalendar(ctx context.Context, arg store.PlatformEnsureCalendarParams) (store.MailboxCalendars, error) {
	var zero store.MailboxCalendars
	return zero, s.record("PlatformEnsureCalendar")
}

func (s *spyQuerier) PlatformEnsureCollectorCheckpoint(ctx context.Context, stream string) error {
	return s.record("PlatformEnsureCollectorCheckpoint")
}

func (s *spyQuerier) PlatformEnsureManagedBrand(ctx context.Context, arg store.PlatformEnsureManagedBrandParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformEnsureManagedBrand")
}

func (s *spyQuerier) PlatformEvidenceVisible(ctx context.Context, arg store.PlatformEvidenceVisibleParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformEvidenceVisible")
}

func (s *spyQuerier) PlatformExpireAIReservations(ctx context.Context, tenantID int64) error {
	return s.record("PlatformExpireAIReservations")
}

func (s *spyQuerier) PlatformExpireArchiveApprovals(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformExpireArchiveApprovals")
}

func (s *spyQuerier) PlatformExpireDNSClaims(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformExpireDNSClaims")
}

func (s *spyQuerier) PlatformExpireOnboarding(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformExpireOnboarding")
}

func (s *spyQuerier) PlatformExpireOnboardingTasks(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformExpireOnboardingTasks")
}

func (s *spyQuerier) PlatformFailArchivePurge(ctx context.Context, arg store.PlatformFailArchivePurgeParams) error {
	return s.record("PlatformFailArchivePurge")
}

func (s *spyQuerier) PlatformFailOnboardingTask(ctx context.Context, arg store.PlatformFailOnboardingTaskParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformFailOnboardingTask")
}

func (s *spyQuerier) PlatformFenceDNSPlan(ctx context.Context, arg store.PlatformFenceDNSPlanParams) (store.DnsChangePlans, error) {
	var zero store.DnsChangePlans
	return zero, s.record("PlatformFenceDNSPlan")
}

func (s *spyQuerier) PlatformFenceOnboarding(ctx context.Context, arg store.PlatformFenceOnboardingParams) (store.OnboardingTasks, error) {
	var zero store.OnboardingTasks
	return zero, s.record("PlatformFenceOnboarding")
}

func (s *spyQuerier) PlatformFindManagedUser(ctx context.Context, email string) (int64, error) {
	var zero int64
	return zero, s.record("PlatformFindManagedUser")
}

func (s *spyQuerier) PlatformFindSchedulingEvent(ctx context.Context, arg store.PlatformFindSchedulingEventParams) (store.MailboxEvents, error) {
	var zero store.MailboxEvents
	return zero, s.record("PlatformFindSchedulingEvent")
}

func (s *spyQuerier) PlatformFindTLSReportFingerprint(ctx context.Context, arg store.PlatformFindTLSReportFingerprintParams) (string, error) {
	var zero string
	return zero, s.record("PlatformFindTLSReportFingerprint")
}

func (s *spyQuerier) PlatformFinishAIRequest(ctx context.Context, arg store.PlatformFinishAIRequestParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformFinishAIRequest")
}

func (s *spyQuerier) PlatformFinishArchivePurge(ctx context.Context, arg store.PlatformFinishArchivePurgeParams) error {
	return s.record("PlatformFinishArchivePurge")
}

func (s *spyQuerier) PlatformFinishCalendarInvitation(ctx context.Context, arg store.PlatformFinishCalendarInvitationParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformFinishCalendarInvitation")
}

func (s *spyQuerier) PlatformFinishCalendarResponse(ctx context.Context, arg store.PlatformFinishCalendarResponseParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformFinishCalendarResponse")
}

func (s *spyQuerier) PlatformFinishEmailNotification(ctx context.Context, arg store.PlatformFinishEmailNotificationParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformFinishEmailNotification")
}

func (s *spyQuerier) PlatformFinishMailboxScheduledSend(ctx context.Context, arg store.PlatformFinishMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformFinishMailboxScheduledSend")
}

func (s *spyQuerier) PlatformFinishMarketingSend(ctx context.Context, arg store.PlatformFinishMarketingSendParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformFinishMarketingSend")
}

func (s *spyQuerier) PlatformFinishReviewedRecall(ctx context.Context, arg store.PlatformFinishReviewedRecallParams) (store.PlatformFinishReviewedRecallRow, error) {
	var zero store.PlatformFinishReviewedRecallRow
	return zero, s.record("PlatformFinishReviewedRecall")
}

func (s *spyQuerier) PlatformFinishSendReceipt(ctx context.Context, arg store.PlatformFinishSendReceiptParams) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformFinishSendReceipt")
}

func (s *spyQuerier) PlatformFreezeScheduledSend(ctx context.Context, arg store.PlatformFreezeScheduledSendParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformFreezeScheduledSend")
}

func (s *spyQuerier) PlatformGetAIDraft(ctx context.Context, arg store.PlatformGetAIDraftParams) (store.PlatformGetAIDraftRow, error) {
	var zero store.PlatformGetAIDraftRow
	return zero, s.record("PlatformGetAIDraft")
}

func (s *spyQuerier) PlatformGetAIProvider(ctx context.Context, tenantID int64) (store.AiProviderConfigs, error) {
	var zero store.AiProviderConfigs
	return zero, s.record("PlatformGetAIProvider")
}

func (s *spyQuerier) PlatformGetAIRequest(ctx context.Context, arg store.PlatformGetAIRequestParams) (store.AiUsageRequests, error) {
	var zero store.AiUsageRequests
	return zero, s.record("PlatformGetAIRequest")
}

func (s *spyQuerier) PlatformGetActiveMarketingChannel(ctx context.Context, tenantID int64) (store.PlatformGetActiveMarketingChannelRow, error) {
	var zero store.PlatformGetActiveMarketingChannelRow
	return zero, s.record("PlatformGetActiveMarketingChannel")
}

func (s *spyQuerier) PlatformGetAddressbookByName(ctx context.Context, arg store.PlatformGetAddressbookByNameParams) (store.MailboxAddressbooks, error) {
	var zero store.MailboxAddressbooks
	return zero, s.record("PlatformGetAddressbookByName")
}

func (s *spyQuerier) PlatformGetBrandHostChallenge(ctx context.Context, hostname string) (store.BrandHostChallenges, error) {
	var zero store.BrandHostChallenges
	return zero, s.record("PlatformGetBrandHostChallenge")
}

func (s *spyQuerier) PlatformGetBrandHostLifecycle(ctx context.Context, hostname string) (store.PlatformGetBrandHostLifecycleRow, error) {
	var zero store.PlatformGetBrandHostLifecycleRow
	return zero, s.record("PlatformGetBrandHostLifecycle")
}

func (s *spyQuerier) PlatformGetBrandThemeByTenant(ctx context.Context, id int64) (store.PlatformGetBrandThemeByTenantRow, error) {
	var zero store.PlatformGetBrandThemeByTenantRow
	return zero, s.record("PlatformGetBrandThemeByTenant")
}

func (s *spyQuerier) PlatformGetCalendarByName(ctx context.Context, arg store.PlatformGetCalendarByNameParams) (store.MailboxCalendars, error) {
	var zero store.MailboxCalendars
	return zero, s.record("PlatformGetCalendarByName")
}

func (s *spyQuerier) PlatformGetCollectorCheckpoint(ctx context.Context, stream string) (store.CollectorCheckpoints, error) {
	var zero store.CollectorCheckpoints
	return zero, s.record("PlatformGetCollectorCheckpoint")
}

func (s *spyQuerier) PlatformGetContact(ctx context.Context, arg store.PlatformGetContactParams) (store.MailboxContacts, error) {
	var zero store.MailboxContacts
	return zero, s.record("PlatformGetContact")
}

func (s *spyQuerier) PlatformGetContactGroup(ctx context.Context, arg store.PlatformGetContactGroupParams) (store.MailboxContactGroups, error) {
	var zero store.MailboxContactGroups
	return zero, s.record("PlatformGetContactGroup")
}

func (s *spyQuerier) PlatformGetDNSCredential(ctx context.Context, arg store.PlatformGetDNSCredentialParams) (store.DnsCredentials, error) {
	var zero store.DnsCredentials
	return zero, s.record("PlatformGetDNSCredential")
}

func (s *spyQuerier) PlatformGetDNSSecurity(ctx context.Context, fqdn string) (store.PlatformGetDNSSecurityRow, error) {
	var zero store.PlatformGetDNSSecurityRow
	return zero, s.record("PlatformGetDNSSecurity")
}

func (s *spyQuerier) PlatformGetDataImportReceipt(ctx context.Context, arg store.PlatformGetDataImportReceiptParams) (store.MailboxJobs, error) {
	var zero store.MailboxJobs
	return zero, s.record("PlatformGetDataImportReceipt")
}

func (s *spyQuerier) PlatformGetDeliveryQueueContext(ctx context.Context, arg store.PlatformGetDeliveryQueueContextParams) (store.DeliveryQueueContexts, error) {
	var zero store.DeliveryQueueContexts
	return zero, s.record("PlatformGetDeliveryQueueContext")
}

func (s *spyQuerier) PlatformGetDomainForRecovery(ctx context.Context, arg store.PlatformGetDomainForRecoveryParams) (store.PlatformGetDomainForRecoveryRow, error) {
	var zero store.PlatformGetDomainForRecoveryRow
	return zero, s.record("PlatformGetDomainForRecovery")
}

func (s *spyQuerier) PlatformGetEvent(ctx context.Context, arg store.PlatformGetEventParams) (store.MailboxEvents, error) {
	var zero store.MailboxEvents
	return zero, s.record("PlatformGetEvent")
}

func (s *spyQuerier) PlatformGetEventAny(ctx context.Context, arg store.PlatformGetEventAnyParams) (store.MailboxEvents, error) {
	var zero store.MailboxEvents
	return zero, s.record("PlatformGetEventAny")
}

func (s *spyQuerier) PlatformGetEventByUID(ctx context.Context, arg store.PlatformGetEventByUIDParams) (store.MailboxEvents, error) {
	var zero store.MailboxEvents
	return zero, s.record("PlatformGetEventByUID")
}

func (s *spyQuerier) PlatformGetLegacyScheduledCopy(ctx context.Context) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformGetLegacyScheduledCopy")
}

func (s *spyQuerier) PlatformGetMailboxByAddress(ctx context.Context, address string) (store.PlatformGetMailboxByAddressRow, error) {
	var zero store.PlatformGetMailboxByAddressRow
	return zero, s.record("PlatformGetMailboxByAddress")
}

func (s *spyQuerier) PlatformGetMailboxPhishingFinding(ctx context.Context, arg store.PlatformGetMailboxPhishingFindingParams) (store.PlatformGetMailboxPhishingFindingRow, error) {
	var zero store.PlatformGetMailboxPhishingFindingRow
	return zero, s.record("PlatformGetMailboxPhishingFinding")
}

func (s *spyQuerier) PlatformGetMailboxQuota(ctx context.Context, arg store.PlatformGetMailboxQuotaParams) (store.PlatformGetMailboxQuotaRow, error) {
	var zero store.PlatformGetMailboxQuotaRow
	return zero, s.record("PlatformGetMailboxQuota")
}

func (s *spyQuerier) PlatformGetMailboxScheduledSend(ctx context.Context, arg store.PlatformGetMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformGetMailboxScheduledSend")
}

func (s *spyQuerier) PlatformGetMailboxScheduledSendAddress(ctx context.Context, arg store.PlatformGetMailboxScheduledSendAddressParams) (string, error) {
	var zero string
	return zero, s.record("PlatformGetMailboxScheduledSendAddress")
}

func (s *spyQuerier) PlatformGetMailboxScheduledSendByKey(ctx context.Context, arg store.PlatformGetMailboxScheduledSendByKeyParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformGetMailboxScheduledSendByKey")
}

func (s *spyQuerier) PlatformGetMailboxSession(ctx context.Context, tokenHash string) (store.PlatformGetMailboxSessionRow, error) {
	var zero store.PlatformGetMailboxSessionRow
	return zero, s.record("PlatformGetMailboxSession")
}

func (s *spyQuerier) PlatformGetMailboxSessionForRevocation(ctx context.Context, tokenHash string) (store.PlatformGetMailboxSessionForRevocationRow, error) {
	var zero store.PlatformGetMailboxSessionForRevocationRow
	return zero, s.record("PlatformGetMailboxSessionForRevocation")
}

func (s *spyQuerier) PlatformGetMailboxSettings(ctx context.Context, arg store.PlatformGetMailboxSettingsParams) (store.PlatformGetMailboxSettingsRow, error) {
	var zero store.PlatformGetMailboxSettingsRow
	return zero, s.record("PlatformGetMailboxSettings")
}

func (s *spyQuerier) PlatformGetMailboxSieve(ctx context.Context, arg store.PlatformGetMailboxSieveParams) (store.PlatformGetMailboxSieveRow, error) {
	var zero store.PlatformGetMailboxSieveRow
	return zero, s.record("PlatformGetMailboxSieve")
}

func (s *spyQuerier) PlatformGetMailboxSnippet(ctx context.Context, arg store.PlatformGetMailboxSnippetParams) (store.MailboxSnippets, error) {
	var zero store.MailboxSnippets
	return zero, s.record("PlatformGetMailboxSnippet")
}

func (s *spyQuerier) PlatformGetMarketingChannel(ctx context.Context, id int64) (store.MarketingChannels, error) {
	var zero store.MarketingChannels
	return zero, s.record("PlatformGetMarketingChannel")
}

func (s *spyQuerier) PlatformGetMarketingDelivery(ctx context.Context, arg store.PlatformGetMarketingDeliveryParams) (store.PlatformGetMarketingDeliveryRow, error) {
	var zero store.PlatformGetMarketingDeliveryRow
	return zero, s.record("PlatformGetMarketingDelivery")
}

func (s *spyQuerier) PlatformGetMarketingFlowPosition(ctx context.Context, arg store.PlatformGetMarketingFlowPositionParams) (store.PlatformGetMarketingFlowPositionRow, error) {
	var zero store.PlatformGetMarketingFlowPositionRow
	return zero, s.record("PlatformGetMarketingFlowPosition")
}

func (s *spyQuerier) PlatformGetMarketingFlowSnapshotContent(ctx context.Context, arg store.PlatformGetMarketingFlowSnapshotContentParams) (store.PlatformGetMarketingFlowSnapshotContentRow, error) {
	var zero store.PlatformGetMarketingFlowSnapshotContentRow
	return zero, s.record("PlatformGetMarketingFlowSnapshotContent")
}

func (s *spyQuerier) PlatformGetMarketingFlowTemplate(ctx context.Context, arg store.PlatformGetMarketingFlowTemplateParams) (store.PlatformGetMarketingFlowTemplateRow, error) {
	var zero store.PlatformGetMarketingFlowTemplateRow
	return zero, s.record("PlatformGetMarketingFlowTemplate")
}

func (s *spyQuerier) PlatformGetNotificationChannel(ctx context.Context) ([]byte, error) {
	var zero []byte
	return zero, s.record("PlatformGetNotificationChannel")
}

func (s *spyQuerier) PlatformGetObservabilityThresholds(ctx context.Context) ([]store.PlatformGetObservabilityThresholdsRow, error) {
	var zero []store.PlatformGetObservabilityThresholdsRow
	return zero, s.record("PlatformGetObservabilityThresholds")
}

func (s *spyQuerier) PlatformGetSendReceipt(ctx context.Context, arg store.PlatformGetSendReceiptParams) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformGetSendReceipt")
}

func (s *spyQuerier) PlatformGetSendReceiptByKey(ctx context.Context, arg store.PlatformGetSendReceiptByKeyParams) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformGetSendReceiptByKey")
}

func (s *spyQuerier) PlatformGetSendReceiptsForDraft(ctx context.Context, arg store.PlatformGetSendReceiptsForDraftParams) ([]store.MailboxSendReceipts, error) {
	var zero []store.MailboxSendReceipts
	return zero, s.record("PlatformGetSendReceiptsForDraft")
}

func (s *spyQuerier) PlatformGetSetting(ctx context.Context, key string) (store.PlatformGetSettingRow, error) {
	var zero store.PlatformGetSettingRow
	return zero, s.record("PlatformGetSetting")
}

func (s *spyQuerier) PlatformGetSieveActivationTarget(ctx context.Context, arg store.PlatformGetSieveActivationTargetParams) (store.PlatformGetSieveActivationTargetRow, error) {
	var zero store.PlatformGetSieveActivationTargetRow
	return zero, s.record("PlatformGetSieveActivationTarget")
}

func (s *spyQuerier) PlatformGetSieveBridge(ctx context.Context, tokenHash string) (store.PlatformGetSieveBridgeRow, error) {
	var zero store.PlatformGetSieveBridgeRow
	return zero, s.record("PlatformGetSieveBridge")
}

func (s *spyQuerier) PlatformGetSubscriptionNoticeDays(ctx context.Context) ([]byte, error) {
	var zero []byte
	return zero, s.record("PlatformGetSubscriptionNoticeDays")
}

func (s *spyQuerier) PlatformGetSupportTicket(ctx context.Context, arg store.PlatformGetSupportTicketParams) (store.Tickets, error) {
	var zero store.Tickets
	return zero, s.record("PlatformGetSupportTicket")
}

func (s *spyQuerier) PlatformGetTenantDirectorySettings(ctx context.Context, tenantID int64) (store.TenantDirectorySettings, error) {
	var zero store.TenantDirectorySettings
	return zero, s.record("PlatformGetTenantDirectorySettings")
}

func (s *spyQuerier) PlatformGrantInvestigation(ctx context.Context, arg store.PlatformGrantInvestigationParams) error {
	return s.record("PlatformGrantInvestigation")
}

func (s *spyQuerier) PlatformGrantTenantOwner(ctx context.Context, arg store.PlatformGrantTenantOwnerParams) error {
	return s.record("PlatformGrantTenantOwner")
}

func (s *spyQuerier) PlatformHoldExpiredScheduledCopies(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformHoldExpiredScheduledCopies")
}

func (s *spyQuerier) PlatformHoldExpiredSendCopies(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformHoldExpiredSendCopies")
}

func (s *spyQuerier) PlatformHoldUnauthorizedScheduledCopies(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformHoldUnauthorizedScheduledCopies")
}

func (s *spyQuerier) PlatformHoldUnauthorizedSendCopies(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformHoldUnauthorizedSendCopies")
}

func (s *spyQuerier) PlatformInboundAllowed(ctx context.Context, dollar_1 string) (bool, error) {
	var zero bool
	return zero, s.record("PlatformInboundAllowed")
}

func (s *spyQuerier) PlatformIndexArchiveContent(ctx context.Context, arg store.PlatformIndexArchiveContentParams) error {
	return s.record("PlatformIndexArchiveContent")
}

func (s *spyQuerier) PlatformIndexArchiveCopy(ctx context.Context, arg store.PlatformIndexArchiveCopyParams) (store.PlatformIndexArchiveCopyRow, error) {
	var zero store.PlatformIndexArchiveCopyRow
	return zero, s.record("PlatformIndexArchiveCopy")
}

func (s *spyQuerier) PlatformInsertAIDraft(ctx context.Context, arg store.PlatformInsertAIDraftParams) (store.PlatformInsertAIDraftRow, error) {
	var zero store.PlatformInsertAIDraftRow
	return zero, s.record("PlatformInsertAIDraft")
}

func (s *spyQuerier) PlatformInsertArchiveEntry(ctx context.Context, arg store.PlatformInsertArchiveEntryParams) error {
	return s.record("PlatformInsertArchiveEntry")
}

func (s *spyQuerier) PlatformInsertBrandHostChallenge(ctx context.Context, arg store.PlatformInsertBrandHostChallengeParams) error {
	return s.record("PlatformInsertBrandHostChallenge")
}

func (s *spyQuerier) PlatformInsertContact(ctx context.Context, arg store.PlatformInsertContactParams) (store.PlatformInsertContactRow, error) {
	var zero store.PlatformInsertContactRow
	return zero, s.record("PlatformInsertContact")
}

func (s *spyQuerier) PlatformInsertContactEnhanced(ctx context.Context, arg store.PlatformInsertContactEnhancedParams) (store.PlatformInsertContactEnhancedRow, error) {
	var zero store.PlatformInsertContactEnhancedRow
	return zero, s.record("PlatformInsertContactEnhanced")
}

func (s *spyQuerier) PlatformInsertContactGroup(ctx context.Context, arg store.PlatformInsertContactGroupParams) (store.MailboxContactGroups, error) {
	var zero store.MailboxContactGroups
	return zero, s.record("PlatformInsertContactGroup")
}

func (s *spyQuerier) PlatformInsertContactGroupMember(ctx context.Context, arg store.PlatformInsertContactGroupMemberParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformInsertContactGroupMember")
}

func (s *spyQuerier) PlatformInsertDataImportReceipt(ctx context.Context, arg store.PlatformInsertDataImportReceiptParams) error {
	return s.record("PlatformInsertDataImportReceipt")
}

func (s *spyQuerier) PlatformInsertEvent(ctx context.Context, arg store.PlatformInsertEventParams) (store.PlatformInsertEventRow, error) {
	var zero store.PlatformInsertEventRow
	return zero, s.record("PlatformInsertEvent")
}

func (s *spyQuerier) PlatformInsertMailboxSettings(ctx context.Context, arg store.PlatformInsertMailboxSettingsParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformInsertMailboxSettings")
}

func (s *spyQuerier) PlatformInsertMailboxSnippet(ctx context.Context, arg store.PlatformInsertMailboxSnippetParams) (store.MailboxSnippets, error) {
	var zero store.MailboxSnippets
	return zero, s.record("PlatformInsertMailboxSnippet")
}

func (s *spyQuerier) PlatformInsertMarketingFlow(ctx context.Context, arg store.PlatformInsertMarketingFlowParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformInsertMarketingFlow")
}

func (s *spyQuerier) PlatformInsertMarketingFlowStep(ctx context.Context, arg store.PlatformInsertMarketingFlowStepParams) error {
	return s.record("PlatformInsertMarketingFlowStep")
}

func (s *spyQuerier) PlatformInsertTenantDirectoryContact(ctx context.Context, arg store.PlatformInsertTenantDirectoryContactParams) (store.TenantDirectoryContacts, error) {
	var zero store.TenantDirectoryContacts
	return zero, s.record("PlatformInsertTenantDirectoryContact")
}

func (s *spyQuerier) PlatformLegacyImportTargetFacts(ctx context.Context, tenantID int64) (store.PlatformLegacyImportTargetFactsRow, error) {
	var zero store.PlatformLegacyImportTargetFactsRow
	return zero, s.record("PlatformLegacyImportTargetFacts")
}

func (s *spyQuerier) PlatformListAliases(ctx context.Context) ([]store.PlatformListAliasesRow, error) {
	var zero []store.PlatformListAliasesRow
	return zero, s.record("PlatformListAliases")
}

func (s *spyQuerier) PlatformListAnnouncements(ctx context.Context) ([]store.Announcements, error) {
	var zero []store.Announcements
	return zero, s.record("PlatformListAnnouncements")
}

func (s *spyQuerier) PlatformListArchivePurgeCandidates(ctx context.Context) ([]store.PlatformListArchivePurgeCandidatesRow, error) {
	var zero []store.PlatformListArchivePurgeCandidatesRow
	return zero, s.record("PlatformListArchivePurgeCandidates")
}

func (s *spyQuerier) PlatformListArchiveUnindexedOriginals(ctx context.Context, afterID int64) ([]store.PlatformListArchiveUnindexedOriginalsRow, error) {
	var zero []store.PlatformListArchiveUnindexedOriginalsRow
	return zero, s.record("PlatformListArchiveUnindexedOriginals")
}

func (s *spyQuerier) PlatformListAuditLogs(ctx context.Context, limit int32) ([]store.AuditLogs, error) {
	var zero []store.AuditLogs
	return zero, s.record("PlatformListAuditLogs")
}

func (s *spyQuerier) PlatformListBrandHosts(ctx context.Context) ([]store.PlatformListBrandHostsRow, error) {
	var zero []store.PlatformListBrandHostsRow
	return zero, s.record("PlatformListBrandHosts")
}

func (s *spyQuerier) PlatformListBrandSettings(ctx context.Context, scopeID int64) ([]store.PlatformListBrandSettingsRow, error) {
	var zero []store.PlatformListBrandSettingsRow
	return zero, s.record("PlatformListBrandSettings")
}

func (s *spyQuerier) PlatformListBrandThemes(ctx context.Context) ([]store.PlatformListBrandThemesRow, error) {
	var zero []store.PlatformListBrandThemesRow
	return zero, s.record("PlatformListBrandThemes")
}

func (s *spyQuerier) PlatformListBrands(ctx context.Context) ([]store.PlatformListBrandsRow, error) {
	var zero []store.PlatformListBrandsRow
	return zero, s.record("PlatformListBrands")
}

func (s *spyQuerier) PlatformListCalendarEventsPage(ctx context.Context, arg store.PlatformListCalendarEventsPageParams) ([]store.MailboxEvents, error) {
	var zero []store.MailboxEvents
	return zero, s.record("PlatformListCalendarEventsPage")
}

func (s *spyQuerier) PlatformListCalendarReminders(ctx context.Context, arg store.PlatformListCalendarRemindersParams) ([]store.PlatformListCalendarRemindersRow, error) {
	var zero []store.PlatformListCalendarRemindersRow
	return zero, s.record("PlatformListCalendarReminders")
}

func (s *spyQuerier) PlatformListCalendarSyncEvents(ctx context.Context, arg store.PlatformListCalendarSyncEventsParams) ([]store.MailboxEvents, error) {
	var zero []store.MailboxEvents
	return zero, s.record("PlatformListCalendarSyncEvents")
}

func (s *spyQuerier) PlatformListChanges(ctx context.Context, arg store.PlatformListChangesParams) ([]store.MailboxChanges, error) {
	var zero []store.MailboxChanges
	return zero, s.record("PlatformListChanges")
}

func (s *spyQuerier) PlatformListChannels(ctx context.Context) ([]store.Channels, error) {
	var zero []store.Channels
	return zero, s.record("PlatformListChannels")
}

func (s *spyQuerier) PlatformListContactGroupMembers(ctx context.Context, arg store.PlatformListContactGroupMembersParams) ([]store.MailboxContacts, error) {
	var zero []store.MailboxContacts
	return zero, s.record("PlatformListContactGroupMembers")
}

func (s *spyQuerier) PlatformListContactGroups(ctx context.Context, arg store.PlatformListContactGroupsParams) ([]store.MailboxContactGroups, error) {
	var zero []store.MailboxContactGroups
	return zero, s.record("PlatformListContactGroups")
}

func (s *spyQuerier) PlatformListContacts(ctx context.Context, arg store.PlatformListContactsParams) ([]store.MailboxContacts, error) {
	var zero []store.MailboxContacts
	return zero, s.record("PlatformListContacts")
}

func (s *spyQuerier) PlatformListContactsPage(ctx context.Context, arg store.PlatformListContactsPageParams) ([]store.MailboxContacts, error) {
	var zero []store.MailboxContacts
	return zero, s.record("PlatformListContactsPage")
}

func (s *spyQuerier) PlatformListDKIMDomains(ctx context.Context) ([]store.PlatformListDKIMDomainsRow, error) {
	var zero []store.PlatformListDKIMDomainsRow
	return zero, s.record("PlatformListDKIMDomains")
}

func (s *spyQuerier) PlatformListDNSActions(ctx context.Context, planID pgtype.UUID) ([]store.DnsRecordActions, error) {
	var zero []store.DnsRecordActions
	return zero, s.record("PlatformListDNSActions")
}

func (s *spyQuerier) PlatformListDNSSecurity(ctx context.Context) ([]store.PlatformListDNSSecurityRow, error) {
	var zero []store.PlatformListDNSSecurityRow
	return zero, s.record("PlatformListDNSSecurity")
}

func (s *spyQuerier) PlatformListDataImportReceipts(ctx context.Context, arg store.PlatformListDataImportReceiptsParams) ([]store.PlatformListDataImportReceiptsRow, error) {
	var zero []store.PlatformListDataImportReceiptsRow
	return zero, s.record("PlatformListDataImportReceipts")
}

func (s *spyQuerier) PlatformListDomains(ctx context.Context) ([]store.PlatformListDomainsRow, error) {
	var zero []store.PlatformListDomainsRow
	return zero, s.record("PlatformListDomains")
}

func (s *spyQuerier) PlatformListEvents(ctx context.Context, arg store.PlatformListEventsParams) ([]store.MailboxEvents, error) {
	var zero []store.MailboxEvents
	return zero, s.record("PlatformListEvents")
}

func (s *spyQuerier) PlatformListInvestigationSources(ctx context.Context, arg store.PlatformListInvestigationSourcesParams) ([]int64, error) {
	var zero []int64
	return zero, s.record("PlatformListInvestigationSources")
}

func (s *spyQuerier) PlatformListMailboxScheduledSends(ctx context.Context, arg store.PlatformListMailboxScheduledSendsParams) ([]store.MailboxScheduledSends, error) {
	var zero []store.MailboxScheduledSends
	return zero, s.record("PlatformListMailboxScheduledSends")
}

func (s *spyQuerier) PlatformListMailboxSenderIdentities(ctx context.Context, arg store.PlatformListMailboxSenderIdentitiesParams) ([]store.PlatformListMailboxSenderIdentitiesRow, error) {
	var zero []store.PlatformListMailboxSenderIdentitiesRow
	return zero, s.record("PlatformListMailboxSenderIdentities")
}

func (s *spyQuerier) PlatformListMailboxSnippets(ctx context.Context, arg store.PlatformListMailboxSnippetsParams) ([]store.MailboxSnippets, error) {
	var zero []store.MailboxSnippets
	return zero, s.record("PlatformListMailboxSnippets")
}

func (s *spyQuerier) PlatformListMailboxes(ctx context.Context) ([]store.PlatformListMailboxesRow, error) {
	var zero []store.PlatformListMailboxesRow
	return zero, s.record("PlatformListMailboxes")
}

func (s *spyQuerier) PlatformListManagedDNS(ctx context.Context) ([]store.PlatformListManagedDNSRow, error) {
	var zero []store.PlatformListManagedDNSRow
	return zero, s.record("PlatformListManagedDNS")
}

func (s *spyQuerier) PlatformListMarketingFlowSteps(ctx context.Context, arg store.PlatformListMarketingFlowStepsParams) ([]store.PlatformListMarketingFlowStepsRow, error) {
	var zero []store.PlatformListMarketingFlowStepsRow
	return zero, s.record("PlatformListMarketingFlowSteps")
}

func (s *spyQuerier) PlatformListOperationalAlerts(ctx context.Context) ([]store.OperationalAlerts, error) {
	var zero []store.OperationalAlerts
	return zero, s.record("PlatformListOperationalAlerts")
}

func (s *spyQuerier) PlatformListPublishedCorporateContacts(ctx context.Context, tenantID int64) ([]store.TenantDirectoryContacts, error) {
	var zero []store.TenantDirectoryContacts
	return zero, s.record("PlatformListPublishedCorporateContacts")
}

func (s *spyQuerier) PlatformListSendReceipts(ctx context.Context, arg store.PlatformListSendReceiptsParams) ([]store.MailboxSendReceipts, error) {
	var zero []store.MailboxSendReceipts
	return zero, s.record("PlatformListSendReceipts")
}

func (s *spyQuerier) PlatformListSettings(ctx context.Context) ([]store.PlatformListSettingsRow, error) {
	var zero []store.PlatformListSettingsRow
	return zero, s.record("PlatformListSettings")
}

func (s *spyQuerier) PlatformListSieveScripts(ctx context.Context) ([]store.PlatformListSieveScriptsRow, error) {
	var zero []store.PlatformListSieveScriptsRow
	return zero, s.record("PlatformListSieveScripts")
}

func (s *spyQuerier) PlatformListSpamDeliveryPolicies(ctx context.Context) ([]store.PlatformListSpamDeliveryPoliciesRow, error) {
	var zero []store.PlatformListSpamDeliveryPoliciesRow
	return zero, s.record("PlatformListSpamDeliveryPolicies")
}

func (s *spyQuerier) PlatformListSupportMessages(ctx context.Context, arg store.PlatformListSupportMessagesParams) ([]store.TicketMessages, error) {
	var zero []store.TicketMessages
	return zero, s.record("PlatformListSupportMessages")
}

func (s *spyQuerier) PlatformListSupportTickets(ctx context.Context, beforeID int64) ([]store.Tickets, error) {
	var zero []store.Tickets
	return zero, s.record("PlatformListSupportTickets")
}

func (s *spyQuerier) PlatformListTenantDirectoryContacts(ctx context.Context, tenantID int64) ([]store.TenantDirectoryContacts, error) {
	var zero []store.TenantDirectoryContacts
	return zero, s.record("PlatformListTenantDirectoryContacts")
}

func (s *spyQuerier) PlatformListTenants(ctx context.Context) ([]store.PlatformListTenantsRow, error) {
	var zero []store.PlatformListTenantsRow
	return zero, s.record("PlatformListTenants")
}

func (s *spyQuerier) PlatformListTransportSecurity(ctx context.Context) ([]store.PlatformListTransportSecurityRow, error) {
	var zero []store.PlatformListTransportSecurityRow
	return zero, s.record("PlatformListTransportSecurity")
}

func (s *spyQuerier) PlatformLockAIQuotaTenantReader(ctx context.Context, arg store.PlatformLockAIQuotaTenantReaderParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformLockAIQuotaTenantReader")
}

func (s *spyQuerier) PlatformLockAliasSenderMailboxes(ctx context.Context, arg store.PlatformLockAliasSenderMailboxesParams) ([]string, error) {
	var zero []string
	return zero, s.record("PlatformLockAliasSenderMailboxes")
}

func (s *spyQuerier) PlatformLockAliasSenderTarget(ctx context.Context, arg store.PlatformLockAliasSenderTargetParams) (store.PlatformLockAliasSenderTargetRow, error) {
	var zero store.PlatformLockAliasSenderTargetRow
	return zero, s.record("PlatformLockAliasSenderTarget")
}

func (s *spyQuerier) PlatformLockArchivePurgeCandidate(ctx context.Context, arg store.PlatformLockArchivePurgeCandidateParams) (store.PlatformLockArchivePurgeCandidateRow, error) {
	var zero store.PlatformLockArchivePurgeCandidateRow
	return zero, s.record("PlatformLockArchivePurgeCandidate")
}

func (s *spyQuerier) PlatformLockBillingTenant(ctx context.Context, p1 int64) error {
	return s.record("PlatformLockBillingTenant")
}

func (s *spyQuerier) PlatformLockBrandHost(ctx context.Context, hostname string) (store.BrandHosts, error) {
	var zero store.BrandHosts
	return zero, s.record("PlatformLockBrandHost")
}

func (s *spyQuerier) PlatformLockBrandSettingsBrand(ctx context.Context, id int64) (int64, error) {
	var zero int64
	return zero, s.record("PlatformLockBrandSettingsBrand")
}

func (s *spyQuerier) PlatformLockCalendarInvitationEvent(ctx context.Context, arg store.PlatformLockCalendarInvitationEventParams) (store.MailboxEvents, error) {
	var zero store.MailboxEvents
	return zero, s.record("PlatformLockCalendarInvitationEvent")
}

func (s *spyQuerier) PlatformLockCollectorCheckpoint(ctx context.Context, stream string) (store.CollectorCheckpoints, error) {
	var zero store.CollectorCheckpoints
	return zero, s.record("PlatformLockCollectorCheckpoint")
}

func (s *spyQuerier) PlatformLockDataImport(ctx context.Context, p1 store.PlatformLockDataImportParams) error {
	return s.record("PlatformLockDataImport")
}

func (s *spyQuerier) PlatformLockLegacyDKIMDomains(ctx context.Context, arg store.PlatformLockLegacyDKIMDomainsParams) ([]store.PlatformLockLegacyDKIMDomainsRow, error) {
	var zero []store.PlatformLockLegacyDKIMDomainsRow
	return zero, s.record("PlatformLockLegacyDKIMDomains")
}

func (s *spyQuerier) PlatformLockLegacyImportTarget(ctx context.Context, arg store.PlatformLockLegacyImportTargetParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformLockLegacyImportTarget")
}

func (s *spyQuerier) PlatformLockLegacyMFASecrets(ctx context.Context, limit int32) ([]store.PlatformLockLegacyMFASecretsRow, error) {
	var zero []store.PlatformLockLegacyMFASecretsRow
	return zero, s.record("PlatformLockLegacyMFASecrets")
}

func (s *spyQuerier) PlatformLockPlatformAdmin(ctx context.Context, id int64) (int64, error) {
	var zero int64
	return zero, s.record("PlatformLockPlatformAdmin")
}

func (s *spyQuerier) PlatformLockRecallAuthority(ctx context.Context, arg store.PlatformLockRecallAuthorityParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformLockRecallAuthority")
}

func (s *spyQuerier) PlatformLockRecallForExpunge(ctx context.Context, arg store.PlatformLockRecallForExpungeParams) (store.PlatformLockRecallForExpungeRow, error) {
	var zero store.PlatformLockRecallForExpungeRow
	return zero, s.record("PlatformLockRecallForExpunge")
}

func (s *spyQuerier) PlatformLockRecallMailbox(ctx context.Context, arg store.PlatformLockRecallMailboxParams) (string, error) {
	var zero string
	return zero, s.record("PlatformLockRecallMailbox")
}

func (s *spyQuerier) PlatformLockRecallTenant(ctx context.Context, p1 int64) error {
	return s.record("PlatformLockRecallTenant")
}

func (s *spyQuerier) PlatformLockRetentionPurgePolicy(ctx context.Context, arg store.PlatformLockRetentionPurgePolicyParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformLockRetentionPurgePolicy")
}

func (s *spyQuerier) PlatformLockSendDraft(ctx context.Context, p1 store.PlatformLockSendDraftParams) error {
	return s.record("PlatformLockSendDraft")
}

func (s *spyQuerier) PlatformMailboxAccessStates(ctx context.Context) ([]store.PlatformMailboxAccessStatesRow, error) {
	var zero []store.PlatformMailboxAccessStatesRow
	return zero, s.record("PlatformMailboxAccessStates")
}

func (s *spyQuerier) PlatformMailboxAuthLookup(ctx context.Context, address string) (store.PlatformMailboxAuthLookupRow, error) {
	var zero store.PlatformMailboxAuthLookupRow
	return zero, s.record("PlatformMailboxAuthLookup")
}

func (s *spyQuerier) PlatformMailboxPasswordFailures(ctx context.Context, arg store.PlatformMailboxPasswordFailuresParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformMailboxPasswordFailures")
}

func (s *spyQuerier) PlatformMailboxPasswordForSession(ctx context.Context, arg store.PlatformMailboxPasswordForSessionParams) (store.PlatformMailboxPasswordForSessionRow, error) {
	var zero store.PlatformMailboxPasswordForSessionRow
	return zero, s.record("PlatformMailboxPasswordForSession")
}

func (s *spyQuerier) PlatformMailboxRestoreIdentity(ctx context.Context, arg store.PlatformMailboxRestoreIdentityParams) (store.PlatformMailboxRestoreIdentityRow, error) {
	var zero store.PlatformMailboxRestoreIdentityRow
	return zero, s.record("PlatformMailboxRestoreIdentity")
}

func (s *spyQuerier) PlatformMailboxSenderIdentityAllowed(ctx context.Context, arg store.PlatformMailboxSenderIdentityAllowedParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformMailboxSenderIdentityAllowed")
}

func (s *spyQuerier) PlatformManagedBrands(ctx context.Context) ([]store.PlatformManagedBrandsRow, error) {
	var zero []store.PlatformManagedBrandsRow
	return zero, s.record("PlatformManagedBrands")
}

func (s *spyQuerier) PlatformMarkDomainReady(ctx context.Context, arg store.PlatformMarkDomainReadyParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformMarkDomainReady")
}

func (s *spyQuerier) PlatformMarkMarketingFlowUncertain(ctx context.Context, arg store.PlatformMarkMarketingFlowUncertainParams) (string, error) {
	var zero string
	return zero, s.record("PlatformMarkMarketingFlowUncertain")
}

func (s *spyQuerier) PlatformMarkMarketingFlowUncertainFenced(ctx context.Context, arg store.PlatformMarkMarketingFlowUncertainFencedParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformMarkMarketingFlowUncertainFenced")
}

func (s *spyQuerier) PlatformMarkMarketingFlowUnsubscribed(ctx context.Context, arg store.PlatformMarkMarketingFlowUnsubscribedParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformMarkMarketingFlowUnsubscribed")
}

func (s *spyQuerier) PlatformMarkMarketingRecipientRevoked(ctx context.Context, arg store.PlatformMarkMarketingRecipientRevokedParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformMarkMarketingRecipientRevoked")
}

func (s *spyQuerier) PlatformMarkScheduledSentCopyUnknown(ctx context.Context, arg store.PlatformMarkScheduledSentCopyUnknownParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformMarkScheduledSentCopyUnknown")
}

func (s *spyQuerier) PlatformMarkSendCopyUnknown(ctx context.Context, arg store.PlatformMarkSendCopyUnknownParams) (store.MailboxSendReceipts, error) {
	var zero store.MailboxSendReceipts
	return zero, s.record("PlatformMarkSendCopyUnknown")
}

func (s *spyQuerier) PlatformMarketingAttemptStillAllowed(ctx context.Context, arg store.PlatformMarketingAttemptStillAllowedParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformMarketingAttemptStillAllowed")
}

func (s *spyQuerier) PlatformMarketingFlowIsVersioned(ctx context.Context, arg store.PlatformMarketingFlowIsVersionedParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformMarketingFlowIsVersioned")
}

func (s *spyQuerier) PlatformMarketingFlowMayAdvance(ctx context.Context, arg store.PlatformMarketingFlowMayAdvanceParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformMarketingFlowMayAdvance")
}

func (s *spyQuerier) PlatformMarketingFlowStillAllowed(ctx context.Context, arg store.PlatformMarketingFlowStillAllowedParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformMarketingFlowStillAllowed")
}

func (s *spyQuerier) PlatformMaxNotificationID(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformMaxNotificationID")
}

func (s *spyQuerier) PlatformNextMailAccessBoundary(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformNextMailAccessBoundary")
}

func (s *spyQuerier) PlatformNotificationOutboxCounts(ctx context.Context) ([]store.PlatformNotificationOutboxCountsRow, error) {
	var zero []store.PlatformNotificationOutboxCountsRow
	return zero, s.record("PlatformNotificationOutboxCounts")
}

func (s *spyQuerier) PlatformOperationalStateCounts(ctx context.Context) ([]store.PlatformOperationalStateCountsRow, error) {
	var zero []store.PlatformOperationalStateCountsRow
	return zero, s.record("PlatformOperationalStateCounts")
}

func (s *spyQuerier) PlatformOverview(ctx context.Context) (store.PlatformOverviewRow, error) {
	var zero store.PlatformOverviewRow
	return zero, s.record("PlatformOverview")
}

func (s *spyQuerier) PlatformPendingAIUsage(ctx context.Context, tenantID int64) ([]store.PlatformPendingAIUsageRow, error) {
	var zero []store.PlatformPendingAIUsageRow
	return zero, s.record("PlatformPendingAIUsage")
}

func (s *spyQuerier) PlatformPrepareOnboardingTenant(ctx context.Context, id int64) error {
	return s.record("PlatformPrepareOnboardingTenant")
}

func (s *spyQuerier) PlatformProvisioningCertificateAllowed(ctx context.Context, fqdn string) (bool, error) {
	var zero bool
	return zero, s.record("PlatformProvisioningCertificateAllowed")
}

func (s *spyQuerier) PlatformPruneMailboxSessions(ctx context.Context) error {
	return s.record("PlatformPruneMailboxSessions")
}

func (s *spyQuerier) PlatformPruneMailboxTickets(ctx context.Context) error {
	return s.record("PlatformPruneMailboxTickets")
}

func (s *spyQuerier) PlatformPruneRoundcubeRequests(ctx context.Context) error {
	return s.record("PlatformPruneRoundcubeRequests")
}

func (s *spyQuerier) PlatformPublicThemeSettings(ctx context.Context, scopeID int64) ([]store.PlatformPublicThemeSettingsRow, error) {
	var zero []store.PlatformPublicThemeSettingsRow
	return zero, s.record("PlatformPublicThemeSettings")
}

func (s *spyQuerier) PlatformPublicTransportSecurity(ctx context.Context, fqdn string) (store.PlatformPublicTransportSecurityRow, error) {
	var zero store.PlatformPublicTransportSecurityRow
	return zero, s.record("PlatformPublicTransportSecurity")
}

func (s *spyQuerier) PlatformPublishDueCalendarReminders(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformPublishDueCalendarReminders")
}

func (s *spyQuerier) PlatformPutDeliveryQueueContext(ctx context.Context, arg store.PlatformPutDeliveryQueueContextParams) error {
	return s.record("PlatformPutDeliveryQueueContext")
}

func (s *spyQuerier) PlatformQuarantinePoisonEmailNotifications(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformQuarantinePoisonEmailNotifications")
}

func (s *spyQuerier) PlatformQueueAuthenticatedTenant(ctx context.Context, arg store.PlatformQueueAuthenticatedTenantParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformQueueAuthenticatedTenant")
}

func (s *spyQuerier) PlatformReadEvidenceBody(ctx context.Context, arg store.PlatformReadEvidenceBodyParams) ([]byte, error) {
	var zero []byte
	return zero, s.record("PlatformReadEvidenceBody")
}

func (s *spyQuerier) PlatformRecallActorAuthorized(ctx context.Context, arg store.PlatformRecallActorAuthorizedParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformRecallActorAuthorized")
}

func (s *spyQuerier) PlatformRecallHoldConflict(ctx context.Context, arg store.PlatformRecallHoldConflictParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformRecallHoldConflict")
}

func (s *spyQuerier) PlatformRecallPreviewSource(ctx context.Context, arg store.PlatformRecallPreviewSourceParams) (store.PlatformRecallPreviewSourceRow, error) {
	var zero store.PlatformRecallPreviewSourceRow
	return zero, s.record("PlatformRecallPreviewSource")
}

func (s *spyQuerier) PlatformRecordCollectedDeliveryEvent(ctx context.Context, arg store.PlatformRecordCollectedDeliveryEventParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecordCollectedDeliveryEvent")
}

func (s *spyQuerier) PlatformRecordMailboxUsage(ctx context.Context, arg store.PlatformRecordMailboxUsageParams) error {
	return s.record("PlatformRecordMailboxUsage")
}

func (s *spyQuerier) PlatformRecordMailboxUsageError(ctx context.Context, arg store.PlatformRecordMailboxUsageErrorParams) error {
	return s.record("PlatformRecordMailboxUsageError")
}

func (s *spyQuerier) PlatformRecordMarketingFeedback(ctx context.Context, arg store.PlatformRecordMarketingFeedbackParams) (string, error) {
	var zero string
	return zero, s.record("PlatformRecordMarketingFeedback")
}

func (s *spyQuerier) PlatformRecordMarketingFlowExecution(ctx context.Context, arg store.PlatformRecordMarketingFlowExecutionParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecordMarketingFlowExecution")
}

func (s *spyQuerier) PlatformRecordMarketingFlowNodeExecution(ctx context.Context, arg store.PlatformRecordMarketingFlowNodeExecutionParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecordMarketingFlowNodeExecution")
}

func (s *spyQuerier) PlatformRecordRoundcubeNonce(ctx context.Context, nonce string) error {
	return s.record("PlatformRecordRoundcubeNonce")
}

func (s *spyQuerier) PlatformRecordSchedulingReply(ctx context.Context, arg store.PlatformRecordSchedulingReplyParams) (store.PlatformRecordSchedulingReplyRow, error) {
	var zero store.PlatformRecordSchedulingReplyRow
	return zero, s.record("PlatformRecordSchedulingReply")
}

func (s *spyQuerier) PlatformRecordTLSReport(ctx context.Context, arg store.PlatformRecordTLSReportParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecordTLSReport")
}

func (s *spyQuerier) PlatformRecoverMarketingClaims(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecoverMarketingClaims")
}

func (s *spyQuerier) PlatformRecoverMarketingFlowLeases(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecoverMarketingFlowLeases")
}

func (s *spyQuerier) PlatformRecoverMarketingFlowUncertain(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecoverMarketingFlowUncertain")
}

func (s *spyQuerier) PlatformRecoverMarketingSubmissions(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecoverMarketingSubmissions")
}

func (s *spyQuerier) PlatformRecoverSendReceipts(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecoverSendReceipts")
}

func (s *spyQuerier) PlatformRecoverStaleMailboxScheduledSends(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRecoverStaleMailboxScheduledSends")
}

func (s *spyQuerier) PlatformRefreshAIAlerts(ctx context.Context) error {
	return s.record("PlatformRefreshAIAlerts")
}

func (s *spyQuerier) PlatformRefreshDNSOnboardingAlerts(ctx context.Context) error {
	return s.record("PlatformRefreshDNSOnboardingAlerts")
}

func (s *spyQuerier) PlatformRehashPanelPassword(ctx context.Context, arg store.PlatformRehashPanelPasswordParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRehashPanelPassword")
}

func (s *spyQuerier) PlatformReleaseDraftSessionLock(ctx context.Context, arg store.PlatformReleaseDraftSessionLockParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformReleaseDraftSessionLock")
}

func (s *spyQuerier) PlatformReleaseMarketingClaim(ctx context.Context, arg store.PlatformReleaseMarketingClaimParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformReleaseMarketingClaim")
}

func (s *spyQuerier) PlatformReleaseMarketingFlowLease(ctx context.Context, arg store.PlatformReleaseMarketingFlowLeaseParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformReleaseMarketingFlowLease")
}

func (s *spyQuerier) PlatformReleaseMarketingFlowReservation(ctx context.Context, arg store.PlatformReleaseMarketingFlowReservationParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformReleaseMarketingFlowReservation")
}

func (s *spyQuerier) PlatformRemoveContactGroupMember(ctx context.Context, arg store.PlatformRemoveContactGroupMemberParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRemoveContactGroupMember")
}

func (s *spyQuerier) PlatformRenameTenant(ctx context.Context, arg store.PlatformRenameTenantParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRenameTenant")
}

func (s *spyQuerier) PlatformRenewDNSLease(ctx context.Context, arg store.PlatformRenewDNSLeaseParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRenewDNSLease")
}

func (s *spyQuerier) PlatformRenewOnboardingLease(ctx context.Context, arg store.PlatformRenewOnboardingLeaseParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRenewOnboardingLease")
}

func (s *spyQuerier) PlatformReplySupportTicket(ctx context.Context, arg store.PlatformReplySupportTicketParams) (store.TicketMessages, error) {
	var zero store.TicketMessages
	return zero, s.record("PlatformReplySupportTicket")
}

func (s *spyQuerier) PlatformRequeueMailboxScheduledSend(ctx context.Context, arg store.PlatformRequeueMailboxScheduledSendParams) (store.MailboxScheduledSends, error) {
	var zero store.MailboxScheduledSends
	return zero, s.record("PlatformRequeueMailboxScheduledSend")
}

func (s *spyQuerier) PlatformReserveAIRequest(ctx context.Context, arg store.PlatformReserveAIRequestParams) (store.AiUsageRequests, error) {
	var zero store.AiUsageRequests
	return zero, s.record("PlatformReserveAIRequest")
}

func (s *spyQuerier) PlatformReserveMailboxAIRequest(ctx context.Context, arg store.PlatformReserveMailboxAIRequestParams) (store.AiUsageRequests, error) {
	var zero store.AiUsageRequests
	return zero, s.record("PlatformReserveMailboxAIRequest")
}

func (s *spyQuerier) PlatformResetBrandTheme(ctx context.Context, id int64) (int64, error) {
	var zero int64
	return zero, s.record("PlatformResetBrandTheme")
}

func (s *spyQuerier) PlatformResetDeliveryQueueContexts(ctx context.Context, stream string) error {
	return s.record("PlatformResetDeliveryQueueContexts")
}

func (s *spyQuerier) PlatformResolveAIUsage(ctx context.Context, arg store.PlatformResolveAIUsageParams) (store.PlatformResolveAIUsageRow, error) {
	var zero store.PlatformResolveAIUsageRow
	return zero, s.record("PlatformResolveAIUsage")
}

func (s *spyQuerier) PlatformResolveAuditTargetTenant(ctx context.Context, arg store.PlatformResolveAuditTargetTenantParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformResolveAuditTargetTenant")
}

func (s *spyQuerier) PlatformResolveDeliveryMailboxTenant(ctx context.Context, address string) (int64, error) {
	var zero int64
	return zero, s.record("PlatformResolveDeliveryMailboxTenant")
}

func (s *spyQuerier) PlatformResolveMarketingAttempt(ctx context.Context, arg store.PlatformResolveMarketingAttemptParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformResolveMarketingAttempt")
}

func (s *spyQuerier) PlatformResolvePinnedArchiveMailbox(ctx context.Context, arg store.PlatformResolvePinnedArchiveMailboxParams) (store.PlatformResolvePinnedArchiveMailboxRow, error) {
	var zero store.PlatformResolvePinnedArchiveMailboxRow
	return zero, s.record("PlatformResolvePinnedArchiveMailbox")
}

func (s *spyQuerier) PlatformRestoreSyncEpoch(ctx context.Context) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRestoreSyncEpoch")
}

func (s *spyQuerier) PlatformReviewDNSPlan(ctx context.Context, arg store.PlatformReviewDNSPlanParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformReviewDNSPlan")
}

func (s *spyQuerier) PlatformRevokeInvestigation(ctx context.Context, arg store.PlatformRevokeInvestigationParams) error {
	return s.record("PlatformRevokeInvestigation")
}

func (s *spyQuerier) PlatformRevokeMailboxCredentials(ctx context.Context, arg store.PlatformRevokeMailboxCredentialsParams) error {
	return s.record("PlatformRevokeMailboxCredentials")
}

func (s *spyQuerier) PlatformRevokeMailboxSession(ctx context.Context, tokenHash string) error {
	return s.record("PlatformRevokeMailboxSession")
}

func (s *spyQuerier) PlatformRevokeMailboxSessions(ctx context.Context, arg store.PlatformRevokeMailboxSessionsParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRevokeMailboxSessions")
}

func (s *spyQuerier) PlatformRevokeSieveBridge(ctx context.Context, tokenHash string) error {
	return s.record("PlatformRevokeSieveBridge")
}

func (s *spyQuerier) PlatformRotateDomainRecoveryToken(ctx context.Context, arg store.PlatformRotateDomainRecoveryTokenParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRotateDomainRecoveryToken")
}

func (s *spyQuerier) PlatformRotatePanelPassword(ctx context.Context, arg store.PlatformRotatePanelPasswordParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformRotatePanelPassword")
}

func (s *spyQuerier) PlatformSaveBrand(ctx context.Context, arg store.PlatformSaveBrandParams) (store.PlatformSaveBrandRow, error) {
	var zero store.PlatformSaveBrandRow
	return zero, s.record("PlatformSaveBrand")
}

func (s *spyQuerier) PlatformSaveMarketingFlowPosition(ctx context.Context, arg store.PlatformSaveMarketingFlowPositionParams) error {
	return s.record("PlatformSaveMarketingFlowPosition")
}

func (s *spyQuerier) PlatformScheduleCalendarReminder(ctx context.Context, arg store.PlatformScheduleCalendarReminderParams) error {
	return s.record("PlatformScheduleCalendarReminder")
}

func (s *spyQuerier) PlatformSealLegacyScheduledCopy(ctx context.Context, arg store.PlatformSealLegacyScheduledCopyParams) error {
	return s.record("PlatformSealLegacyScheduledCopy")
}

func (s *spyQuerier) PlatformSearchContacts(ctx context.Context, arg store.PlatformSearchContactsParams) ([]store.MailboxContacts, error) {
	var zero []store.MailboxContacts
	return zero, s.record("PlatformSearchContacts")
}

func (s *spyQuerier) PlatformSearchPublishedCorporateContacts(ctx context.Context, arg store.PlatformSearchPublishedCorporateContactsParams) ([]store.TenantDirectoryContacts, error) {
	var zero []store.TenantDirectoryContacts
	return zero, s.record("PlatformSearchPublishedCorporateContacts")
}

func (s *spyQuerier) PlatformSetAIBudget(ctx context.Context, arg store.PlatformSetAIBudgetParams) error {
	return s.record("PlatformSetAIBudget")
}

func (s *spyQuerier) PlatformSetAIProvider(ctx context.Context, arg store.PlatformSetAIProviderParams) (store.AiProviderConfigs, error) {
	var zero store.AiProviderConfigs
	return zero, s.record("PlatformSetAIProvider")
}

func (s *spyQuerier) PlatformSetActiveAIService(ctx context.Context, arg store.PlatformSetActiveAIServiceParams) error {
	return s.record("PlatformSetActiveAIService")
}

func (s *spyQuerier) PlatformSetAliasSenderLogins(ctx context.Context, arg store.PlatformSetAliasSenderLoginsParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformSetAliasSenderLogins")
}

func (s *spyQuerier) PlatformSetArchiveOriginalHash(ctx context.Context, arg store.PlatformSetArchiveOriginalHashParams) error {
	return s.record("PlatformSetArchiveOriginalHash")
}

func (s *spyQuerier) PlatformSetBrandHostLifecycle(ctx context.Context, arg store.PlatformSetBrandHostLifecycleParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformSetBrandHostLifecycle")
}

func (s *spyQuerier) PlatformSetBrandStatus(ctx context.Context, arg store.PlatformSetBrandStatusParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformSetBrandStatus")
}

func (s *spyQuerier) PlatformSetDNSSecurity(ctx context.Context, arg store.PlatformSetDNSSecurityParams) (store.DomainDnsSecurity, error) {
	var zero store.DomainDnsSecurity
	return zero, s.record("PlatformSetDNSSecurity")
}

func (s *spyQuerier) PlatformSetDraftSaveScope(ctx context.Context, arg store.PlatformSetDraftSaveScopeParams) error {
	return s.record("PlatformSetDraftSaveScope")
}

func (s *spyQuerier) PlatformSetExplicitDNS(ctx context.Context, arg store.PlatformSetExplicitDNSParams) error {
	return s.record("PlatformSetExplicitDNS")
}

func (s *spyQuerier) PlatformSetLegacyDKIM(ctx context.Context, arg store.PlatformSetLegacyDKIMParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformSetLegacyDKIM")
}

func (s *spyQuerier) PlatformSetMailboxSieve(ctx context.Context, arg store.PlatformSetMailboxSieveParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformSetMailboxSieve")
}

func (s *spyQuerier) PlatformSetNonplatformAIQuota(ctx context.Context, arg store.PlatformSetNonplatformAIQuotaParams) error {
	return s.record("PlatformSetNonplatformAIQuota")
}

func (s *spyQuerier) PlatformSetOperationalCondition(ctx context.Context, arg store.PlatformSetOperationalConditionParams) error {
	return s.record("PlatformSetOperationalCondition")
}

func (s *spyQuerier) PlatformSetTenantDirectorySettings(ctx context.Context, arg store.PlatformSetTenantDirectorySettingsParams) (store.TenantDirectorySettings, error) {
	var zero store.TenantDirectorySettings
	return zero, s.record("PlatformSetTenantDirectorySettings")
}

func (s *spyQuerier) PlatformSetTenantOwner(ctx context.Context, arg store.PlatformSetTenantOwnerParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformSetTenantOwner")
}

func (s *spyQuerier) PlatformSetupTenant(ctx context.Context, arg store.PlatformSetupTenantParams) (store.PlatformSetupTenantRow, error) {
	var zero store.PlatformSetupTenantRow
	return zero, s.record("PlatformSetupTenant")
}

func (s *spyQuerier) PlatformSieveOutboundAllowed(ctx context.Context, address string) (bool, error) {
	var zero bool
	return zero, s.record("PlatformSieveOutboundAllowed")
}

func (s *spyQuerier) PlatformSkipMarketingRecipient(ctx context.Context, arg store.PlatformSkipMarketingRecipientParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformSkipMarketingRecipient")
}

func (s *spyQuerier) PlatformSubmitDNSAction(ctx context.Context, arg store.PlatformSubmitDNSActionParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformSubmitDNSAction")
}

func (s *spyQuerier) PlatformSubmitDNSRollback(ctx context.Context, arg store.PlatformSubmitDNSRollbackParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformSubmitDNSRollback")
}

func (s *spyQuerier) PlatformSumAIUsageBetween(ctx context.Context, arg store.PlatformSumAIUsageBetweenParams) (store.PlatformSumAIUsageBetweenRow, error) {
	var zero store.PlatformSumAIUsageBetweenRow
	return zero, s.record("PlatformSumAIUsageBetween")
}

func (s *spyQuerier) PlatformTakeMailboxDailyQuota(ctx context.Context, arg store.PlatformTakeMailboxDailyQuotaParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformTakeMailboxDailyQuota")
}

func (s *spyQuerier) PlatformTakeReminderScan(ctx context.Context) ([]store.MailboxEvents, error) {
	var zero []store.MailboxEvents
	return zero, s.record("PlatformTakeReminderScan")
}

func (s *spyQuerier) PlatformTenantBrandMatches(ctx context.Context, arg store.PlatformTenantBrandMatchesParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformTenantBrandMatches")
}

func (s *spyQuerier) PlatformTransportCertificateAllowed(ctx context.Context, fqdn string) (bool, error) {
	var zero bool
	return zero, s.record("PlatformTransportCertificateAllowed")
}

func (s *spyQuerier) PlatformUnbindBrandHost(ctx context.Context, hostname string) (int64, error) {
	var zero int64
	return zero, s.record("PlatformUnbindBrandHost")
}

func (s *spyQuerier) PlatformUpdateAIDraft(ctx context.Context, arg store.PlatformUpdateAIDraftParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformUpdateAIDraft")
}

func (s *spyQuerier) PlatformUpdateAnnouncement(ctx context.Context, arg store.PlatformUpdateAnnouncementParams) (store.Announcements, error) {
	var zero store.Announcements
	return zero, s.record("PlatformUpdateAnnouncement")
}

func (s *spyQuerier) PlatformUpdateContact(ctx context.Context, arg store.PlatformUpdateContactParams) (store.PlatformUpdateContactRow, error) {
	var zero store.PlatformUpdateContactRow
	return zero, s.record("PlatformUpdateContact")
}

func (s *spyQuerier) PlatformUpdateContactEnhanced(ctx context.Context, arg store.PlatformUpdateContactEnhancedParams) (store.PlatformUpdateContactEnhancedRow, error) {
	var zero store.PlatformUpdateContactEnhancedRow
	return zero, s.record("PlatformUpdateContactEnhanced")
}

func (s *spyQuerier) PlatformUpdateEvent(ctx context.Context, arg store.PlatformUpdateEventParams) (store.PlatformUpdateEventRow, error) {
	var zero store.PlatformUpdateEventRow
	return zero, s.record("PlatformUpdateEvent")
}

func (s *spyQuerier) PlatformUpdateMailboxSettings(ctx context.Context, arg store.PlatformUpdateMailboxSettingsParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformUpdateMailboxSettings")
}

func (s *spyQuerier) PlatformUpdateMailboxSnippet(ctx context.Context, arg store.PlatformUpdateMailboxSnippetParams) (store.MailboxSnippets, error) {
	var zero store.MailboxSnippets
	return zero, s.record("PlatformUpdateMailboxSnippet")
}

func (s *spyQuerier) PlatformUpdateMarketingChannel(ctx context.Context, arg store.PlatformUpdateMarketingChannelParams) (store.PlatformUpdateMarketingChannelRow, error) {
	var zero store.PlatformUpdateMarketingChannelRow
	return zero, s.record("PlatformUpdateMarketingChannel")
}

func (s *spyQuerier) PlatformUpdateTenantDirectoryContact(ctx context.Context, arg store.PlatformUpdateTenantDirectoryContactParams) (store.TenantDirectoryContacts, error) {
	var zero store.TenantDirectoryContacts
	return zero, s.record("PlatformUpdateTenantDirectoryContact")
}

func (s *spyQuerier) PlatformUpdateTenantStatus(ctx context.Context, arg store.PlatformUpdateTenantStatusParams) (store.PlatformUpdateTenantStatusRow, error) {
	var zero store.PlatformUpdateTenantStatusRow
	return zero, s.record("PlatformUpdateTenantStatus")
}

func (s *spyQuerier) PlatformUpsertBrand(ctx context.Context, arg store.PlatformUpsertBrandParams) (store.PlatformUpsertBrandRow, error) {
	var zero store.PlatformUpsertBrandRow
	return zero, s.record("PlatformUpsertBrand")
}

func (s *spyQuerier) PlatformUpsertBrandSetting(ctx context.Context, arg store.PlatformUpsertBrandSettingParams) error {
	return s.record("PlatformUpsertBrandSetting")
}

func (s *spyQuerier) PlatformUpsertChannel(ctx context.Context, arg store.PlatformUpsertChannelParams) (store.Channels, error) {
	var zero store.Channels
	return zero, s.record("PlatformUpsertChannel")
}

func (s *spyQuerier) PlatformUpsertMailboxPhishingFinding(ctx context.Context, arg store.PlatformUpsertMailboxPhishingFindingParams) error {
	return s.record("PlatformUpsertMailboxPhishingFinding")
}

func (s *spyQuerier) PlatformUpsertSetting(ctx context.Context, arg store.PlatformUpsertSettingParams) error {
	return s.record("PlatformUpsertSetting")
}

func (s *spyQuerier) PlatformUsageMailboxes(ctx context.Context) ([]store.PlatformUsageMailboxesRow, error) {
	var zero []store.PlatformUsageMailboxesRow
	return zero, s.record("PlatformUsageMailboxes")
}

func (s *spyQuerier) PlatformVerifyBrandHostProof(ctx context.Context, arg store.PlatformVerifyBrandHostProofParams) (int64, error) {
	var zero int64
	return zero, s.record("PlatformVerifyBrandHostProof")
}

func (s *spyQuerier) PlatformVersionedMarketingFlowMayAdvance(ctx context.Context, arg store.PlatformVersionedMarketingFlowMayAdvanceParams) (bool, error) {
	var zero bool
	return zero, s.record("PlatformVersionedMarketingFlowMayAdvance")
}

func (s *spyQuerier) PreviewArchiveRetention(ctx context.Context, arg store.PreviewArchiveRetentionParams) (store.RetentionPreviews, error) {
	var zero store.RetentionPreviews
	return zero, s.record("PreviewArchiveRetention")
}

func (s *spyQuerier) PreviewMarketingCampaign(ctx context.Context, arg store.PreviewMarketingCampaignParams) (store.PreviewMarketingCampaignRow, error) {
	var zero store.PreviewMarketingCampaignRow
	return zero, s.record("PreviewMarketingCampaign")
}

func (s *spyQuerier) PublicBrandByHost(ctx context.Context, hostname string) (store.PublicBrandByHostRow, error) {
	var zero store.PublicBrandByHostRow
	return zero, s.record("PublicBrandByHost")
}

func (s *spyQuerier) PublicBrandBySlug(ctx context.Context, slug string) (store.PublicBrandBySlugRow, error) {
	var zero store.PublicBrandBySlugRow
	return zero, s.record("PublicBrandBySlug")
}

func (s *spyQuerier) PublishDomainTransportSecurity(ctx context.Context, arg store.PublishDomainTransportSecurityParams) (store.DomainTransportSecurity, error) {
	var zero store.DomainTransportSecurity
	return zero, s.record("PublishDomainTransportSecurity")
}

func (s *spyQuerier) PublishMarketingFlowDefinition(ctx context.Context, arg store.PublishMarketingFlowDefinitionParams) error {
	return s.record("PublishMarketingFlowDefinition")
}

func (s *spyQuerier) PurgeMailboxAIChunks(ctx context.Context, arg store.PurgeMailboxAIChunksParams) (int64, error) {
	var zero int64
	return zero, s.record("PurgeMailboxAIChunks")
}

func (s *spyQuerier) RecallMessageCopies(ctx context.Context, arg store.RecallMessageCopiesParams) ([]store.RecallMessageCopiesRow, error) {
	var zero []store.RecallMessageCopiesRow
	return zero, s.record("RecallMessageCopies")
}

func (s *spyQuerier) RecordDeliveryEvent(ctx context.Context, arg store.RecordDeliveryEventParams) (int64, error) {
	var zero int64
	return zero, s.record("RecordDeliveryEvent")
}

func (s *spyQuerier) RecordMailboxImportAudit(ctx context.Context, arg store.RecordMailboxImportAuditParams) error {
	return s.record("RecordMailboxImportAudit")
}

func (s *spyQuerier) RecordMarketingFlowAudit(ctx context.Context, arg store.RecordMarketingFlowAuditParams) error {
	return s.record("RecordMarketingFlowAudit")
}

func (s *spyQuerier) RecordTenantAudit(ctx context.Context, arg store.RecordTenantAuditParams) error {
	return s.record("RecordTenantAudit")
}

func (s *spyQuerier) RemoveMarketingSuppression(ctx context.Context, arg store.RemoveMarketingSuppressionParams) (int64, error) {
	var zero int64
	return zero, s.record("RemoveMarketingSuppression")
}

func (s *spyQuerier) RequestDNSRollback(ctx context.Context, arg store.RequestDNSRollbackParams) error {
	return s.record("RequestDNSRollback")
}

func (s *spyQuerier) RequestOnboardingRetry(ctx context.Context, arg store.RequestOnboardingRetryParams) error {
	return s.record("RequestOnboardingRetry")
}

func (s *spyQuerier) ReserveDNSZoneVersion(ctx context.Context, arg store.ReserveDNSZoneVersionParams) (int64, error) {
	var zero int64
	return zero, s.record("ReserveDNSZoneVersion")
}

func (s *spyQuerier) RestoreArchiveCopy(ctx context.Context, arg store.RestoreArchiveCopyParams) (int64, error) {
	var zero int64
	return zero, s.record("RestoreArchiveCopy")
}

func (s *spyQuerier) RetryMarketingFlowInstance(ctx context.Context, arg store.RetryMarketingFlowInstanceParams) (bool, error) {
	var zero bool
	return zero, s.record("RetryMarketingFlowInstance")
}

func (s *spyQuerier) ReviewDNSPlan(ctx context.Context, arg store.ReviewDNSPlanParams) error {
	return s.record("ReviewDNSPlan")
}

func (s *spyQuerier) RevokeApplicationCredential(ctx context.Context, arg store.RevokeApplicationCredentialParams) (int64, error) {
	var zero int64
	return zero, s.record("RevokeApplicationCredential")
}

func (s *spyQuerier) RevokeComplianceGrant(ctx context.Context, arg store.RevokeComplianceGrantParams) (store.ComplianceGrants, error) {
	var zero store.ComplianceGrants
	return zero, s.record("RevokeComplianceGrant")
}

func (s *spyQuerier) RevokeDNSCredential(ctx context.Context, arg store.RevokeDNSCredentialParams) error {
	return s.record("RevokeDNSCredential")
}

func (s *spyQuerier) RotateDomainChallenge(ctx context.Context, arg store.RotateDomainChallengeParams) (store.RotateDomainChallengeRow, error) {
	var zero store.RotateDomainChallengeRow
	return zero, s.record("RotateDomainChallenge")
}

func (s *spyQuerier) SaveDNSCredential(ctx context.Context, arg store.SaveDNSCredentialParams) (int64, error) {
	var zero int64
	return zero, s.record("SaveDNSCredential")
}

func (s *spyQuerier) ScheduleMarketingCampaign(ctx context.Context, arg store.ScheduleMarketingCampaignParams) (string, error) {
	var zero string
	return zero, s.record("ScheduleMarketingCampaign")
}

func (s *spyQuerier) SearchArchiveContent(ctx context.Context, arg store.SearchArchiveContentParams) ([]store.SearchArchiveContentRow, error) {
	var zero []store.SearchArchiveContentRow
	return zero, s.record("SearchArchiveContent")
}

func (s *spyQuerier) SearchMailboxAIChunks(ctx context.Context, arg store.SearchMailboxAIChunksParams) ([]store.SearchMailboxAIChunksRow, error) {
	var zero []store.SearchMailboxAIChunksRow
	return zero, s.record("SearchMailboxAIChunks")
}

func (s *spyQuerier) SearchMailboxAIChunksInScope(ctx context.Context, arg store.SearchMailboxAIChunksInScopeParams) ([]store.SearchMailboxAIChunksInScopeRow, error) {
	var zero []store.SearchMailboxAIChunksInScopeRow
	return zero, s.record("SearchMailboxAIChunksInScope")
}

func (s *spyQuerier) SetArchiveEntryLegalHold(ctx context.Context, arg store.SetArchiveEntryLegalHoldParams) (store.ArchiveEntries, error) {
	var zero store.ArchiveEntries
	return zero, s.record("SetArchiveEntryLegalHold")
}

func (s *spyQuerier) SetMFASecret(ctx context.Context, arg store.SetMFASecretParams) error {
	return s.record("SetMFASecret")
}

func (s *spyQuerier) SetMailboxEASPingConfig(ctx context.Context, arg store.SetMailboxEASPingConfigParams) (int64, error) {
	var zero int64
	return zero, s.record("SetMailboxEASPingConfig")
}

func (s *spyQuerier) SetMailboxImportItem(ctx context.Context, arg store.SetMailboxImportItemParams) (int64, error) {
	var zero int64
	return zero, s.record("SetMailboxImportItem")
}

func (s *spyQuerier) SetMailboxImportJob(ctx context.Context, arg store.SetMailboxImportJobParams) (store.MailboxImportJobs, error) {
	var zero store.MailboxImportJobs
	return zero, s.record("SetMailboxImportJob")
}

func (s *spyQuerier) SetMarketingReportBudget(ctx context.Context) error {
	return s.record("SetMarketingReportBudget")
}

func (s *spyQuerier) SoftDeleteArchiveEntry(ctx context.Context, arg store.SoftDeleteArchiveEntryParams) (int64, error) {
	var zero int64
	return zero, s.record("SoftDeleteArchiveEntry")
}

func (s *spyQuerier) StopMarketingFlowInstance(ctx context.Context, arg store.StopMarketingFlowInstanceParams) (int64, error) {
	var zero int64
	return zero, s.record("StopMarketingFlowInstance")
}

func (s *spyQuerier) StoreRecallReviewPreview(ctx context.Context, arg store.StoreRecallReviewPreviewParams) (store.RecallRequests, error) {
	var zero store.RecallRequests
	return zero, s.record("StoreRecallReviewPreview")
}

func (s *spyQuerier) SupportDiagnosticMailboxCounts(ctx context.Context, tenantID int64) (store.SupportDiagnosticMailboxCountsRow, error) {
	var zero store.SupportDiagnosticMailboxCountsRow
	return zero, s.record("SupportDiagnosticMailboxCounts")
}

func (s *spyQuerier) TenantConsentAICloud(ctx context.Context, arg store.TenantConsentAICloudParams) error {
	return s.record("TenantConsentAICloud")
}

func (s *spyQuerier) TenantDeliveryOutcomeCounts(ctx context.Context, arg store.TenantDeliveryOutcomeCountsParams) ([]store.TenantDeliveryOutcomeCountsRow, error) {
	var zero []store.TenantDeliveryOutcomeCountsRow
	return zero, s.record("TenantDeliveryOutcomeCounts")
}

func (s *spyQuerier) UpdateAlias(ctx context.Context, arg store.UpdateAliasParams) (store.MailboxAliases, error) {
	var zero store.MailboxAliases
	return zero, s.record("UpdateAlias")
}

func (s *spyQuerier) UpdateDomainModeStatus(ctx context.Context, arg store.UpdateDomainModeStatusParams) (store.UpdateDomainModeStatusRow, error) {
	var zero store.UpdateDomainModeStatusRow
	return zero, s.record("UpdateDomainModeStatus")
}

func (s *spyQuerier) UpdateDomainStatus(ctx context.Context, arg store.UpdateDomainStatusParams) (store.UpdateDomainStatusRow, error) {
	var zero store.UpdateDomainStatusRow
	return zero, s.record("UpdateDomainStatus")
}

func (s *spyQuerier) UpdateMailboxPassword(ctx context.Context, arg store.UpdateMailboxPasswordParams) (store.Mailboxes, error) {
	var zero store.Mailboxes
	return zero, s.record("UpdateMailboxPassword")
}

func (s *spyQuerier) UpdateMailboxQuota(ctx context.Context, arg store.UpdateMailboxQuotaParams) (store.Mailboxes, error) {
	var zero store.Mailboxes
	return zero, s.record("UpdateMailboxQuota")
}

func (s *spyQuerier) UpdateMailboxSettings(ctx context.Context, arg store.UpdateMailboxSettingsParams) (store.UpdateMailboxSettingsRow, error) {
	var zero store.UpdateMailboxSettingsRow
	return zero, s.record("UpdateMailboxSettings")
}

func (s *spyQuerier) UpdateMailboxStatus(ctx context.Context, arg store.UpdateMailboxStatusParams) (store.Mailboxes, error) {
	var zero store.Mailboxes
	return zero, s.record("UpdateMailboxStatus")
}

func (s *spyQuerier) UpdateMarketingFlowDefinition(ctx context.Context, arg store.UpdateMarketingFlowDefinitionParams) (store.MarketingFlowDefinitions, error) {
	var zero store.MarketingFlowDefinitions
	return zero, s.record("UpdateMarketingFlowDefinition")
}

func (s *spyQuerier) UpdateSessionTenant(ctx context.Context, arg store.UpdateSessionTenantParams) error {
	return s.record("UpdateSessionTenant")
}

func (s *spyQuerier) UpdateTenantSettings(ctx context.Context, arg store.UpdateTenantSettingsParams) (store.Tenants, error) {
	var zero store.Tenants
	return zero, s.record("UpdateTenantSettings")
}

func (s *spyQuerier) UpdateTenantStatus(ctx context.Context, arg store.UpdateTenantStatusParams) (store.Tenants, error) {
	var zero store.Tenants
	return zero, s.record("UpdateTenantStatus")
}

func (s *spyQuerier) UpdateUserPassword(ctx context.Context, arg store.UpdateUserPasswordParams) error {
	return s.record("UpdateUserPassword")
}

func (s *spyQuerier) UpsertMarketingContact(ctx context.Context, arg store.UpsertMarketingContactParams) (store.MarketingContacts, error) {
	var zero store.MarketingContacts
	return zero, s.record("UpsertMarketingContact")
}

func (s *spyQuerier) UpsertRetentionPolicy(ctx context.Context, arg store.UpsertRetentionPolicyParams) (store.RetentionPolicies, error) {
	var zero store.RetentionPolicies
	return zero, s.record("UpsertRetentionPolicy")
}

func (s *spyQuerier) UpsertTenantSetting(ctx context.Context, arg store.UpsertTenantSettingParams) (store.Settings, error) {
	var zero store.Settings
	return zero, s.record("UpsertTenantSetting")
}

func (s *spyQuerier) VerifyDomainChallenge(ctx context.Context, arg store.VerifyDomainChallengeParams) (store.VerifyDomainChallengeRow, error) {
	var zero store.VerifyDomainChallengeRow
	return zero, s.record("VerifyDomainChallenge")
}
