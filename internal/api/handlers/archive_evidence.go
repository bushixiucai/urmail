package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/evidence"
	"github.com/bushixiucai/urmail/internal/store"
)

type evidenceVisible interface {
	PlatformEvidenceVisible(context.Context, store.PlatformEvidenceVisibleParams) (bool, error)
	PlatformReadEvidenceBody(context.Context, store.PlatformReadEvidenceBodyParams) ([]byte, error)
}

// ArchiveEvidence serves platform audit evidence bodies to authorized actors
// after re-verifying visibility (tenant, actor and digest binding).
type ArchiveEvidence struct {
	Q        evidenceVisible
	Manifest func(context.Context, int64, int64) (string, error)
}

// Download streams one evidence attachment after checking visibility; it
// never exposes evidence that the actor's tenant is not entitled to read.
func (h ArchiveEvidence) Download(c *gin.Context) {
	tenant, tenantOK := middleware.GetTenantID(c)
	actor, actorOK := middleware.GetUserID(c)
	requestID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	digest := c.Query("digest")
	if !tenantOK || !actorOK || err != nil || requestID <= 0 || len(digest) != 64 || h.Q == nil {
		authError(c, http.StatusBadRequest, "INVALID_PARAMS")
		return
	}
	visible, err := h.Q.PlatformEvidenceVisible(c.Request.Context(), store.PlatformEvidenceVisibleParams{PTenant: tenant, PRequest: requestID, PActor: actor, PDigest: digest})
	if err != nil || !visible {
		authError(c, http.StatusForbidden, "EVIDENCE_NOT_VISIBLE")
		return
	}
	body, err := h.Q.PlatformReadEvidenceBody(c.Request.Context(), store.PlatformReadEvidenceBodyParams{PTenant: tenant, PRequest: requestID, PActor: actor, PDigest: digest})
	if err != nil || len(body) == 0 {
		authError(c, http.StatusNotFound, "EVIDENCE_NOT_FOUND")
		return
	}
	bodyText, err := evidence.Read(c.Request.Context(), func(ctx context.Context, tenant, request, actor int64, digest string) (bool, error) {
		return h.Q.PlatformEvidenceVisible(ctx, store.PlatformEvidenceVisibleParams{PTenant: tenant, PRequest: request, PActor: actor, PDigest: digest})
	}, tenant, requestID, actor, digest, string(body))
	if err != nil {
		authError(c, http.StatusForbidden, "EVIDENCE_NOT_VISIBLE")
		return
	}
	c.Data(http.StatusOK, "application/json", []byte(bodyText))
}
