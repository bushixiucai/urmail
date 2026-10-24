package miabimport

import (
	"errors"
	"testing"
)

func TestActivateLegacyDelegatedMailKeepsNSDExternal(t *testing.T) {
	f := newCoreFixture(t)
	if _, err := ApplyCore(t.Context(), f.pool, f.source, f.options); err != nil {
		t.Fatal(err)
	}
	options, _ := dkFixture(t, f.source, 2048)
	reviewed, err := LoadReviewedDKIM(t.Context(), options, f.source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyReviewedDKIM(t.Context(), f.pool, reviewed, Options{
		TenantID: f.options.TenantID, ActorID: f.options.ActorID, TenantRef: f.options.TenantRef,
		Reason: "Preserve exact source DKIM private key before mail activation",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := ActivateLegacyDelegatedMail(t.Context(), f.pool, Options{
		TenantID: f.options.TenantID, ActorID: f.options.ActorID, TenantRef: f.options.TenantRef,
		Reason: "Activate delegated mail routing while preserving external NSD",
	}, f.source.PrivateProofSHA256(), 7)
	if err != nil || !result.Applied || result.CutoverReady || result.ManagedDNSEmitted || !result.ModeRemainsDelegated || result.Domains != 7 {
		t.Fatalf("unexpected activate: %+v %v", result, err)
	}
	var ready, delegated, managed int
	if err = f.pool.QueryRow(t.Context(), `
SELECT
 count(*) FILTER (WHERE status='active' AND provisioning_state='ready' AND verified_at IS NOT NULL),
 count(*) FILTER (WHERE mode='delegated' AND NOT dns_explicit),
 count(*) FILTER (WHERE mode='managed' OR dns_explicit)
FROM domains WHERE tenant_id=$1`, f.options.TenantID).Scan(&ready, &delegated, &managed); err != nil {
		t.Fatal(err)
	}
	if ready != 7 || delegated != 7 || managed != 0 {
		t.Fatalf("domain state drifted ready=%d delegated=%d managed=%d", ready, delegated, managed)
	}
	var mailboxes int
	if err = f.pool.QueryRow(t.Context(), `
SELECT count(*) FROM mailboxes m
JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id
WHERE m.tenant_id=$1 AND m.status='active' AND d.status='active' AND d.provisioning_state='ready'`, f.options.TenantID).Scan(&mailboxes); err != nil || mailboxes != 43 {
		t.Fatalf("mailboxes not listable for postfix snapshot: %d %v", mailboxes, err)
	}
	var audits int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='IMPORT legacy.activate-mail' AND detail->>'cutover_ready'='false' AND detail->>'managed_dns_emitted'='false'`, f.options.TenantID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("activate audit missing: %d %v", audits, err)
	}
	if _, err = ActivateLegacyDelegatedMail(t.Context(), f.pool, Options{
		TenantID: f.options.TenantID, ActorID: f.options.ActorID, TenantRef: f.options.TenantRef,
		Reason: "Second activate must find no pending_dns rows left",
	}, f.source.PrivateProofSHA256(), 7); !errors.Is(err, ErrActivateTarget) {
		t.Fatalf("expected idempotent refusal, got %v", err)
	}
}

func TestActivateLegacyDelegatedMailRequiresDKIMImport(t *testing.T) {
	f := newCoreFixture(t)
	if _, err := ApplyCore(t.Context(), f.pool, f.source, f.options); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateLegacyDelegatedMail(t.Context(), f.pool, Options{
		TenantID: f.options.TenantID, ActorID: f.options.ActorID, TenantRef: f.options.TenantRef,
		Reason: "Activate without DKIM import must be refused",
	}, f.source.PrivateProofSHA256(), 7); !errors.Is(err, ErrActivateTarget) {
		t.Fatalf("expected missing dkim refusal, got %v", err)
	}
}
