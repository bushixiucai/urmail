package handlers

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestVerifyExpiredChallengeReturnsGoneBeforeDNSLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &shapeFakeDomainStore{byID: store.GetDomainByIDRow{
		ID: 1, TenantID: shapeTenantID, Fqdn: "example.com", Status: "pending_dns",
		VerificationIssuedAt:  pgtype.Timestamptz{Time: time.Now().Add(-8 * 24 * time.Hour), Valid: true},
		VerificationExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true},
	}}
	assertDomainChallengeExpired(t, fake)
}

func TestVerifyMissingChallengeTimestampFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &shapeFakeDomainStore{byID: store.GetDomainByIDRow{
		ID: 1, TenantID: shapeTenantID, Fqdn: "example.com", Status: "pending_dns",
	}}
	assertDomainChallengeExpired(t, fake)
}

func TestVerifyFutureChallengeTimestampFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &shapeFakeDomainStore{byID: store.GetDomainByIDRow{
		ID: 1, TenantID: shapeTenantID, Fqdn: "example.com", Status: "pending_dns",
		VerificationIssuedAt:  pgtype.Timestamptz{Time: time.Now().Add(2 * time.Hour), Valid: true},
		VerificationExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(7 * 24 * time.Hour), Valid: true},
	}}
	assertDomainChallengeExpired(t, fake)
}

func assertDomainChallengeExpired(t *testing.T, fake *shapeFakeDomainStore) {
	t.Helper()
	handler := NewDomainHandler(fake, nil)
	router := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.POST("/domains/:id/verify", handler.Verify) })
	code, raw, body := shapeRequest(t, router, http.MethodPost, "/api/v1/domains/1/verify", "")
	if code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", code, raw)
	}
	if got := shapeObject(t, body, "error")["code"]; got != "DOMAIN_CHALLENGE_EXPIRED" {
		t.Fatalf("error code = %v, want DOMAIN_CHALLENGE_EXPIRED", got)
	}
}
