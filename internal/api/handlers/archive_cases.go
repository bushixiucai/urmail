package handlers

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// ArchiveCases owns the tenant transaction boundary for case products. A role
// alone never grants content access; case membership and independent live
// compliance grants remain authoritative in both SQL policies and handlers.
type archiveCases struct {
	App, Platform *db.Pool
	Root          string
}

// RegisterArchiveCaseRoutes attaches to the existing authenticated, CSRF and
// tenant-transaction group. It deliberately does not install another auth stack.
func RegisterArchiveCaseRoutes(group *gin.RouterGroup, app, platform *db.Pool, root string) {
	if app == nil || platform == nil {
		return
	}
	h := &archiveCases{App: app, Platform: platform, Root: root}
	group.GET("/archive/cases", h.list)
	group.POST("/archive/cases", h.create)
	group.GET("/archive/cases/eligible-members", h.eligible)
	group.GET("/archive/cases/:case_id", h.detail)
	group.POST("/archive/cases/:case_id/close", h.close)
	group.POST("/archive/cases/:case_id/members", h.addMember)
	group.DELETE("/archive/cases/:case_id/members/:user_id", h.removeMember)
	group.POST("/archive/cases/:case_id/sources", h.addSource)
	group.DELETE("/archive/cases/:case_id/sources/:entry_id", h.removeSource)
	group.POST("/archive/cases/:case_id/approvals", h.requestApproval)
	group.POST("/archive/cases/:case_id/approvals/:approval_id/decision", h.decide)
	group.POST("/archive/cases/:case_id/approvals/:approval_id/withdraw", h.withdraw)
	group.POST("/archive/cases/:case_id/approvals/:approval_id/evidence", h.prepareEvidence)
	group.GET("/archive/cases/:case_id/approvals/:approval_id/package", h.downloadPackage)
	group.HEAD("/archive/cases/:case_id/approvals/:approval_id/package", h.downloadPackage)
}

func (h *archiveCases) principal(c *gin.Context, scope string) (*store.Queries, int64, int64, bool) {
	tenant, tok := middleware.GetTenantID(c)
	actor, uok := middleware.GetUserID(c)
	if !tok || !uok || tenant <= 0 || actor <= 0 {
		authError(c, 401, "UNAUTHORIZED")
		return nil, 0, 0, false
	}
	q := requestStore(c, store.New(h.App.Pool))
	allowed, err := q.ArchiveCasesGrantEligible(c, store.ArchiveCasesGrantEligibleParams{T: tenant, U: actor, S: scope})
	if err != nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return nil, 0, 0, false
	}
	if !allowed {
		authError(c, 403, "ARCHIVE_CASE_GRANT_REQUIRED")
		return nil, 0, 0, false
	}
	return q, tenant, actor, true
}
func casePathID(c *gin.Context, key string) (int64, bool) {
	v, e := strconv.ParseInt(c.Param(key), 10, 64)
	if e != nil || v <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return 0, false
	}
	return v, true
}
func archiveCaseError(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "23514", "23505":
			authError(c, 409, "ARCHIVE_CASE_STALE")
			return
		case "23503":
			authError(c, 404, "NOT_FOUND")
			return
		case "42501":
			authError(c, 403, "FORBIDDEN")
			return
		}
	}
	authError(c, 503, "ARCHIVE_UNAVAILABLE")
}

func caseAudit(c *gin.Context, q *store.Queries, tenant, actor int64, action string, id int64) bool {
	if err := q.RecordTenantAudit(c, store.RecordTenantAuditParams{TenantID: tenant, UserID: actor, Action: action, Target: strconv.FormatInt(id, 10), Status: 200}); err != nil {
		authError(c, 500, "DB_ERROR")
		return false
	}
	return true
}
func (h *archiveCases) caseRow(c *gin.Context, q *store.Queries, tenant int64, lock bool) (store.ArchiveInvestigations, bool) {
	id, ok := casePathID(c, "case_id")
	if !ok {
		return store.ArchiveInvestigations{}, false
	}
	var row store.ArchiveInvestigations
	var err error
	if lock {
		row, err = q.ArchiveCasesLock(c, store.ArchiveCasesLockParams{TenantID: tenant, ID: id})
	} else {
		row, err = q.ArchiveCasesGet(c, store.ArchiveCasesGetParams{TenantID: tenant, ID: id})
	}
	if err != nil {
		archiveCaseError(c, err)
		return row, false
	}
	return row, true
}
func caseWritable(c *gin.Context, row store.ArchiveInvestigations, version int64) bool {
	if row.Status != "open" || !row.ExpiresAt.Time.After(time.Now()) || version != row.Version {
		authError(c, 409, "ARCHIVE_CASE_STALE")
		return false
	}
	return true
}
func (h *archiveCases) list(c *gin.Context) {
	q, t, _, ok := h.principal(c, "full_content")
	if !ok {
		return
	}
	rows, err := q.ArchiveCasesList(c, t)
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if rows == nil {
		rows = []store.ArchiveInvestigations{}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"cases": rows})
}
func (h *archiveCases) create(c *gin.Context) {
	q, t, u, ok := h.principal(c, "full_content")
	if !ok {
		return
	}
	var input struct {
		Title      string    `json:"title"`
		Purpose    string    `json:"purpose"`
		ExpiresAt  time.Time `json:"expires_at"`
		RequestKey string    `json:"request_key"`
	}
	if c.ShouldBindJSON(&input) != nil || len(strings.TrimSpace(input.Title)) < 1 || len(input.Title) > 200 || len(strings.TrimSpace(input.Purpose)) < 1 || len(input.Purpose) > 2000 || !input.ExpiresAt.After(time.Now()) || input.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	parsed, err := uuid.Parse(input.RequestKey)
	if err != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	id, err := q.ArchiveCasesCreate(c, store.ArchiveCasesCreateParams{TenantID: t, ActorID: u, Title: strings.TrimSpace(input.Title), Purpose: strings.TrimSpace(input.Purpose), ExpiresAt: pgtype.Timestamptz{Time: input.ExpiresAt.UTC(), Valid: true}, RequestKey: parsed.String()})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if !caseAudit(c, q, t, u, "CREATE archive.investigation", id) {
		return
	}
	c.JSON(201, gin.H{"id": id})
}
func (h *archiveCases) eligible(c *gin.Context) {
	q, t, _, ok := h.principal(c, "full_content")
	if !ok {
		return
	}
	rows, err := q.ArchiveCasesEligibleMembers(c, t)
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if rows == nil {
		rows = []store.ArchiveCasesEligibleMembersRow{}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"members": rows})
}
func (h *archiveCases) detail(c *gin.Context) {
	q, t, u, ok := h.principal(c, "full_content")
	if !ok {
		return
	}
	row, ok := h.caseRow(c, q, t, false)
	if !ok {
		return
	}
	members, err := q.ArchiveCasesMembers(c, store.ArchiveCasesMembersParams{TenantID: t, InvestigationID: row.ID})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	sources, err := q.ArchiveCasesSources(c, store.ArchiveCasesSourcesParams{TenantID: t, InvestigationID: row.ID})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	digest, err := q.ArchiveCasesSnapshotDigest(c, store.ArchiveCasesSnapshotDigestParams{T: t, C: row.ID, U: u})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	requests, err := q.ArchiveCasesApprovals(c, store.ArchiveCasesApprovalsParams{TenantID: t, InvestigationID: row.ID})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	approvals := make([]gin.H, 0, len(requests))
	for _, r := range requests {
		evidence, readErr := q.ArchiveCasesGetEvidence(c, store.ArchiveCasesGetEvidenceParams{TenantID: t, ApprovalID: r.ID})
		if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
			archiveCaseError(c, readErr)
			return
		}
		approvals = append(approvals, gin.H{"id": r.ID, "requester_user_id": r.RequesterUserID, "approver_user_id": r.ApproverUserID, "state": r.State, "expires_at": r.ExpiresAt, "case_version": r.CaseVersion, "source_digest": r.SourceDigest, "decision_reason": r.DecisionReason, "stale": r.CaseVersion != row.Version || r.SourceDigest != digest || !r.ExpiresAt.Time.After(time.Now()), "manifest_sha256": evidence.ManifestSha256})
	}
	safeSources := make([]gin.H, 0, len(sources))
	for _, s := range sources {
		safeSources = append(safeSources, gin.H{"id": s.ID, "mailbox_id": s.MailboxID, "subject": s.Subject, "size_bytes": s.SizeBytes, "raw_sha256": s.RawSha256, "legal_hold": s.LegalHold, "deleted": s.DeletedAt.Valid})
	}
	if members == nil {
		members = []store.ArchiveCasesMembersRow{}
	}
	canExport, err := q.ArchiveCasesGrantEligible(c, store.ArchiveCasesGrantEligibleParams{T: t, U: u, S: "export"})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if !caseAudit(c, q, t, u, "READ archive.investigation", row.ID) {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"case": row, "members": members, "sources": safeSources, "approvals": approvals, "source_digest": digest, "actor_id": u, "can_manage": row.CreatedBy == u && row.Status == "open", "can_export": canExport})
}
func (h *archiveCases) addMember(c *gin.Context)    { h.member(c, false) }
func (h *archiveCases) removeMember(c *gin.Context) { h.member(c, true) }
func (h *archiveCases) member(c *gin.Context, remove bool) {
	q, t, u, ok := h.principal(c, "full_content")
	if !ok {
		return
	}
	var input struct {
		UserID  int64 `json:"user_id"`
		Version int64 `json:"version"`
	}
	if c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if remove {
		var valid bool
		input.UserID, valid = casePathID(c, "user_id")
		if !valid {
			return
		}
	}
	row, ok := h.caseRow(c, q, t, true)
	if !ok || !caseWritable(c, row, input.Version) {
		return
	}
	if row.CreatedBy != u || input.UserID <= 0 || (remove && input.UserID == row.CreatedBy) {
		authError(c, 403, "FORBIDDEN")
		return
	}
	var count int64
	var err error
	if remove {
		count, err = q.ArchiveCasesRemoveMember(c, store.ArchiveCasesRemoveMemberParams{TenantID: t, InvestigationID: row.ID, UserID: input.UserID, CreatedBy: u, Version: input.Version})
	} else {
		count, err = q.ArchiveCasesAddMember(c, store.ArchiveCasesAddMemberParams{TenantID: t, CaseID: row.ID, UserID: input.UserID, ActorID: u, Version: input.Version})
	}
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if count != 1 {
		authError(c, 409, "ARCHIVE_CASE_STALE")
		return
	}
	if !caseAudit(c, q, t, u, "UPDATE archive.investigation_member", row.ID) {
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (h *archiveCases) addSource(c *gin.Context)    { h.source(c, false) }
func (h *archiveCases) removeSource(c *gin.Context) { h.source(c, true) }
func (h *archiveCases) source(c *gin.Context, remove bool) {
	q, t, u, ok := h.principal(c, "full_content")
	if !ok {
		return
	}
	var input struct {
		EntryID int64 `json:"entry_id"`
		Version int64 `json:"version"`
	}
	if c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	if remove {
		var valid bool
		input.EntryID, valid = casePathID(c, "entry_id")
		if !valid {
			return
		}
	}
	row, ok := h.caseRow(c, q, t, true)
	if !ok || !caseWritable(c, row, input.Version) {
		return
	}
	if input.EntryID <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	entries, err := q.ArchiveCasesSources(c, store.ArchiveCasesSourcesParams{TenantID: t, InvestigationID: row.ID})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if !remove && len(entries) >= 100 {
		authError(c, 400, "ARCHIVE_PACKAGE_LIMIT")
		return
	}
	var count int64
	if remove {
		count, err = q.ArchiveCasesRemoveSource(c, store.ArchiveCasesRemoveSourceParams{TenantID: t, InvestigationID: row.ID, ArchiveEntryID: input.EntryID, Version: input.Version})
	} else {
		count, err = q.ArchiveCasesAddSource(c, store.ArchiveCasesAddSourceParams{TenantID: t, CaseID: row.ID, ActorID: u, EntryID: input.EntryID, Version: input.Version})
	}
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if count != 1 {
		authError(c, 409, "ARCHIVE_CASE_STALE")
		return
	}
	if !caseAudit(c, q, t, u, "UPDATE archive.investigation_sources", row.ID) {
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (h *archiveCases) close(c *gin.Context) {
	q, t, u, ok := h.principal(c, "full_content")
	if !ok {
		return
	}
	var input struct {
		Version int64 `json:"version"`
	}
	if c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	row, ok := h.caseRow(c, q, t, true)
	if !ok || !caseWritable(c, row, input.Version) {
		return
	}
	if row.CreatedBy != u {
		authError(c, 403, "FORBIDDEN")
		return
	}
	_, err := q.ArchiveCasesClose(c, store.ArchiveCasesCloseParams{TenantID: t, ID: row.ID, CreatedBy: u, Version: input.Version})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if !caseAudit(c, q, t, u, "CLOSE archive.investigation", row.ID) {
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (h *archiveCases) requestApproval(c *gin.Context) {
	q, t, u, ok := h.principal(c, "export")
	if !ok {
		return
	}
	var input struct {
		Version int64  `json:"version"`
		Digest  string `json:"digest"`
	}
	if c.ShouldBindJSON(&input) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	row, ok := h.caseRow(c, q, t, true)
	if !ok || !caseWritable(c, row, input.Version) {
		return
	}
	sources, err := q.ArchiveCasesSources(c, store.ArchiveCasesSourcesParams{TenantID: t, InvestigationID: row.ID})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if len(sources) == 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	digest, err := q.ArchiveCasesSnapshotDigest(c, store.ArchiveCasesSnapshotDigestParams{T: t, C: row.ID, U: u})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if digest != input.Digest {
		authError(c, 409, "ARCHIVE_CASE_STALE")
		return
	}
	id, err := q.ArchiveCasesRequestApproval(c, store.ArchiveCasesRequestApprovalParams{TenantID: t, ActorID: u, Digest: digest, CaseID: row.ID, Version: row.Version})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if !caseAudit(c, q, t, u, "REQUEST archive.export_approval", id) {
		return
	}
	c.JSON(201, gin.H{"id": id})
}
func (h *archiveCases) decide(c *gin.Context) {
	q, t, u, ok := h.principal(c, "export")
	if !ok {
		return
	}
	row, ok := h.caseRow(c, q, t, true)
	if !ok {
		return
	}
	id, ok := casePathID(c, "approval_id")
	if !ok {
		return
	}
	var input struct {
		Decision string `json:"decision"`
		Digest   string `json:"digest"`
		Reason   string `json:"reason"`
	}
	if c.ShouldBindJSON(&input) != nil || (input.Decision != "approved" && input.Decision != "rejected") || len(strings.TrimSpace(input.Reason)) == 0 || len(input.Reason) > 2000 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	request, err := q.ArchiveCasesApproval(c, store.ArchiveCasesApprovalParams{TenantID: t, InvestigationID: row.ID, ID: id})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if request.RequesterUserID == u {
		authError(c, 403, "ARCHIVE_INDEPENDENT_APPROVAL_REQUIRED")
		return
	}
	_, err = q.ArchiveCasesDecideApproval(c, store.ArchiveCasesDecideApprovalParams{TenantID: t, CaseID: row.ID, ApprovalID: id, ActorID: u, Decision: input.Decision, Reason: strings.TrimSpace(input.Reason), Digest: input.Digest})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if !caseAudit(c, q, t, u, "DECIDE archive.export_approval", id) {
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
func (h *archiveCases) withdraw(c *gin.Context) {
	q, t, u, ok := h.principal(c, "export")
	if !ok {
		return
	}
	row, ok := h.caseRow(c, q, t, true)
	if !ok {
		return
	}
	id, ok := casePathID(c, "approval_id")
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&input) != nil || len(strings.TrimSpace(input.Reason)) == 0 || len(input.Reason) > 2000 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	_, err := q.ArchiveCasesWithdrawApproval(c, store.ArchiveCasesWithdrawApprovalParams{TenantID: t, InvestigationID: row.ID, ID: id, RequesterUserID: u, DecisionReason: strings.TrimSpace(input.Reason)})
	if err != nil {
		archiveCaseError(c, err)
		return
	}
	if !caseAudit(c, q, t, u, "WITHDRAW archive.export_approval", id) {
		return
	}
	c.JSON(200, gin.H{"ok": true})
}
