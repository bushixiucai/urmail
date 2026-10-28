package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/api/middleware"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/brandhost"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// BrandHostLifecycleHandler completes explicit platform-owned DNS verification.
type BrandHostLifecycleHandler struct {
	Pool      *db.Pool
	Agent     DNSAuthorityAgent
	Reconcile ReconcileTrigger
	LookupTXT func(context.Context, string) ([]string, error)
}

// Action never accepts a presented ownership token; it observes DNS itself.
func (h *BrandHostLifecycleHandler) Action(c *gin.Context) {
	host, ok := normalizeBrandHost(c.Param("hostname"))
	var input struct {
		Action  string `json:"action"`
		Confirm bool   `json:"confirm"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	decoder.DisallowUnknownFields()
	if !ok || strings.Contains(c.Param("hostname"), ":") || decoder.Decode(&input) != nil || !input.Confirm || h.Pool == nil || decoder.Decode(&struct{}{}) != io.EOF {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	q := store.New(h.Pool)
	row, err := q.PlatformGetBrandHostLifecycle(ctx, host)
	if errors.Is(err, pgx.ErrNoRows) {
		authError(c, 404, "NOT_FOUND")
		return
	}
	if err != nil {
		authError(c, 503, "DB_ERROR")
		return
	}
	switch input.Action {
	case "challenge":
		if row.Status != "pending-verify" && row.Status != "failed" {
			authError(c, 409, "BRAND_HOST_STATE_CONFLICT")
			return
		}
		challenge, e := brandhost.NewChallenge(host, time.Now())
		if e != nil {
			authError(c, 400, "INVALID_PARAMS")
			return
		}
		tx, e := h.Pool.PlatformTx(ctx)
		if e != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		tq := store.New(tx)
		locked, e := tq.PlatformLockBrandHost(ctx, host)
		if e != nil || locked.BrandID != row.BrandID || !locked.UpdatedAt.Time.Equal(row.UpdatedAt.Time) {
			authError(c, 409, "BRAND_HOST_STATE_CONFLICT")
			return
		}
		if e = tq.PlatformInsertBrandHostChallenge(ctx, store.PlatformInsertBrandHostChallengeParams{Hostname: host, TokenDigest: challenge.Digest, Method: challenge.Method, ExpiresAt: pgtype.Timestamptz{Time: challenge.Expires, Valid: true}}); e != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		if tx.Commit(ctx) != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(200, gin.H{"name": challenge.TXTName(), "value": challenge.TXTValue(), "expires_at": challenge.Expires})
	case "verify":
		if row.Status != "pending-verify" && row.Status != "failed" {
			authError(c, 409, "BRAND_HOST_STATE_CONFLICT")
			return
		}
		proof, e := q.PlatformGetBrandHostChallenge(ctx, host)
		if e != nil {
			authError(c, 409, "BRAND_HOST_PROOF_REQUIRED")
			return
		}
		lookup := h.LookupTXT
		if lookup == nil {
			lookup = net.DefaultResolver.LookupTXT
		}
		values, e := lookup(ctx, "_urmail-brand-ownership."+host+".")
		if e != nil || len(values) > 32 {
			authError(c, 409, "BRAND_HOST_DNS_NOT_VERIFIED")
			return
		}
		var consumed *time.Time
		if proof.ConsumedAt.Valid {
			consumed = &proof.ConsumedAt.Time
		}
		matched := false
		for _, v := range values {
			if len(v) <= 512 && strings.HasPrefix(v, "urmail-brand-verify=") && brandhost.CheckChallenge(brandhost.ChallengeRecord{TokenDigest: proof.TokenDigest, ExpiresAt: proof.ExpiresAt.Time, ConsumedAt: consumed}, strings.TrimPrefix(v, "urmail-brand-verify="), time.Now()) == nil {
				matched = true
				break
			}
		}
		if !matched {
			authError(c, 409, "BRAND_HOST_DNS_NOT_VERIFIED")
			return
		}
		tx, e := h.Pool.PlatformTx(ctx)
		if e != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		tq := store.New(tx)
		locked, e := tq.PlatformLockBrandHost(ctx, host)
		if e != nil || locked.BrandID != row.BrandID || !locked.UpdatedAt.Time.Equal(row.UpdatedAt.Time) {
			authError(c, 409, "BRAND_HOST_STATE_CONFLICT")
			return
		}
		n, e := tq.PlatformVerifyBrandHostProof(ctx, store.PlatformVerifyBrandHostProofParams{Hostname: host, TokenDigest: proof.TokenDigest, BrandID: row.BrandID, ExpectedUpdatedAt: row.UpdatedAt})
		if e != nil || n != 1 {
			authError(c, 409, "BRAND_HOST_STATE_CONFLICT")
			return
		}
		if tx.Commit(ctx) != nil {
			authError(c, 503, "DB_ERROR")
			return
		}
		if h.Reconcile != nil {
			h.Reconcile.Trigger()
		}
		c.JSON(200, gin.H{"status": "verified", "configuration_pending": true})
	case "activate":
		if row.Status != "verified" && row.Status != "cert-issued" && row.Status != "active" {
			authError(c, 409, "BRAND_HOST_PROOF_REQUIRED")
			return
		}
		if h.Agent == nil {
			authError(c, 503, "BRAND_HOST_CERTIFICATE_UNAVAILABLE")
			return
		}
		response, e := h.Agent.Do(ctx, agentd.OpBrandHostActivate, agentd.BrandHostParams{Hostname: host, BrandID: row.BrandID, VerifiedAt: row.VerifiedAt.Time, ActorID: func() int64 { id, _ := middleware.GetUserID(c); return id }()})
		if e != nil || response == nil {
			authError(c, 503, "BRAND_HOST_CERTIFICATE_UNAVAILABLE")
			return
		}
		c.JSON(200, gin.H{"status": "active"})
	default:
		authError(c, 400, "INVALID_PARAMS")
	}
}

// Status returns persisted lifecycle and the current public certificate verdict.
func (h *BrandHostLifecycleHandler) Status(c *gin.Context) {
	host, ok := normalizeBrandHost(c.Param("hostname"))
	if !ok || h.Pool == nil {
		authError(c, 400, "INVALID_PARAMS")
		return
	}
	row, err := store.New(h.Pool).PlatformGetBrandHostLifecycle(c, host)
	if err != nil {
		authError(c, 404, "NOT_FOUND")
		return
	}
	var certificate any
	if h.Agent != nil && (row.Status == "verified" || row.Status == "cert-issued" || row.Status == "active") {
		response, e := h.Agent.Do(c, agentd.OpCertificateStatus, agentd.CertificateParams{Domain: host})
		if e == nil && response != nil {
			var value json.RawMessage
			if json.Unmarshal([]byte(response.Stdout), &value) == nil {
				certificate = value
			}
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"host": row, "certificate": certificate})
}
