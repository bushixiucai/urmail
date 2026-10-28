package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	recall "github.com/bushixiucai/urmail/internal/domain/recall"
	"github.com/bushixiucai/urmail/internal/store"
)

// RecallReviewer exposes only database-bound inventory and confirmed fixed-copy cleanup.
type RecallReviewer interface {
	PreviewRecall(context.Context, int64, int64, int64) (recall.Snapshot, error)
	ExpungeReviewedRecall(context.Context, int64, int64, int64, string) error
}

// RegisterRecallProductRoutes reuses the authenticated tenant/CSRF transaction group.
func RegisterRecallProductRoutes(group *gin.RouterGroup, h *RecallHandler) {
	group.POST("/compliance/recalls/preview", middleware.RequireRole("owner", "admin"), h.Preview)
}
func recallPrincipal(c *gin.Context) (int64, int64, bool) {
	tenant, tok := middleware.GetTenantID(c)
	actor, aok := middleware.GetUserID(c)
	role := middleware.GetRole(c)
	if !tok || !aok || tenant <= 0 || actor <= 0 {
		authError(c, 401, "UNAUTHORIZED")
		return 0, 0, false
	}
	if role != "owner" && role != "admin" {
		authError(c, 403, "FORBIDDEN")
		return 0, 0, false
	}
	return tenant, actor, true
}
func recallDecode(c *gin.Context, value any) bool {
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 65537))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		authError(c, 400, "INVALID_PARAMS")
		return false
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		authError(c, 400, "INVALID_PARAMS")
		return false
	}
	return true
}
func recallProductError(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 409, "RECALL_REVIEW_STALE")
		return
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "42501":
			authError(c, 403, "FORBIDDEN")
			return
		case "23514", "23505":
			authError(c, 409, "RECALL_REVIEW_STALE")
			return
		}
	}
	authError(c, 503, "ARCHIVE_UNAVAILABLE")
}
func recallAudit(c *gin.Context, q *store.Queries, tenant, actor, id int64, action string, _ any) bool {
	row, err := q.GetRecallByID(c.Request.Context(), store.GetRecallByIDParams{TenantID: tenant, ID: id})
	if err != nil {
		recallProductError(c, err)
		return false
	}
	operation := "PREVIEW compliance.recall"
	if action == "compliance.recall.confirm" {
		operation = "CONFIRM compliance.recall"
	}
	target := strconv.FormatInt(id, 10) + "/" + strconv.FormatInt(row.AttemptEpoch, 10) + "/" + row.PreviewDigest.String
	if err = q.RecordTenantAudit(c.Request.Context(), store.RecordTenantAuditParams{TenantID: tenant, UserID: actor, Action: operation, Target: target, Status: 200}); err != nil {
		recallProductError(c, err)
		return false
	}
	return true
}

func recallPreviewProjection(row store.RecallRequests) gin.H {
	var snapshot recall.Snapshot
	_ = json.Unmarshal(row.PreviewSnapshot, &snapshot)
	copies := make([]gin.H, 0, len(snapshot.Copies))
	for _, copy := range snapshot.Copies {
		copies = append(copies, gin.H{"key": copy.Key(), "folder": copy.Folder, "uid": copy.UID, "uid_validity": copy.UIDValidity})
	}
	return gin.H{"recall": recallResponse(row), "copies": copies, "address": snapshot.Address, "reason": ""}
}

// Preview returns explicit local copy choices and unsupported/held explanations.
// Inventory RPCs run after the request transaction commits: the agent acquires
// the tenant fence itself, so holding that fence across RPC would self-deadlock.
func (h *RecallHandler) Preview(c *gin.Context) {
	tenant, actor, ok := recallPrincipal(c)
	if !ok {
		return
	}
	reviewer, ok := h.Expunger.(RecallReviewer)
	if !ok || h.AppPool == nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	var input struct {
		ArchiveEntryID int64 `json:"archive_entry_id"`
		IncludeRelated *bool `json:"include_related"`
	}
	if !recallDecode(c, &input) {
		return
	}
	if input.ArchiveEntryID <= 0 {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	q := requestStore(c, h.Store)
	if q == nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	sources, err := q.RecallMessageCopies(c.Request.Context(), store.RecallMessageCopiesParams{TenantID: tenant, ID: input.ArchiveEntryID})
	if err != nil {
		recallProductError(c, err)
		return
	}
	if len(sources) == 0 {
		authError(c, 404, "NOT_FOUND")
		return
	}
	if len(sources) > recall.MaxCopies {
		authError(c, 422, "RECALL_SCOPE_TOO_LARGE")
		return
	}
	if input.IncludeRelated != nil && !*input.IncludeRelated {
		filtered := sources[:0]
		for _, source := range sources {
			if source.ID == input.ArchiveEntryID {
				filtered = append(filtered, source)
			}
		}
		sources = filtered
	}
	if len(sources) > 20 {
		authError(c, 422, "RECALL_SCOPE_TOO_LARGE")
		return
	}
	middleware.AfterCommit(c, func() {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
		defer cancel()
		snapshots := make(map[int64]recall.Snapshot, len(sources))
		targets := make([]gin.H, 0, len(sources))
		total := 0
		for _, source := range sources {
			projection := gin.H{"entry_id": source.ID, "address": source.Address, "reason": "", "copies": []gin.H{}, "recall": nil}
			switch {
			case !source.MailboxID.Valid:
				projection["reason"] = "remote_delivery_not_recallable"
			case source.DeletedAt.Valid:
				projection["reason"] = "archive_source_deleted"
			default:
				snapshot, readErr := reviewer.PreviewRecall(ctx, tenant, source.ID, actor)
				if readErr != nil {
					if ctx.Err() != nil {
						authError(c, 503, "ARCHIVE_UNAVAILABLE")
						return
					}
					projection["reason"] = "local_preview_unavailable"
				} else {
					if snapshot.TenantID != tenant || snapshot.ActorID != actor || snapshot.EntryID != source.ID || snapshot.MailboxID != source.MailboxID.Int64 {
						authError(c, 503, "ARCHIVE_UNAVAILABLE")
						return
					}
					if reason := snapshot.Refusal(); reason != "" {
						projection["reason"] = reason
					} else {
						total += len(snapshot.Copies)
						if total > recall.MaxCopies {
							authError(c, 422, "RECALL_SCOPE_TOO_LARGE")
							return
						}
						snapshots[source.ID] = snapshot
					}
				}
			}
			targets = append(targets, projection)
		}
		tx, q, err := h.recallTransaction(ctx, tenant, actor)
		if err != nil {
			recallProductError(c, err)
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		for index, source := range sources {
			snapshot, exists := snapshots[source.ID]
			if !exists {
				continue
			}
			encoded, encodeErr := snapshot.Canonical()
			digest, digestErr := snapshot.Digest()
			if encodeErr != nil || digestErr != nil {
				authError(c, 503, "ARCHIVE_UNAVAILABLE")
				return
			}
			stored, saveErr := q.StoreRecallReviewPreview(ctx, store.StoreRecallReviewPreviewParams{TenantID: tenant, ActorID: actor, EntryID: source.ID, Token: uuid.NewString(), Digest: digest, Snapshot: encoded})
			if saveErr != nil {
				recallProductError(c, saveErr)
				return
			}
			row := store.RecallRequests(stored)
			projection := recallPreviewProjection(row)
			projection["entry_id"] = source.ID
			if row.Status == "pending" {
				projection["reason"] = "review_in_progress"
				if !row.PreviewActorID.Valid || row.PreviewActorID.Int64 != actor {
					projection["reason"] = "review_in_progress_by_other_actor"
				}
			}
			if !recallAudit(c, q, tenant, actor, row.ID, "compliance.recall.preview", gin.H{"epoch": row.AttemptEpoch, "digest": row.PreviewDigest, "scope": "local_mailbox", "copy_count": len(snapshot.Copies)}) {
				return
			}
			targets[index] = projection
		}
		if err = tx.Commit(ctx); err != nil {
			recallProductError(c, err)
			return
		}
		c.JSON(200, gin.H{"targets": targets, "scope": "local_mailbox", "remote_recall_guaranteed": false})
	})
}

func (h *RecallHandler) recallTransaction(ctx context.Context, tenant, actor int64) (pgx.Tx, *store.Queries, error) {
	tx, err := h.AppPool.UserTx(ctx, tenant, actor)
	if err != nil {
		return nil, nil, err
	}
	q := store.New(tx)
	if err = q.PlatformLockRecallTenant(ctx, tenant); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	role, err := q.GetUserRoleForTenant(ctx, store.GetUserRoleForTenantParams{TenantID: tenant, UserID: actor})
	if err != nil || role != "owner" && role != "admin" {
		_ = tx.Rollback(ctx)
		return nil, nil, &pgconn.PgError{Code: "42501", Message: "recall authority revoked"}
	}
	return tx, q, nil
}

type recallConfirmation struct {
	ID       int64    `json:"id"`
	Token    string   `json:"preview_token"`
	Digest   string   `json:"digest"`
	CopyKeys []string `json:"copy_keys"`
}

func (h *RecallHandler) confirmReviewed(c *gin.Context) {
	if h.Platform == nil || h.AppPool == nil {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	tenant, actor, ok := recallPrincipal(c)
	if !ok {
		return
	}
	reviewer, ok := h.Expunger.(RecallReviewer)
	if !ok {
		authError(c, 503, "ARCHIVE_UNAVAILABLE")
		return
	}
	var input struct {
		Confirmed bool                 `json:"confirmed"`
		Targets   []recallConfirmation `json:"targets"`
	}
	if !recallDecode(c, &input) {
		return
	}
	if !input.Confirmed || len(input.Targets) == 0 || len(input.Targets) > 20 {
		authError(c, 400, "RECALL_PREVIEW_REQUIRED")
		return
	}
	seen := map[int64]bool{}
	total := 0
	for _, target := range input.Targets {
		if target.ID <= 0 || seen[target.ID] || len(target.CopyKeys) == 0 || len(target.CopyKeys) > recall.MaxCopies {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		seen[target.ID] = true
		total += len(target.CopyKeys)
		if total > recall.MaxCopies {
			authError(c, 422, "RECALL_SCOPE_TOO_LARGE")
			return
		}
		if _, err := uuid.Parse(target.Token); err != nil || len(target.Digest) != 64 {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
	}
	middleware.AfterCommit(c, func() {
		ctx := c.Request.Context()
		// A short principal-scoped read is closed before calling the independently
		// fenced agent. The final write rechecks authority and the exact proof.
		readTx, err := h.AppPool.UserTx(ctx, tenant, actor)
		if err != nil {
			recallProductError(c, err)
			return
		}
		expected := make([]store.RecallRequests, 0, len(input.Targets))
		readQ := store.New(readTx)
		for _, target := range input.Targets {
			row, e := readQ.GetRecallByID(ctx, store.GetRecallByIDParams{TenantID: tenant, ID: target.ID})
			if e != nil {
				_ = readTx.Rollback(ctx)
				recallProductError(c, e)
				return
			}
			expected = append(expected, row)
		}
		if err = readTx.Commit(ctx); err != nil {
			recallProductError(c, err)
			return
		}
		selections := make([][]byte, 0, len(input.Targets))
		for index, target := range input.Targets {
			persisted := expected[index]
			if !persisted.PreviewActorID.Valid || persisted.PreviewActorID.Int64 != actor || persisted.PreviewToken.String != target.Token || persisted.PreviewDigest.String != target.Digest {
				authError(c, 409, "RECALL_REVIEW_STALE")
				return
			}
			var snapshot recall.Snapshot
			if json.Unmarshal(persisted.PreviewSnapshot, &snapshot) != nil {
				authError(c, 409, "RECALL_REVIEW_STALE")
				return
			}
			selected, e := snapshot.Select(target.CopyKeys)
			if e != nil {
				authError(c, 400, "INVALID_PARAMS")
				return
			}
			keys := make([]string, 0, len(selected))
			for _, copy := range selected {
				keys = append(keys, copy.Key())
			}
			sort.Strings(keys)
			encoded, _ := json.Marshal(keys)
			selections = append(selections, encoded)
			if persisted.Status == "preview" {
				if !persisted.PreviewExpiresAt.Valid || !persisted.PreviewExpiresAt.Time.After(time.Now()) {
					authError(c, 409, "RECALL_REVIEW_STALE")
					return
				}
				current, e := reviewer.PreviewRecall(ctx, tenant, snapshot.EntryID, actor)
				if e != nil {
					authError(c, 503, "ARCHIVE_UNAVAILABLE")
					return
				}
				digest, e := current.Digest()
				if e != nil || digest != target.Digest || current.Refusal() != "" {
					authError(c, 409, "RECALL_REVIEW_STALE")
					return
				}
			}
		}
		tx, q, err := h.recallTransaction(ctx, tenant, actor)
		if err != nil {
			recallProductError(c, err)
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		rows := make([]store.RecallRequests, 0, len(input.Targets))
		for index, target := range input.Targets {
			confirmed, e := q.ConfirmRecallReview(ctx, store.ConfirmRecallReviewParams{TenantID: tenant, ActorID: pgtype.Int8{Int64: actor, Valid: true}, RecallID: target.ID, Token: pgtype.Text{String: target.Token, Valid: true}, Digest: pgtype.Text{String: target.Digest, Valid: true}, Selected: selections[index]})
			if e != nil {
				recallProductError(c, e)
				return
			}
			row := store.RecallRequests(confirmed)
			if !recallAudit(c, q, tenant, actor, row.ID, "compliance.recall.confirm", gin.H{"epoch": row.AttemptEpoch, "digest": row.PreviewDigest, "copy_keys": target.CopyKeys, "scope": "local_mailbox"}) {
				return
			}
			rows = append(rows, row)
		}
		if err = tx.Commit(ctx); err != nil {
			recallProductError(c, err)
			return
		}
		results := make([]RecallResponse, 0, len(rows))
		pending := false
		for _, row := range rows {
			if row.Status == "pending" {
				_ = reviewer.ExpungeReviewedRecall(ctx, tenant, row.ID, row.AttemptEpoch, row.PreviewToken.String)
			}
			if current, e := h.Platform.GetRecallByID(ctx, store.GetRecallByIDParams{TenantID: tenant, ID: row.ID}); e == nil && current.AttemptEpoch == row.AttemptEpoch {
				row = current
			}
			if row.Status == "pending" {
				pending = true
			}
			results = append(results, recallResponse(row))
		}
		status := http.StatusOK
		if pending {
			status = http.StatusAccepted
		}
		c.JSON(status, gin.H{"recalls": results, "scope": "local_mailbox", "remote_recall_guaranteed": false})
	})
}
