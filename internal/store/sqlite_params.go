package store

import "github.com/jackc/pgx/v5/pgtype"

// ---------------------------------------------------------------------------
// B5a: parameter and row types for statements that moved out of the sqlc
// generated files into sqlite_methods.go (S4/S5/S7), plus the row types the
// Querier interface still names. sqlc regenerated the tenant query files from
// the SQLite statements, which no longer carry these `-- name:` blocks, so the
// structs would otherwise disappear and break the handwritten signatures.
//
// The definitions are copied verbatim from the pre-B5a generated files and
// must keep their field names, order and json tags: handlers construct them
// by field name and the store round-trips the json tags.
// ---------------------------------------------------------------------------

type ArchiveCasesCreateParams struct {
	TenantID   int64              `json:"tenant_id"`
	ActorID    int64              `json:"actor_id"`
	Title      string             `json:"title"`
	Purpose    string             `json:"purpose"`
	ExpiresAt  pgtype.Timestamptz `json:"expires_at"`
	RequestKey string             `json:"request_key"`
}

type RecordMarketingFlowAuditParams struct {
	PTenant     int64  `json:"p_tenant"`
	PDefinition int64  `json:"p_definition"`
	PAction     string `json:"p_action"`
	PDetail     []byte `json:"p_detail"`
}

type RetryMarketingFlowInstanceParams struct {
	PTenant               int64 `json:"p_tenant"`
	PDefinition           int64 `json:"p_definition"`
	PPosition             int64 `json:"p_position"`
	PConfirmedNotAccepted bool  `json:"p_confirmed_not_accepted"`
}

type SoftDeleteArchiveEntryParams struct {
	TenantID int64 `json:"tenant_id"`
	ID       int64 `json:"id"`
}

type ConfirmRecallReviewRow struct {
	ID               int64              `json:"id"`
	TenantID         int64              `json:"tenant_id"`
	MessageID        string             `json:"message_id"`
	RequestedBy      int64              `json:"requested_by"`
	Status           string             `json:"status"`
	Reason           pgtype.Text        `json:"reason"`
	CreatedAt        pgtype.Timestamptz `json:"created_at"`
	ArchiveEntryID   pgtype.Int8        `json:"archive_entry_id"`
	MailboxID        pgtype.Int8        `json:"mailbox_id"`
	CompletedAt      pgtype.Timestamptz `json:"completed_at"`
	PreviewActorID   pgtype.Int8        `json:"preview_actor_id"`
	PreviewToken     pgtype.Text        `json:"preview_token"`
	PreviewDigest    pgtype.Text        `json:"preview_digest"`
	PreviewSnapshot  []byte             `json:"preview_snapshot"`
	PreviewExpiresAt pgtype.Timestamptz `json:"preview_expires_at"`
	ConfirmedAt      pgtype.Timestamptz `json:"confirmed_at"`
	SelectedCopyKeys []byte             `json:"selected_copy_keys"`
	AttemptEpoch     int64              `json:"attempt_epoch"`
	LocalResult      []byte             `json:"local_result"`
	ResultHistory    []byte             `json:"result_history"`
}

type StoreRecallReviewPreviewRow struct {
	ID               int64              `json:"id"`
	TenantID         int64              `json:"tenant_id"`
	MessageID        string             `json:"message_id"`
	RequestedBy      int64              `json:"requested_by"`
	Status           string             `json:"status"`
	Reason           pgtype.Text        `json:"reason"`
	CreatedAt        pgtype.Timestamptz `json:"created_at"`
	ArchiveEntryID   pgtype.Int8        `json:"archive_entry_id"`
	MailboxID        pgtype.Int8        `json:"mailbox_id"`
	CompletedAt      pgtype.Timestamptz `json:"completed_at"`
	PreviewActorID   pgtype.Int8        `json:"preview_actor_id"`
	PreviewToken     pgtype.Text        `json:"preview_token"`
	PreviewDigest    pgtype.Text        `json:"preview_digest"`
	PreviewSnapshot  []byte             `json:"preview_snapshot"`
	PreviewExpiresAt pgtype.Timestamptz `json:"preview_expires_at"`
	ConfirmedAt      pgtype.Timestamptz `json:"confirmed_at"`
	SelectedCopyKeys []byte             `json:"selected_copy_keys"`
	AttemptEpoch     int64              `json:"attempt_epoch"`
	LocalResult      []byte             `json:"local_result"`
	ResultHistory    []byte             `json:"result_history"`
}

type ScheduleMarketingCampaignParams struct {
	TenantID    int64              `json:"tenant_id"`
	CampaignID  int64              `json:"campaign_id"`
	ScheduledAt pgtype.Timestamptz `json:"scheduled_at"`
}

type CreateRecallRow struct {
	ID               int64              `json:"id"`
	TenantID         int64              `json:"tenant_id"`
	MessageID        string             `json:"message_id"`
	RequestedBy      int64              `json:"requested_by"`
	Status           string             `json:"status"`
	Reason           pgtype.Text        `json:"reason"`
	CreatedAt        pgtype.Timestamptz `json:"created_at"`
	ArchiveEntryID   pgtype.Int8        `json:"archive_entry_id"`
	MailboxID        pgtype.Int8        `json:"mailbox_id"`
	CompletedAt      pgtype.Timestamptz `json:"completed_at"`
	PreviewActorID   pgtype.Int8        `json:"preview_actor_id"`
	PreviewToken     pgtype.Text        `json:"preview_token"`
	PreviewDigest    pgtype.Text        `json:"preview_digest"`
	PreviewSnapshot  []byte             `json:"preview_snapshot"`
	PreviewExpiresAt pgtype.Timestamptz `json:"preview_expires_at"`
	ConfirmedAt      pgtype.Timestamptz `json:"confirmed_at"`
	SelectedCopyKeys []byte             `json:"selected_copy_keys"`
	AttemptEpoch     int64              `json:"attempt_epoch"`
	LocalResult      []byte             `json:"local_result"`
	ResultHistory    []byte             `json:"result_history"`
}

type AbandonDNSPlanParams struct {
	TenantID int64       `json:"tenant_id"`
	ID       pgtype.UUID `json:"id"`
	Reason   string      `json:"reason"`
}

type CancelDNSPlanParams struct {
	TenantID int64       `json:"tenant_id"`
	ID       pgtype.UUID `json:"id"`
	Reason   string      `json:"reason"`
}

type ConfirmDNSPlanParams struct {
	TenantID     int64       `json:"tenant_id"`
	ID           pgtype.UUID `json:"id"`
	SourceDigest string      `json:"source_digest"`
}

type CreateDNSPlanParams struct {
	ID                pgtype.UUID `json:"id"`
	TenantID          int64       `json:"tenant_id"`
	DomainID          int64       `json:"domain_id"`
	CredentialID      pgtype.Int8 `json:"credential_id"`
	CredentialVersion pgtype.Int8 `json:"credential_version"`
	Kind              string      `json:"kind"`
	DomainDigest      string      `json:"domain_digest"`
	SourceDigest      string      `json:"source_digest"`
	BeforeRecords     []byte      `json:"before_records"`
	DesiredRecords    []byte      `json:"desired_records"`
	Changes           []byte      `json:"changes"`
}

type RequestDNSRollbackParams struct {
	TenantID int64       `json:"tenant_id"`
	ID       pgtype.UUID `json:"id"`
	Reason   string      `json:"reason"`
}

type RequestOnboardingRetryParams struct {
	TenantID int64  `json:"tenant_id"`
	DomainID int64  `json:"domain_id"`
	Reason   string `json:"reason"`
}

type ReviewDNSPlanParams struct {
	TenantID int64       `json:"tenant_id"`
	ID       pgtype.UUID `json:"id"`
	Reason   string      `json:"reason"`
}

type RevokeDNSCredentialParams struct {
	TenantID int64 `json:"tenant_id"`
	DomainID int64 `json:"domain_id"`
}

type SaveDNSCredentialParams struct {
	TenantID        int64  `json:"tenant_id"`
	DomainID        int64  `json:"domain_id"`
	Provider        string `json:"provider"`
	ZoneName        string `json:"zone_name"`
	ZoneID          string `json:"zone_id"`
	EncryptedSecret string `json:"encrypted_secret"`
}
