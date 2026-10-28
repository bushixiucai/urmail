package billingrenew

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

const merchantRefreshPerRound = 4

// refreshMerchantEvidence only extends technical evidence for an existing,
// unchanged manual approval. It cannot approve a merchant or grant debit consent.
func (d *Dispatcher) refreshMerchantEvidence(ctx context.Context) error {
	if d.Registry == nil || len(d.Adapters) == 0 {
		return nil
	}
	keys := make([]string, 0, len(d.Adapters))
	for key, adapter := range d.Adapters {
		if adapter != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)
	q := store.New(d.Pool)
	rows, err := q.PlatformListRecurringEvidenceRefreshCandidates(ctx, keys)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	// Rotate each minute so a permanently failing account cannot monopolize
	// every bounded round. No new durable scheduler or financial state is needed.
	offset := int((time.Now().Unix() / 60) % int64(len(rows)))
	refreshCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	attempted := 0
	for i := 0; i < len(rows) && attempted < merchantRefreshPerRound; i++ {
		if refreshCtx.Err() != nil {
			break
		}
		row := rows[(offset+i)%len(rows)]
		adapter := d.Adapters[row.AdapterKey]
		if adapter == nil || adapter.Name() != row.Provider {
			continue
		}
		attempted++
		perCtx, perCancel := context.WithTimeout(refreshCtx, 8*time.Second)
		m, _, loadErr := d.Registry.Load(perCtx, row.MerchantID, true)
		if loadErr != nil || m.Provider != row.Provider || m.Environment != row.Environment {
			perCancel()
			continue
		}
		evidence, verifyErr := adapter.VerifyMerchant(perCtx, m)
		if verifyErr != nil || perCtx.Err() != nil || evidence.Reference == "" || len(evidence.Reference) > 500 || !evidence.ExpiresAt.After(time.Now()) || evidence.ExpiresAt.After(time.Now().AddDate(1, 0, 0)) {
			perCancel()
			continue
		}
		_, updateErr := q.PlatformRefreshRecurringEvidence(perCtx, store.PlatformRefreshRecurringEvidenceParams{
			MerchantID: row.MerchantID, AdapterKey: row.AdapterKey, CapabilityUpdatedAt: row.UpdatedAt, PreviousEvidenceAt: row.EvidenceAt, MerchantConfigAt: row.MerchantConfigAt,
			Reference: evidence.Reference, ExpiresAt: pgtype.Timestamptz{Time: evidence.ExpiresAt, Valid: true},
		})
		perCancel()
		if updateErr != nil {
			return updateErr
		}
	}
	// Provider failures deliberately preserve the previous deadline. The caller
	// re-reads authoritative state and closes readiness when it has expired.
	return nil
}
