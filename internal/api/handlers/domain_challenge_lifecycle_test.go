package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

type challengeLifecycleStore struct {
	*shapeFakeDomainStore
	rotated   store.RotateDomainChallengeParams
	verified  store.VerifyDomainChallengeParams
	verifyErr error
}

func (f *challengeLifecycleStore) RotateDomainChallenge(_ context.Context, p store.RotateDomainChallengeParams) (store.RotateDomainChallengeRow, error) {
	f.rotated = p
	if f.byID.Status == "active" {
		return store.RotateDomainChallengeRow{}, pgx.ErrNoRows
	}
	return store.RotateDomainChallengeRow{ID: p.ID, Fqdn: f.byID.Fqdn, VerificationToken: p.VerificationToken, VerificationIssuedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, VerificationExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(7 * 24 * time.Hour), Valid: true}}, nil
}
func (f *challengeLifecycleStore) VerifyDomainChallenge(_ context.Context, p store.VerifyDomainChallengeParams) (store.VerifyDomainChallengeRow, error) {
	f.verified = p
	return store.VerifyDomainChallengeRow{ID: p.ID, TenantID: p.TenantID, Fqdn: f.byID.Fqdn, Status: "active"}, f.verifyErr
}

func TestOldDomainCanVerifyFreshChallenge(t *testing.T) {
	f := &challengeLifecycleStore{shapeFakeDomainStore: &shapeFakeDomainStore{byID: store.GetDomainByIDRow{ID: 4, TenantID: shapeTenantID, Fqdn: "old.example.test", Status: "pending_dns", VerificationToken: "fresh-token", CreatedAt: pgtype.Timestamptz{Time: time.Now().AddDate(-1, 0, 0), Valid: true}, VerificationIssuedAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}, VerificationExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}}}
	h := NewDomainHandler(f, nil)
	h.Verification = domainStoreProbeFake{f}
	h.LookupTXT = func(context.Context, string) ([]string, error) { return []string{"fresh-token"}, nil }
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.POST("/domains/:id/verify", h.Verify) })
	status, body, _ := shapeRequest(t, r, http.MethodPost, "/api/v1/domains/4/verify", "")
	if status != 200 || f.verified.VerificationToken != "fresh-token" {
		t.Fatalf("old domain fresh proof rejected: %d %s", status, body)
	}
	f.verifyErr = pgx.ErrNoRows
	status, body, _ = shapeRequest(t, r, http.MethodPost, "/api/v1/domains/4/verify", "")
	if status != 409 {
		t.Fatalf("revoked in-flight proof was accepted: %d %s", status, body)
	}
}

func TestReissueChallengeBindsTenantAndRefusesActiveDomain(t *testing.T) {
	f := &challengeLifecycleStore{shapeFakeDomainStore: &shapeFakeDomainStore{byID: store.GetDomainByIDRow{ID: 4, Fqdn: "old.example.test", Status: "pending_dns"}}}
	h := NewDomainHandler(f, nil)
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.POST("/domains/:id/challenge", h.ReissueChallenge) })
	status, raw, body := shapeRequest(t, r, http.MethodPost, "/api/v1/domains/4/challenge", "")
	if status != 200 || f.rotated.TenantID != shapeTenantID || f.rotated.ID != 4 || len(f.rotated.VerificationToken) != 64 {
		t.Fatalf("bad challenge: %d %s %#v", status, raw, f.rotated)
	}
	if body["verification_value"] != f.rotated.VerificationToken {
		t.Fatalf("not the canonical rotated value: %v", body)
	}
	first := f.rotated.VerificationToken
	status, _, _ = shapeRequest(t, r, http.MethodPost, "/api/v1/domains/4/challenge", "")
	if status != 200 || first == f.rotated.VerificationToken {
		t.Fatal("challenge was not independently rotated")
	}
	f.byID.Status = "active"
	status, _, _ = shapeRequest(t, r, http.MethodPost, "/api/v1/domains/4/challenge", "")
	if status != 409 {
		t.Fatal("active domain challenge was rotated")
	}
}

type rejectingOwnershipDNS struct{ called bool }

func (d *rejectingOwnershipDNS) EnsureTXT(context.Context, string, string, string) error {
	d.called = true
	return errors.New("must never be called")
}
func TestCreateNeverUsesPlatformDNSToProveTenantOwnership(t *testing.T) {
	f := &shapeFakeDomainStore{created: store.CreateDomainRow{ID: 8, TenantID: shapeTenantID, Fqdn: "unassigned.platform.test", Status: "pending_dns"}}
	dns := &rejectingOwnershipDNS{}
	h := NewDomainHandler(f, nil)
	h.DNS = dns
	h.DNSManagedZones = []string{"platform.test"}
	r := newShapeRouter(shapeTenantID, func(g *gin.RouterGroup) { g.POST("/domains", h.Create) })
	status, raw, body := shapeRequest(t, r, http.MethodPost, "/api/v1/domains", `{"fqdn":"unassigned.platform.test"}`)
	if status != 201 || dns.called || body["dns_auto"] != false {
		t.Fatalf("platform self-proved tenant ownership: status=%d DNS=%v body=%s", status, dns.called, raw)
	}
}
