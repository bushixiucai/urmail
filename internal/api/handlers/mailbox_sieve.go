package handlers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/sieve"
	"github.com/bushixiucai/urmail/internal/store"
)

// SieveActivator exposes only persisted-mailbox script activation.
type SieveActivator interface {
	ActivateSieve(context.Context, int64, int64, string) error
}

// RunSieveReconciler retries persisted desired scripts after a crash or a typed
// agent failure. It has no arbitrary shell operation or caller-supplied path.
func RunSieveReconciler(ctx context.Context, pool *db.Pool, activator SieveActivator, logger *zap.Logger) {
	if pool == nil || activator == nil {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	applied := map[int64]string{}
	for {
		rows, err := store.New(pool).PlatformListSieveScripts(ctx)
		if err != nil {
			logger.Error("list desired sieve scripts", zap.Error(err))
		} else {
			for _, row := range rows {
				if !row.SieveScript.Valid {
					continue
				}
				digest := sieve.Digest(row.SieveScript.String)
				if applied[row.ID] == digest {
					continue
				}
				applyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				err = activator.ActivateSieve(applyCtx, row.TenantID, row.ID, digest)
				cancel()
				if err != nil {
					logger.Warn("activate desired sieve script", zap.Int64("mailbox_id", row.ID))
					continue
				}
				detail, _ := json.Marshal(map[string]any{"actor_type": "system", "mailbox_id": row.ID, "digest": digest, "status": 200, "outcome": "succeeded"})
				if _, auditErr := store.New(pool).CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: row.TenantID, Valid: true}, Action: "sieve.activated", TargetType: pgtype.Text{String: "mailbox", Valid: true}, Detail: detail}); auditErr != nil {
					logger.Error("write sieve activation audit", zap.Error(auditErr))
					continue
				}
				applied[row.ID] = digest
			}
		}
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
