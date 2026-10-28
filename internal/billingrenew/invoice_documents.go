package billingrenew

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

// CaptureInvoiceDocumentSnapshot freezes current identities when a new quote is
// issued. Callers settling legacy orders must pass "observed_at_settlement";
// historical invoices are never backfilled by a download.
func CaptureInvoiceDocumentSnapshot(ctx context.Context, q *store.Queries, tenantID, amountCents int64, currency string, stage ...string) ([]byte, error) {
	seller, buyer := billing.DocumentProfile{}, billing.DocumentProfile{}
	for _, target := range []struct {
		scope   string
		id      int64
		profile *billing.DocumentProfile
	}{{"platform", 0, &seller}, {"tenant", tenantID, &buyer}} {
		raw, err := q.PlatformGetDocumentProfile(ctx, store.PlatformGetDocumentProfileParams{Scope: target.scope, ScopeID: target.id})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, target.profile) != nil {
			return nil, billing.ErrDocumentInvalid
		}
	}
	buyerConfigured := buyer.Party.Name != ""
	if buyer.Party.Name == "" {
		tenant, err := q.GetTenantByID(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		buyer.Party.Name = tenant.Name
	}
	provenance := "quote"
	if len(stage) > 0 {
		provenance = stage[0]
	}
	switch provenance {
	case "quote", "observed_at_settlement", "observed_at_issuance":
	default:
		return nil, billing.ErrDocumentInvalid
	}
	snapshot, err := billing.BuildDocumentSnapshot(seller, buyer, amountCents, currency, provenance, time.Now())
	if err != nil {
		return nil, err
	}
	snapshot.SellerSource = "profile"
	if seller.Party.Name == "" {
		snapshot.SellerSource = "unconfigured"
	}
	snapshot.BuyerSource = "profile"
	if !buyerConfigured {
		snapshot.BuyerSource = "workspace_name"
	}
	return json.Marshal(snapshot)
}
