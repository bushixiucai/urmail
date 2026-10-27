package agentd

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/config"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

// OPS-101: the certificate pipeline must refuse brand hosts whose ownership
// has not been verified, and accept the eligibility predicate once the
// lifecycle reaches verified. The check fails closed on unknown hosts.
func TestCertificateIssueRejectsUnverifiedBrandHost(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	m := &ApplyManager{cfg: &config.Config{MyHostname: "box.example.com", PrimaryDomain: "example.com"}}
	m.pool = platform
	ctx := context.Background()
	queries := store.New(platform)

	var brand int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('certgate','CertGate') RETURNING id").Scan(&brand); err != nil {
		t.Fatal(err)
	}
	const host = "brandgate.example.test"
	if _, err := queries.PlatformBindBrandHost(ctx, store.PlatformBindBrandHostParams{Hostname: host, ID: brand}); err != nil {
		t.Fatal(err)
	}

	// pending-verify: rejected at the operation boundary.
	_, _, err := m.IssueCertificate([]byte(`{"domain":"brandgate.example.test"}`))
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("pending-verify issue: got %v, want ErrRejected", err)
	}
	eligible, err := queries.PlatformBrandHostCertEligible(ctx, host)
	if err != nil || eligible {
		t.Fatalf("pending-verify eligible=%v err=%v", eligible, err)
	}

	// Verify ownership, then the eligibility predicate flips.
	verified := store.PlatformSetBrandHostLifecycleParams{Hostname: host, Status: "verified"}
	if n, err := queries.PlatformSetBrandHostLifecycle(ctx, verified); err != nil || n != 1 {
		t.Fatalf("mark verified: rows=%d err=%v", n, err)
	}
	eligible, err = queries.PlatformBrandHostCertEligible(ctx, host)
	if err != nil || !eligible {
		t.Fatalf("verified host not eligible: %v %v", eligible, err)
	}
	if !m.brandHostCertEligible(host) {
		t.Fatalf("verified brand host rejected by eligibility predicate")
	}

	// Failed hosts are refused again.
	failed := store.PlatformSetBrandHostLifecycleParams{Hostname: host, Status: "failed", FailureReason: pgtext("certificate_issuance_failed")}
	if n, err := queries.PlatformSetBrandHostLifecycle(ctx, failed); err != nil || n != 1 {
		t.Fatalf("mark failed: rows=%d err=%v", n, err)
	}
	if m.brandHostCertEligible(host) {
		t.Fatalf("failed brand host accepted by eligibility predicate")
	}
	_, _, err = m.IssueCertificate([]byte(`{"domain":"brandgate.example.test"}`))
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("failed host issue: got %v, want ErrRejected", err)
	}

	// Unknown hostnames fail closed.
	if m.brandHostCertEligible("never-bound.example.test") {
		t.Fatalf("unknown host accepted")
	}
	// Malformed identities are rejected before any store round trip.
	_, _, err = m.IssueCertificate([]byte(`{"domain":"not a host"}`))
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("invalid identity: got %v, want ErrRejected", err)
	}
}

func pgtext(v string) pgtype.Text { return pgtype.Text{String: v, Valid: v != ""} }
