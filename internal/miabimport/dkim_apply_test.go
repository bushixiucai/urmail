package miabimport

import (
	"errors"
	"strings"
	"testing"
)

func TestApplyReviewedDKIMPreservesExactPEMWithoutActivation(t *testing.T) {
	f := newCoreFixture(t)
	if _, err := ApplyCore(t.Context(), f.pool, f.source, f.options); err != nil {
		t.Fatal(err)
	}
	options, raw := dkFixture(t, f.source, 2048)
	reviewed, err := LoadReviewedDKIM(t.Context(), options, f.source)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyReviewedDKIM(t.Context(), f.pool, reviewed, Options{
		TenantID:  f.options.TenantID,
		ActorID:   f.options.ActorID,
		TenantRef: f.options.TenantRef,
		Reason:    "Preserve exact source DKIM private key without DNS activation",
	})
	if err != nil || !result.Applied || result.CutoverReady || result.Domains != 7 || result.Selector != "mail" {
		t.Fatalf("unexpected dkim apply: %+v %v", result, err)
	}
	var stored string
	var count int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*), min(dkim_private_key) FROM domains WHERE tenant_id=$1`, f.options.TenantID).Scan(&count, &stored); err != nil {
		t.Fatal(err)
	}
	if count != 7 || stored != string(raw) {
		t.Fatal("exact source PEM was not preserved on every pending domain")
	}
	var audits int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='IMPORT legacy.dkim' AND detail->>'cutover_ready'='false' AND detail->>'commit_outcome'='committed'`, f.options.TenantID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("dkim audit missing: %d %v", audits, err)
	}
	if _, err = ApplyReviewedDKIM(t.Context(), f.pool, reviewed, Options{
		TenantID: f.options.TenantID, ActorID: f.options.ActorID, TenantRef: f.options.TenantRef,
		Reason: "Second apply must refuse domains that already hold a private key",
	}); !errors.Is(err, ErrDKIMTarget) {
		t.Fatalf("expected target refusal after keys exist, got %v", err)
	}
	if strings.Contains(stored, "BEGIN") == false {
		t.Fatal("fixture expected PEM material in controlled test DB only")
	}
}

func TestApplyReviewedDKIMRequiresCommittedCoreImport(t *testing.T) {
	f := newCoreFixture(t)
	options, _ := dkFixture(t, f.source, 2048)
	reviewed, err := LoadReviewedDKIM(t.Context(), options, f.source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyReviewedDKIM(t.Context(), f.pool, reviewed, Options{
		TenantID: f.options.TenantID, ActorID: f.options.ActorID, TenantRef: f.options.TenantRef,
		Reason: "DKIM without a committed core import must be refused",
	}); !errors.Is(err, ErrDKIMTarget) {
		t.Fatalf("expected missing core import refusal, got %v", err)
	}
}
