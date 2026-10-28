package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/billingrenew"
	"github.com/bushixiucai/urmail/internal/payment"
)

type recurringAdminProvider struct {
	*recurringSandboxAdapter
	hook func() error
}

func (a *recurringAdminProvider) VerifyMerchant(ctx context.Context, m payment.Merchant) (billingrenew.MerchantEvidence, error) {
	if a.hook != nil {
		if e := a.hook(); e != nil {
			return billingrenew.MerchantEvidence{}, e
		}
	}
	return a.recurringSandboxAdapter.VerifyMerchant(ctx, m)
}
func (a *recurringAdminProvider) VerifyAuthorization(ctx context.Context, m payment.Merchant, ref string) (billingrenew.VerifiedAuthorization, error) {
	if a.hook != nil {
		if e := a.hook(); e != nil {
			return billingrenew.VerifiedAuthorization{}, e
		}
	}
	return a.recurringSandboxAdapter.VerifyAuthorization(ctx, m, ref)
}

func TestRecurringPlatformAuthorityRecheckedAfterProvider_PG(t *testing.T) {
	for _, operation := range []string{"approve", "binding"} {
		for _, change := range []string{"admin_revoked", "user_disabled", "merchant_disabled", "merchant_changed", "capability_revoked_then_reapproved"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				f := newRecurringFixture(t)
				if _, e := f.s.p.Exec(t.Context(), "INSERT INTO platform_admins(user_id) VALUES($1)", f.s.uid); e != nil {
					t.Fatal(e)
				}
				if _, e := f.s.p.Exec(t.Context(), "UPDATE subscription_renewal_authorizations SET revoked_at=now() WHERE tenant_id=$1", f.s.tid); e != nil {
					t.Fatal(e)
				}
				adapter := &recurringAdminProvider{recurringSandboxAdapter: f.a}
				f.d.Adapters["isolated-sandbox-v1"] = adapter
				var before time.Time
				if e := f.s.p.QueryRow(t.Context(), "SELECT updated_at FROM payment_recurring_capabilities WHERE merchant_id=$1", f.merchant).Scan(&before); e != nil {
					t.Fatal(e)
				}
				called := false
				adapter.hook = func() error {
					called = true
					// This represents authority changing while the external provider GET runs.
					// These writes would block if the RPC incorrectly retained authority locks.
					var query string
					var arg int64
					switch change {
					case "admin_revoked":
						query = "DELETE FROM platform_admins WHERE user_id=$1"
						arg = f.s.uid
					case "user_disabled":
						query = "UPDATE users SET status='disabled' WHERE id=$1"
						arg = f.s.uid
					case "merchant_disabled":
						query = "UPDATE payment_merchants SET enabled=false,updated_at=clock_timestamp() WHERE id=$1"
						arg = f.merchant
					case "merchant_changed":
						query = "UPDATE payment_merchants SET name='Changed during provider request',updated_at=clock_timestamp() WHERE id=$1"
						arg = f.merchant
					case "capability_revoked_then_reapproved":
						if _, e := f.s.p.Exec(t.Context(), "UPDATE payment_recurring_capabilities SET merchant_approved=false,updated_at=clock_timestamp() WHERE merchant_id=$1", f.merchant); e != nil {
							return e
						}
						query = "UPDATE payment_recurring_capabilities SET merchant_approved=true,evidence_reference='new independent approval',updated_at=clock_timestamp() WHERE merchant_id=$1"
						arg = f.merchant
					}
					_, e := f.s.p.Exec(t.Context(), query, arg)
					return e
				}
				var e error
				if operation == "approve" {
					e = f.d.ApproveMerchant(t.Context(), f.merchant, "isolated-sandbox-v1", f.s.uid)
				} else {
					_, e = f.d.VerifyBinding(t.Context(), f.s.tid, f.subscription, f.merchant, "provider-verified-fixture-reference", f.s.uid)
				}
				if e == nil || !called {
					t.Fatal("stale platform authority accepted", operation, change, e)
				}
				if operation == "binding" {
					var revoked bool
					if e = f.s.p.QueryRow(t.Context(), "SELECT revoked_at IS NOT NULL FROM subscription_renewal_authorizations WHERE tenant_id=$1 AND id=$2", f.s.tid, f.binding).Scan(&revoked); e != nil || !revoked {
						t.Fatal("stale verifier resurrected binding", revoked, e)
					}
				}
				if change == "capability_revoked_then_reapproved" {
					var reference string
					if e = f.s.p.QueryRow(t.Context(), "SELECT evidence_reference FROM payment_recurring_capabilities WHERE merchant_id=$1", f.merchant).Scan(&reference); e != nil || reference != "new independent approval" {
						t.Fatal("old response replaced new approval epoch", reference, e)
					}
				}
			})
		}
	}
}

func TestRecurringPlatformAuthorityPositiveAndZeroActor_PG(t *testing.T) {
	f := newRecurringFixture(t)
	if _, e := f.s.p.Exec(t.Context(), "INSERT INTO platform_admins(user_id) VALUES($1)", f.s.uid); e != nil {
		t.Fatal(e)
	}
	if e := f.d.ApproveMerchant(t.Context(), f.merchant, "isolated-sandbox-v1", 0); e == nil {
		t.Fatal("explicit absent public actor used trusted bypass")
	}
	if _, e := f.d.VerifyBinding(t.Context(), f.s.tid, f.subscription, f.merchant, "provider-verified-fixture-reference", 0); e == nil {
		t.Fatal("explicit absent public binding actor used trusted bypass")
	}
	if e := f.d.ApproveMerchant(t.Context(), f.merchant, "isolated-sandbox-v1", f.s.uid); e != nil {
		t.Fatal("live platform approval failed", e)
	}
	if _, e := f.d.VerifyBinding(t.Context(), f.s.tid, f.subscription, f.merchant, "provider-verified-fixture-reference", f.s.uid); e != nil {
		t.Fatal("live platform binding failed", e)
	}
}
