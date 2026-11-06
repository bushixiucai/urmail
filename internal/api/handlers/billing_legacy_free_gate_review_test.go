package handlers

import (
	"context"
	"os"
	"strings"
	"testing"
)

// This migration regression recreates a historically overallocated free tenant.
// The new paid-term capacity gate must not revoke its existing mail authority.
func TestLegacyFreeCapacityMigrationPreservesMailAuthority(t *testing.T) {
	p, principal := mailboxAIFixture(t, 1)
	seatAIPlatformBudget(t, p, principal.TenantID)
	ctx := context.Background()
	if _, err := p.Exec(ctx, "UPDATE plans SET quota_bytes=1 WHERE code='free'"); err != nil {
		t.Fatal(err)
	}
	var definition string
	if err := p.QueryRow(ctx, "SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure)").Scan(&definition); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(definition, " AND /* capacity_activation_gate */")
	if start < 0 {
		t.Fatal("migration gate absent")
	}
	// Remove exactly the added balanced predicate to restore pre-0055 semantics.
	predicateStart := strings.Index(definition[start:], "(") + start
	depth, end := 0, -1
	for index := predicateStart; index < len(definition); index++ {
		switch definition[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = index + 1
			}
		}
		if end > 0 {
			break
		}
	}
	if end < 0 {
		t.Fatal("capacity predicate is unbalanced")
	}
	if _, err := p.Exec(ctx, definition[:start]+definition[end:]); err != nil {
		t.Fatal(err)
	}
	var before, after, fits bool
	if err := p.QueryRow(ctx, "SELECT urmail_mail_allowed($1),urmail_plan_snapshot_fits($1,(SELECT to_jsonb(p) FROM plans p WHERE code='free'))", principal.TenantID).Scan(&before, &fits); err != nil {
		t.Fatal(err)
	}
	if !before || fits {
		t.Fatalf("fixture must be mail-enabled but overallocated: allowed=%v fits=%v", before, fits)
	}
	migration, err := os.ReadFile("../../../migrations/0055_subscription_capacity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	var current string
	if err := p.QueryRow(ctx, "SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure)").Scan(&current); err != nil {
		t.Fatal(err)
	}
	// 0086 rewrote the period predicate into the 7-day grace form, so 0055's
	// literal replace no longer matches and replaying its DO block verbatim
	// raises MAIL_CAPACITY_GATE_UPDATE_REQUIRED by design. The property under
	// review is unchanged: activating the capacity gate on the CURRENT
	// definition must not revoke existing mail authority. Extract the gate
	// predicate from the migration text and apply it to the current
	// definition the same way 0055 did to its own.
	marker := "/* capacity_activation_gate */"
	markerAt := strings.Index(string(migration), marker)
	if markerAt < 0 {
		t.Fatal("actual migration activation block absent")
	}
	gateStart := strings.Index(string(migration)[markerAt:], "(") + markerAt
	gateDepth, gateEnd := 0, -1
	for index := gateStart; index < len(migration); index++ {
		switch migration[index] {
		case '(':
			gateDepth++
		case ')':
			gateDepth--
			if gateDepth == 0 {
				gateEnd = index + 1
			}
		}
		if gateEnd > 0 {
			break
		}
	}
	if gateEnd < 0 {
		t.Fatal("capacity predicate is unbalanced")
	}
	gate := string(migration[gateStart:gateEnd])
	if err := p.QueryRow(ctx, "SELECT pg_get_functiondef('urmail_mail_allowed(bigint)'::regprocedure)").Scan(&current); err != nil {
		t.Fatal(err)
	}
	const gracePredicate = "(s.period_end + interval '7 days' > now())"
	if !strings.Contains(current, gracePredicate) {
		t.Fatal("current mail gate no longer carries the 0086 grace predicate; update this regression")
	}
	if strings.Contains(current, marker) {
		t.Fatal("capacity gate already active")
	}
	patched := strings.Replace(current, gracePredicate, gracePredicate+" AND "+gate, 1)
	if _, err := p.Exec(ctx, patched); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "SELECT urmail_mail_allowed($1)", principal.TenantID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("0055 silently disabled existing free tenant: before=%v after=%v", before, after)
	}
}
