package billingrenew

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

type fakeStore struct {
	row              store.SubscriptionRenewalAttempts
	claimed          bool
	failed, notified int
	canceled         int
	state            string
	uncertain        int
	lastFailure      string
	dedupeKeys       []string
}

func (f *fakeStore) PlatformEnqueueDueRenewals(context.Context) error { return nil }
func (f *fakeStore) PlatformClaimRenewalAttempt(context.Context) (store.SubscriptionRenewalAttempts, error) {
	if f.claimed {
		return store.SubscriptionRenewalAttempts{}, pgx.ErrNoRows
	}
	f.claimed = true
	return f.row, nil
}
func (f *fakeStore) PlatformFailRenewalAttempt(_ context.Context, arg store.PlatformFailRenewalAttemptParams) error {
	f.lastFailure = arg.LastError
	f.failed++
	return nil
}
func (f *fakeStore) PlatformEnqueueRenewalFailure(context.Context, int64) error {
	f.notified++
	return nil
}
func (f *fakeStore) PlatformMarkRenewalAttemptUncertain(_ context.Context, _ store.PlatformMarkRenewalAttemptUncertainParams) error {
	f.uncertain++
	return nil
}
func (f *fakeStore) PlatformSetRenewalDedupeKey(_ context.Context, arg store.PlatformSetRenewalDedupeKeyParams) error {
	f.dedupeKeys = append(f.dedupeKeys, arg.DedupeKey)
	return nil
}
func (f *fakeStore) PlatformGetRenewalChargeContext(context.Context, int64) (store.PlatformGetRenewalChargeContextRow, error) {
	return store.PlatformGetRenewalChargeContextRow{}, pgx.ErrNoRows
}

func (f *fakeStore) PlatformRecoverRenewalLeases(context.Context) error { return nil }
func (f *fakeStore) PlatformCompleteRenewalClaim(_ context.Context, p store.PlatformCompleteRenewalClaimParams) (int64, error) {
	f.state = p.Column4
	f.lastFailure = p.LastError
	switch p.Column4 {
	case "canceled":
		f.canceled++
	case "uncertain":
		f.uncertain++
	case "failed":
		f.failed++
	}
	return 1, nil
}
func (f *fakeStore) BeginCharge(_ context.Context, row store.SubscriptionRenewalAttempts, key string) (store.SubscriptionRenewalAttempts, error) {
	f.dedupeKeys = append(f.dedupeKeys, key)
	return row, nil
}

type fakeSettlements struct {
	params []RenewalSettlementParams
	result RenewalSettlement
	err    error
}

func (f *fakeSettlements) PlatformSettleRenewalAttempt(_ context.Context, params RenewalSettlementParams) (RenewalSettlement, error) {
	f.params = append(f.params, params)
	return f.result, f.err
}

type fakeCharger struct {
	outcome ChargeOutcome
	err     error
	reqs    []ChargeRequest
}

func (f *fakeCharger) Name() string { return "stripe" }
func (f *fakeCharger) Charge(_ context.Context, req ChargeRequest) (ChargeOutcome, error) {
	f.reqs = append(f.reqs, req)
	return f.outcome, f.err
}

type fakeResolver struct {
	charger Charger
	req     ChargeRequest
	err     error
}

func (f fakeResolver) ChargerFor(_ context.Context, _ store.SubscriptionRenewalAttempts) (Charger, ChargeRequest, error) {
	if f.err != nil {
		return nil, ChargeRequest{}, f.err
	}
	return f.charger, f.req, nil
}

func TestRunOnceDoesNotInventPayment(t *testing.T) {
	f := &fakeStore{row: store.SubscriptionRenewalAttempts{ID: 4}}
	if err := (Worker{Store: f, Coordinator: f}).RunOnce(context.Background()); err != nil || f.canceled != 1 || f.notified != 0 {
		t.Fatalf("err=%v failed=%d notified=%d", err, f.failed, f.notified)
	}
	if f.lastFailure != "ADAPTER_UNAVAILABLE" {
		t.Fatalf("unexpected failure reason %q", f.lastFailure)
	}
}

func TestRunOnceWithoutChargingChannelKeepsExplicitFailure(t *testing.T) {
	f := &fakeStore{row: store.SubscriptionRenewalAttempts{ID: 4}}
	w := Worker{Store: f, Coordinator: f, Chargers: fakeResolver{err: ErrNoChargingChannel}}
	if err := w.RunOnce(context.Background()); err != nil || f.canceled != 1 || f.notified != 0 {
		t.Fatalf("err=%v failed=%d notified=%d", err, f.failed, f.notified)
	}
	if len(f.dedupeKeys) != 0 {
		t.Fatalf("no dedupe key should be recorded without a charge: %v", f.dedupeKeys)
	}
}

func TestRunOnceChargeSuccessSettlesIdempotently(t *testing.T) {
	periodEnd := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	f := &fakeStore{row: store.SubscriptionRenewalAttempts{ID: 7, TenantID: 3, SubscriptionID: 9, PeriodEnd: pgtype.Timestamptz{Time: periodEnd, Valid: true}}}
	settlements := &fakeSettlements{}
	charger := &fakeCharger{outcome: ChargeOutcome{Status: ChargeSucceeded, ProviderTxnID: "txn_1", AmountCents: 1000, Currency: "CNY"}}
	w := Worker{Store: f, Coordinator: f, Chargers: fakeResolver{charger: charger, req: ChargeRequest{AmountCents: 1000, Currency: "CNY"}}, Settlements: settlements}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(settlements.params) != 1 || settlements.params[0].ProviderTxnID != "txn_1" || settlements.params[0].AttemptID != 7 {
		t.Fatalf("unexpected settlement params %+v", settlements.params)
	}
	if len(f.dedupeKeys) != 1 || f.dedupeKeys[0] != RenewalDedupeKey(9, periodEnd, 7)+":charge-1" {
		t.Fatalf("dedupe key not recorded: %v", f.dedupeKeys)
	}
	if len(charger.reqs) != 1 || charger.reqs[0].IdempotencyKey == "" {
		t.Fatalf("charge must carry an idempotency key: %+v", charger.reqs)
	}
}

func TestRunOnceChargeUncertainDefersWithoutRecharge(t *testing.T) {
	f := &fakeStore{row: store.SubscriptionRenewalAttempts{ID: 7, TenantID: 3}}
	charger := &fakeCharger{outcome: ChargeOutcome{Status: ChargeUncertain}}
	w := Worker{Store: f, Coordinator: f, Chargers: fakeResolver{charger: charger}, Settlements: &fakeSettlements{}}
	if err := w.RunOnce(context.Background()); err != nil || f.uncertain != 1 {
		t.Fatalf("err=%v uncertain=%d", err, f.uncertain)
	}
	if f.failed != 0 || f.notified != 0 {
		t.Fatalf("an uncertain charge is neither a failure nor a notification: failed=%d notified=%d", f.failed, f.notified)
	}
}

func TestRunOnceChargeErrorIsUncertain(t *testing.T) {
	f := &fakeStore{row: store.SubscriptionRenewalAttempts{ID: 7, TenantID: 3}}
	charger := &fakeCharger{err: errors.New("gateway down")}
	w := Worker{Store: f, Coordinator: f, Chargers: fakeResolver{charger: charger}, Settlements: &fakeSettlements{}}
	if err := w.RunOnce(context.Background()); err != nil || f.uncertain != 1 || f.notified != 0 {
		t.Fatalf("err=%v failed=%d notified=%d", err, f.failed, f.notified)
	}
	if f.lastFailure != "PROVIDER_UNCERTAIN" {
		t.Fatalf("unexpected failure reason %q", f.lastFailure)
	}
}

func TestRunOnceChargeSuccessWithoutSettlementStoreFails(t *testing.T) {
	f := &fakeStore{row: store.SubscriptionRenewalAttempts{ID: 7, TenantID: 3}}
	charger := &fakeCharger{outcome: ChargeOutcome{Status: ChargeSucceeded, ProviderTxnID: "txn_1", AmountCents: 1000, Currency: "CNY"}}
	w := Worker{Store: f, Coordinator: f, Chargers: fakeResolver{charger: charger}}
	if err := w.RunOnce(context.Background()); err != nil || f.uncertain != 1 {
		t.Fatal("a confirmed charge without settlement must be held uncertain")
	}
}

type reconcilingCharger struct {
	fakeCharger
	queries int
}

func (f *reconcilingCharger) Query(_ context.Context, _ ChargeRequest) (ChargeOutcome, error) {
	f.queries++
	return ChargeOutcome{Status: ChargeUncertain}, nil
}
func TestRunOnceUnknownNeverChargesAgain(t *testing.T) {
	f := &fakeStore{row: store.SubscriptionRenewalAttempts{ID: 7, TenantID: 3, SideEffectIntentAt: pgtype.Timestamptz{Time: time.Now().AddDate(-1, 0, 0), Valid: true}, DedupeKey: "original"}}
	charger := &reconcilingCharger{}
	w := Worker{Store: f, Coordinator: f, Chargers: fakeResolver{charger: charger, req: ChargeRequest{IdempotencyKey: "original"}}}
	for range 3 {
		f.claimed = false
		if err := w.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(charger.reqs) != 0 || charger.queries != 3 || f.uncertain != 3 {
		t.Fatalf("charge=%d query=%d uncertain=%d", len(charger.reqs), charger.queries, f.uncertain)
	}
}

func TestRenewalDedupeKeyFromPayload(t *testing.T) {
	cases := []struct {
		payload string
		want    string
	}{
		{"{\"idempotency_key\":\"9:20261101T000000Z:7\"}", "9:20261101T000000Z:7"},
		{"{\"metadata\":{\"idempotency_key\":\"k2\"}}", "k2"},
		{"{\"data\":{\"object\":{\"metadata\":{\"idempotency_key\":\"k3\"}}}}", "k3"},
		{"{\"data\":{\"metadata\":{\"idempotency_key\":\"k4\"}}}", "k4"},
		{"{\"other\":1}", ""},
		{"not json", ""},
	}
	for _, c := range cases {
		if got := RenewalDedupeKeyFromPayload([]byte(c.payload)); got != c.want {
			t.Fatalf("%s: got %q want %q", c.payload, got, c.want)
		}
	}
}
