package billingrenew

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/store"
)

type recurringAdminSnapshot struct {
	merchant      store.PaymentMerchants
	capability    store.PaymentRecurringCapabilities
	hasCapability bool
}

func recurringAdminActor(ctx context.Context, q *store.Queries, actor []int64) error {
	// No actor is reserved for trusted in-process callers. Public handlers always
	// supply exactly one positive ContextUserID, including on internal failure.
	if len(actor) == 0 {
		return nil
	}
	if len(actor) != 1 || actor[0] <= 0 {
		return RuleError("FORBIDDEN_ROLE")
	}
	if _, e := q.PlatformLockRecurringAdminActor(ctx, actor[0]); e != nil {
		return RuleError("FORBIDDEN_ROLE")
	}
	return nil
}
func recurringAdminBefore(ctx context.Context, q *store.Queries, id int64, actor []int64) (recurringAdminSnapshot, error) {
	out := recurringAdminSnapshot{}
	if e := recurringAdminActor(ctx, q, actor); e != nil {
		return out, e
	}
	merchant, e := q.PlatformGetPaymentMerchant(ctx, id)
	if e != nil {
		return out, e
	}
	out.merchant = merchant
	capability, e := q.PlatformGetRecurringAdminCapability(ctx, id)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	out.capability = capability
	out.hasCapability = e == nil
	return out, nil
}
func recurringAdminAfter(ctx context.Context, q *store.Queries, previous recurringAdminSnapshot, actor []int64) error {
	if e := recurringAdminActor(ctx, q, actor); e != nil {
		return e
	}
	// FOR UPDATE also fences insertion of a previously absent capability via its
	// merchant FK. No provider I/O runs while this lock is held.
	merchant, e := q.PlatformLockRecurringAdminMerchant(ctx, previous.merchant.ID)
	if e != nil {
		return e
	}
	if !merchant.Enabled || !merchant.UpdatedAt.Time.Equal(previous.merchant.UpdatedAt.Time) || merchant.AccountID != previous.merchant.AccountID || merchant.Provider != previous.merchant.Provider || merchant.Environment != previous.merchant.Environment {
		return RuleError("CAPABILITY_UNVERIFIED")
	}
	capability, e := q.PlatformLockRecurringAdminCapability(ctx, merchant.ID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	exists := e == nil
	if exists != previous.hasCapability {
		return RuleError("CAPABILITY_UNVERIFIED")
	}
	if exists && (!capability.UpdatedAt.Time.Equal(previous.capability.UpdatedAt.Time) || capability.AdapterKey != previous.capability.AdapterKey || capability.MerchantApproved != previous.capability.MerchantApproved || capability.EvidenceAt != previous.capability.EvidenceAt || capability.MerchantConfigAt != previous.capability.MerchantConfigAt) {
		return RuleError("CAPABILITY_UNVERIFIED")
	}
	return nil
}
