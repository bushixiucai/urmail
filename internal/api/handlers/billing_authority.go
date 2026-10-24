package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

type orderAuthorityFacts struct {
	ChannelID                                                      int64
	ID, TenantID                                                   int64
	OrderNo, PlanCode, Kind, Currency, Cycle                       string
	AmountCents                                                    int64
	QuotePayloadDigest, PlanSnapshotDigest, DocumentSnapshotDigest string
}

func orderAuthorityDigest(order store.Orders, payload, plan, document []byte) (string, error) {
	var err error
	facts := orderAuthorityFacts{ID: order.ID, TenantID: order.TenantID, OrderNo: order.OrderNo, PlanCode: order.PlanCode.String, Kind: order.Kind, Currency: order.Currency, Cycle: order.BillingCycle, AmountCents: order.AmountCents, ChannelID: order.ChannelID.Int64}
	facts.QuotePayloadDigest, err = billing.SnapshotDigest(payload)
	if err != nil {
		return "", err
	}
	facts.PlanSnapshotDigest, err = billing.SnapshotDigest(plan)
	if err != nil {
		return "", err
	}
	facts.DocumentSnapshotDigest, err = billing.SnapshotDigest(document)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return "", err
	}
	return billing.SnapshotDigest(raw)
}

// authorizePersistedOrder seals verified pricing only through the private platform role.
// App-owned intentions, including raw zero orders, never authenticate themselves.
func (h *BillingHandler) authorizePersistedOrder(ctx context.Context, order store.Orders, actor int64) error {
	if h.Pool == nil {
		if _, realStore := h.Store.(*store.Queries); !realStore {
			return nil
		}
		return errors.New("platform order authorization unavailable")
	}
	tx, err := h.Pool.PlatformTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	if err = q.PlatformLockBillingTenant(ctx, order.TenantID); err != nil {
		return err
	}
	quoted, err := q.PlatformGetOrderQuote(ctx, order.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		if order.AmountCents == 0 {
			return billing.ErrQuoteTampered
		}
		// Existing legacy sessions remain readable, but an unsigned intention
		// cannot start any new physical checkout, even for a positive amount.
		intent, lookupErr := q.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
		if lookupErr != nil || intent.AccountScope != "legacy" || intent.Provider != "stripe" {
			return billing.ErrQuoteTampered
		}
		return nil
	}
	if err != nil {
		return err
	}
	var quote billing.Quote
	if json.Unmarshal(quoted.Payload, &quote) != nil || quote.VerifySignature(h.quoteKey()) != nil || quote.VerifySnapshots(quoted.PlanSnapshot, quoted.DocumentSnapshot) != nil {
		return billing.ErrQuoteTampered
	}
	if quote.TenantID != order.TenantID || quote.PlanCode != order.PlanCode.String || quote.Kind != order.Kind || quote.FinalAmountCents != order.AmountCents || quote.Currency != order.Currency || quote.BillingCycle != order.BillingCycle || !order.QuoteDigest.Valid || quote.Digest != order.QuoteDigest.String {
		return billing.ErrQuoteTampered
	}
	if order.Status == "pending" {
		_, intentErr := q.PlatformGetCheckoutIntent(ctx, store.PlatformGetCheckoutIntentParams{TenantID: order.TenantID, OrderID: order.ID})
		if errors.Is(intentErr, pgx.ErrNoRows) {
			if err = verifyQuoteBillingCalendar(ctx, q, order.TenantID, quote.Calendar); err != nil {
				return err
			}
		} else if intentErr != nil {
			return intentErr
		}
	}
	role, err := q.GetUserRoleForTenant(ctx, store.GetUserRoleForTenantParams{UserID: actor, TenantID: order.TenantID})
	if err != nil || (role != "owner" && role != "admin" && role != "billing") {
		return billing.ErrQuoteTampered
	}
	attribution, attributionErr := q.GetChannelAttributionByTenant(ctx, order.TenantID)
	if attributionErr == nil {
		if !order.ChannelID.Valid || attribution.ChannelID != order.ChannelID.Int64 {
			return billing.ErrQuoteTampered
		}
	} else if !errors.Is(attributionErr, pgx.ErrNoRows) || order.ChannelID.Valid {
		return billing.ErrQuoteTampered
	}
	redemption, redemptionErr := q.PlatformLockCouponRedemptionForAuthority(ctx, store.PlatformLockCouponRedemptionForAuthorityParams{TenantID: order.TenantID, OrderID: pgtype.Int8{Int64: order.ID, Valid: true}})
	if quote.CouponCode != "" {
		if redemptionErr != nil || redemption != quote.CouponCode {
			return billing.ErrQuoteTampered
		}
	} else if redemptionErr == nil || !errors.Is(redemptionErr, pgx.ErrNoRows) {
		return billing.ErrQuoteTampered
	}
	digest, err := orderAuthorityDigest(order, quoted.Payload, quoted.PlanSnapshot, quoted.DocumentSnapshot)
	if err != nil {
		return err
	}
	previous, err := q.PlatformGetOrderAuthorization(ctx, store.PlatformGetOrderAuthorizationParams{TenantID: order.TenantID, OrderID: order.ID})
	if err == nil {
		if previous != digest {
			return billing.ErrQuoteTampered
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	changed, err := q.PlatformAuthorizeBillingOrder(ctx, store.PlatformAuthorizeBillingOrderParams{TenantID: order.TenantID, OrderID: order.ID, AuthorityDigest: digest, AuthorizedBy: pgtype.Int8{Int64: actor, Valid: actor > 0}})
	if err != nil {
		return err
	}
	if changed != 1 {
		return billing.ErrQuoteTampered
	}
	detail, _ := json.Marshal(map[string]any{"actor_type": "user", "outcome": "succeeded", "status": 200, "quote_digest": quote.Digest})
	if _, err = q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: order.TenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: actor, Valid: actor > 0}, Action: "AUTHORIZE order.pricing", TargetType: pgtype.Text{String: "orders", Valid: true}, TargetID: pgtype.Text{String: strconv.FormatInt(order.ID, 10), Valid: true}, Detail: detail}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func verifyPersistedOrderAuthority(ctx context.Context, q *store.Queries, order store.PlatformGetOrderForSettlementRow) error {
	quoted, err := q.PlatformGetOrderQuote(ctx, order.ID)
	if err != nil {
		return err
	}
	digest, err := orderAuthorityDigest(store.Orders{ID: order.ID, TenantID: order.TenantID, OrderNo: order.OrderNo, PlanCode: order.PlanCode, Kind: order.Kind, Currency: order.Currency, BillingCycle: order.BillingCycle, AmountCents: order.AmountCents, ChannelID: order.ChannelID}, quoted.Payload, quoted.PlanSnapshot, quoted.DocumentSnapshot)
	if err != nil {
		return err
	}
	sealed, err := q.PlatformGetOrderAuthorization(ctx, store.PlatformGetOrderAuthorizationParams{TenantID: order.TenantID, OrderID: order.ID})
	if err != nil {
		return err
	}

	var quote billing.Quote
	if json.Unmarshal(quoted.Payload, &quote) != nil {
		return billing.ErrQuoteTampered
	}
	if quote.VerifySnapshots(quoted.PlanSnapshot, quoted.DocumentSnapshot) != nil {
		return billing.ErrQuoteTampered
	}
	redemption, redemptionErr := q.PlatformLockCouponRedemptionForAuthority(ctx, store.PlatformLockCouponRedemptionForAuthorityParams{TenantID: order.TenantID, OrderID: pgtype.Int8{Int64: order.ID, Valid: true}})
	if quote.CouponCode != "" {
		if redemptionErr != nil || redemption != quote.CouponCode {
			return billing.ErrQuoteTampered
		}
	} else if redemptionErr == nil || !errors.Is(redemptionErr, pgx.ErrNoRows) {
		return billing.ErrQuoteTampered
	}
	if sealed != digest {
		return billing.ErrQuoteTampered
	}
	return nil
}
