package billingrenew

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/payment"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type evidenceRefreshAdapter struct {
	*StripeRecurring
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	fail    bool
}

func (a *evidenceRefreshAdapter) VerifyMerchant(ctx context.Context, _ payment.Merchant) (MerchantEvidence, error) {
	a.calls.Add(1)
	if a.started != nil {
		close(a.started)
		select {
		case <-a.release:
		case <-ctx.Done():
			return MerchantEvidence{}, ctx.Err()
		}
	}
	if a.fail {
		return MerchantEvidence{}, errors.New("controlled provider unavailable")
	}
	return MerchantEvidence{Reference: "controlled-read-only-account-evidence", ExpiresAt: time.Now().Add(15 * time.Minute)}, nil
}
func (*evidenceRefreshAdapter) Charge(context.Context, ChargeRequest) (ChargeOutcome, error) {
	panic("evidence refresh must never charge")
}

func TestMerchantEvidenceRefreshPG(t *testing.T) {
	platform, _ := testdb.OpenMigrated(t)
	q := store.New(platform)
	ctx := t.Context()
	create := func(t *testing.T, name string) (*Dispatcher, *evidenceRefreshAdapter, int64) {
		t.Helper()
		m, err := q.PlatformCreatePaymentMerchant(ctx, store.PlatformCreatePaymentMerchantParams{Provider: "stripe", Environment: "sandbox", AccountID: "acct_refresh_" + name, Name: name, Enabled: true, Currencies: []string{"USD"}, Entrypoint: "hosted", EncryptedConfig: "enc:v1:controlled", CheckoutLimits: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		err = q.PlatformApproveRecurringCapability(ctx, store.PlatformApproveRecurringCapabilityParams{MerchantID: m.ID, AdapterKey: StripeAdapterKey, EvidenceExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}, EvidenceKind: "sandbox", MerchantConfigAt: m.UpdatedAt, EvidenceReference: "old"})
		if err != nil {
			t.Fatal(err)
		}
		registry := &payment.Registry{Store: q, Secrets: stripeTestBox{}, BaseURL: "https://panel.example.test"}
		a := &evidenceRefreshAdapter{StripeRecurring: NewStripeRecurring(registry)}
		return &Dispatcher{Pool: platform, Registry: registry, Adapters: map[string]RecurringAdapter{StripeAdapterKey: a}}, a, m.ID
	}
	state := func(t *testing.T, id int64) (bool, bool, time.Time, time.Time, string) {
		t.Helper()
		var approved, ready bool
		var expires, updated time.Time
		var ref string
		if err := platform.QueryRow(ctx, "SELECT merchant_approved,runtime_available,evidence_expires_at,updated_at,evidence_reference FROM payment_recurring_capabilities WHERE merchant_id=$1", id).Scan(&approved, &ready, &expires, &updated, &ref); err != nil {
			t.Fatal(err)
		}
		return approved, ready, expires, updated, ref
	}
	for _, mode := range []string{"sustainable", "unapproved", "disabled", "config", "unregistered", "failure", "revoke_race", "disable_race", "config_race", "reapprove_race", "config_back_race", "adapter_race"} {
		t.Run(mode, func(t *testing.T) {
			// Isolate each round from earlier approved candidates in this shared fixture.
			if _, err := platform.Exec(ctx, "UPDATE payment_merchants SET enabled=false,updated_at=clock_timestamp()"); err != nil {
				t.Fatal(err)
			}
			d, a, id := create(t, mode)
			_, _, oldExpiry, _, _ := state(t, id)
			exec := func(sql string) {
				t.Helper()
				if _, err := platform.Exec(ctx, sql, id); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "unapproved":
				exec("UPDATE payment_recurring_capabilities SET merchant_approved=false,updated_at=clock_timestamp() WHERE merchant_id=$1")
			case "disabled":
				exec("UPDATE payment_merchants SET enabled=false,updated_at=clock_timestamp() WHERE id=$1")
			case "config":
				exec("UPDATE payment_merchants SET name='new configuration',updated_at=clock_timestamp() WHERE id=$1")
			case "unregistered":
				d.Adapters = map[string]RecurringAdapter{}
			case "failure":
				a.fail = true
			}
			if mode == "revoke_race" || mode == "disable_race" || mode == "config_race" || mode == "reapprove_race" || mode == "config_back_race" || mode == "adapter_race" {
				a.started = make(chan struct{})
				a.release = make(chan struct{})
				done := make(chan error, 1)
				go func() { done <- d.RefreshRuntime(ctx) }()
				select {
				case <-a.started:
				case <-time.After(5 * time.Second):
					t.Fatal("verification did not start")
				}
				switch mode {
				case "revoke_race":
					exec("UPDATE payment_recurring_capabilities SET merchant_approved=false,runtime_available=false,updated_at=clock_timestamp() WHERE merchant_id=$1")
				case "disable_race":
					exec("UPDATE payment_merchants SET enabled=false,updated_at=clock_timestamp() WHERE id=$1")
				case "config_race":
					exec("UPDATE payment_merchants SET name='changed during network',updated_at=clock_timestamp() WHERE id=$1")
				case "adapter_race":
					exec("UPDATE payment_recurring_capabilities SET adapter_key='different-adapter',updated_at=clock_timestamp() WHERE merchant_id=$1")
				case "reapprove_race":
					exec("UPDATE payment_recurring_capabilities SET merchant_approved=false,updated_at=clock_timestamp() WHERE merchant_id=$1")
					exec("UPDATE payment_recurring_capabilities SET merchant_approved=true,evidence_reference='new-manual-approval',updated_at=clock_timestamp() WHERE merchant_id=$1")
				case "config_back_race":
					exec("UPDATE payment_merchants SET name='intermediate',updated_at=clock_timestamp() WHERE id=$1")
					exec("UPDATE payment_merchants SET name='config_back_race',updated_at=clock_timestamp() WHERE id=$1")
				}
				close(a.release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			} else if err := d.RefreshRuntime(ctx); err != nil {
				t.Fatal(err)
			}
			approved, ready, expiry, _, ref := state(t, id)
			if mode == "sustainable" {
				if !approved || !ready || !expiry.After(time.Now().Add(14*time.Minute)) || a.calls.Load() != 1 {
					t.Fatal(approved, ready, expiry, a.calls.Load())
				}
				// Healthy TTL must not trigger another network lookup or no-op DB write.
				_, _, _, firstUpdated, _ := state(t, id)
				if err := d.RefreshRuntime(ctx); err != nil {
					t.Fatal(err)
				}
				_, _, _, afterUpdated, _ := state(t, id)
				if a.calls.Load() != 1 || !firstUpdated.Equal(afterUpdated) {
					t.Fatal("healthy evidence caused unnecessary work")
				}
				// A subsequent cycle can extend the same manual approval again.
				exec("UPDATE payment_recurring_capabilities SET evidence_expires_at=now()+interval '4 minutes',updated_at=clock_timestamp() WHERE merchant_id=$1")
				if err := d.RefreshRuntime(ctx); err != nil {
					t.Fatal(err)
				}
				if a.calls.Load() != 2 {
					t.Fatal("next cycle did not refresh")
				}
			} else {
				if ready || !expiry.Equal(oldExpiry) || ref == "controlled-read-only-account-evidence" {
					t.Fatalf("stale authority extended: %v %v %s %s", approved, ready, expiry, ref)
				}
				if mode == "revoke_race" && approved {
					t.Fatal("revoked approval revived")
				}
				if mode == "unapproved" || mode == "disabled" || mode == "config" || mode == "unregistered" {
					if a.calls.Load() != 0 {
						t.Fatal("ineligible merchant contacted provider")
					}
				}
			}
		})
	}
	// The external request budget is shared by the whole round. Successful
	// accounts leave the due set, so later rounds service the remaining accounts.
	if _, err := platform.Exec(ctx, "UPDATE payment_merchants SET enabled=false,updated_at=clock_timestamp()"); err != nil {
		t.Fatal(err)
	}
	var bounded *Dispatcher
	var shared *evidenceRefreshAdapter
	for i := range 6 {
		bounded, shared, _ = create(t, fmt.Sprintf("budget_%d", i))
	}
	if err := bounded.RefreshRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if shared.calls.Load() != merchantRefreshPerRound {
		t.Fatal("unbounded verification round", shared.calls.Load())
	}
	if err := bounded.RefreshRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if shared.calls.Load() != 6 {
		t.Fatal("remaining due merchants were starved", shared.calls.Load())
	}
	// Runtime convergence also fences a key switch after the caller re-read.
	var finalID int64
	if err := platform.QueryRow(ctx, "SELECT max(merchant_id) FROM payment_recurring_capabilities").Scan(&finalID); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "UPDATE payment_recurring_capabilities SET adapter_key='changed-after-snapshot',runtime_available=true WHERE merchant_id=$1", finalID); err != nil {
		t.Fatal(err)
	}
	if err := q.PlatformConvergeRecurringRuntime(ctx, store.PlatformConvergeRecurringRuntimeParams{MerchantID: finalID, AdapterKey: StripeAdapterKey, Provider: "stripe", AdapterAvailable: true}); err != nil {
		t.Fatal(err)
	}
	_, ready, _, _, _ := state(t, finalID)
	if ready {
		t.Fatal("stale adapter snapshot revived runtime")
	}
	// Corrupt/incomplete proof is fail-closed rather than NULL runtime state.
	if _, err := platform.Exec(ctx, "UPDATE payment_recurring_capabilities SET adapter_key=$2,runtime_available=true,evidence_expires_at=NULL WHERE merchant_id=$1", finalID, StripeAdapterKey); err != nil {
		t.Fatal(err)
	}
	if err := q.PlatformConvergeRecurringRuntime(ctx, store.PlatformConvergeRecurringRuntimeParams{MerchantID: finalID, AdapterKey: StripeAdapterKey, Provider: "stripe", AdapterAvailable: true}); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "SELECT runtime_available FROM payment_recurring_capabilities WHERE merchant_id=$1", finalID).Scan(&ready); err != nil || ready {
		t.Fatal("missing proof did not close runtime", err)
	}
	// Approval/runtime facts do not mutate any customer financial or consent rows.
	var intents int
	if err := platform.QueryRow(ctx, "SELECT (SELECT count(*) FROM payments)+(SELECT count(*) FROM subscription_renewal_authorizations)+(SELECT count(*) FROM subscription_renewal_enrollments)").Scan(&intents); err != nil || intents != 0 {
		t.Fatal(fmt.Sprint(intents), err)
	}
}
