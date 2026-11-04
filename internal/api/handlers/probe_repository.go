package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// ErrProbeForbidden indicates missing or revoked probe authorization.
var ErrProbeForbidden = errors.New("probe authorization changed or denied")

// ErrProbeChanged indicates the network result no longer matches its snapshot.
var ErrProbeChanged = errors.New("probe snapshot changed")

// ProbeActor is authenticated identity, never a client-supplied tenant selector.
type ProbeActor struct{ TenantID, UserID int64 }

// DomainVerificationRepository cannot run arbitrary queries or return a pool/transaction.
// Each method must commit/release all database resources before returning.
type DomainVerificationRepository interface {
	ReadVerification(context.Context, ProbeActor, int64) (store.GetDomainByIDRow, error)
	CompleteVerification(context.Context, ProbeActor, store.GetDomainByIDRow) (store.VerifyDomainChallengeRow, error)
}

// AIHealthSnapshot pins provider configuration and current cloud authorization.
type AIHealthSnapshot struct {
	Configuration   store.AiProviderConfigs
	Present         bool
	CloudAuthorized bool
}

// AIHealthRepository is the complete database capability granted to Health.
// It exposes neither PlatformPool nor a callback with unrestricted queries.
type AIHealthRepository interface {
	ReadHealth(context.Context, ProbeActor) (AIHealthSnapshot, error)
	CompleteHealth(context.Context, ProbeActor, AIHealthSnapshot, bool, string) error
}

type probeRepository struct{ pool *db.Pool }
type domainVerificationRepository struct{ probeRepository }
type aiHealthRepository struct{ probeRepository }

// NewDomainVerificationRepository grants only tenant domain verification operations.
func NewDomainVerificationRepository(pool *db.Pool) DomainVerificationRepository {
	return &domainVerificationRepository{probeRepository{pool: pool}}
}

// NewAIHealthRepository encapsulates platform-owned AI configuration reads.
// AI configuration is not SELECT-granted to urmail_app.
// The privileged pool is encapsulated here; all operations are tenant-filtered
// and use UserTx (SET LOCAL tenant/user), never unscoped platform transactions.
func NewAIHealthRepository(pool *db.Pool) AIHealthRepository {
	return &aiHealthRepository{probeRepository{pool: pool}}
}

func (r probeRepository) transaction(ctx context.Context, actor ProbeActor, fn func(context.Context, *store.Queries) error) error {
	if r.pool == nil || actor.TenantID <= 0 || actor.UserID <= 0 {
		return ErrProbeForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := r.pool.UserTx(ctx, actor.TenantID, actor.UserID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := store.New(tx)
	// Match tenant mutations' existing lock protocol, but only for these short
	// database phases. In particular no lock is held during DNS/HTTP I/O.
	if err = q.PlatformLockBillingTenant(ctx, actor.TenantID); err != nil {
		return err
	}
	role, err := q.GetUserRoleForTenant(ctx, store.GetUserRoleForTenantParams{UserID: actor.UserID, TenantID: actor.TenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProbeForbidden
		}
		return err
	}
	if role != "owner" && role != "admin" {
		return ErrProbeForbidden
	}
	if err = fn(ctx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *domainVerificationRepository) ReadVerification(ctx context.Context, actor ProbeActor, id int64) (row store.GetDomainByIDRow, err error) {
	err = r.transaction(ctx, actor, func(ctx context.Context, q *store.Queries) error {
		var e error
		row, e = q.GetDomainByID(ctx, store.GetDomainByIDParams{TenantID: actor.TenantID, ID: id})
		return e
	})
	return
}
func (r *domainVerificationRepository) CompleteVerification(ctx context.Context, actor ProbeActor, snapshot store.GetDomainByIDRow) (row store.VerifyDomainChallengeRow, err error) {
	err = r.transaction(ctx, actor, func(ctx context.Context, q *store.Queries) error {
		current, e := q.GetDomainByID(ctx, store.GetDomainByIDParams{TenantID: actor.TenantID, ID: snapshot.ID})
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrProbeChanged
		}
		if e != nil {
			return e
		}
		if snapshot.TenantID != actor.TenantID || !reflect.DeepEqual(current, snapshot) {
			return ErrProbeChanged
		}
		row, e = q.VerifyDomainChallenge(ctx, store.VerifyDomainChallengeParams{TenantID: actor.TenantID, ID: snapshot.ID, VerificationToken: snapshot.VerificationToken})
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrProbeChanged
		}
		if e != nil {
			return e
		}
		return q.RecordTenantAudit(ctx, store.RecordTenantAuditParams{TenantID: actor.TenantID, UserID: actor.UserID, Action: "POST /domains/:id/verify", Target: strconv.FormatInt(snapshot.ID, 10), Status: 200})
	})
	return
}
func healthSnapshot(ctx context.Context, q *store.Queries, actor ProbeActor) (AIHealthSnapshot, error) {
	cfg, err := q.PlatformGetAIProvider(ctx, actor.TenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AIHealthSnapshot{}, nil
	}
	if err != nil {
		return AIHealthSnapshot{}, err
	}
	s := AIHealthSnapshot{Configuration: cfg, Present: true}
	if cfg.Provider != "ollama" {
		s.CloudAuthorized, err = q.AICloudAuthorized(ctx, store.AICloudAuthorizedParams{TenantID: actor.TenantID, ConfigurationDigest: cfg.ConfigurationDigest, ScopeHash: AICloudScopeHash})
	}
	return s, err
}
func (r *aiHealthRepository) ReadHealth(ctx context.Context, actor ProbeActor) (s AIHealthSnapshot, err error) {
	err = r.transaction(ctx, actor, func(ctx context.Context, q *store.Queries) error {
		var e error
		s, e = healthSnapshot(ctx, q, actor)
		return e
	})
	return
}
func (r *aiHealthRepository) CompleteHealth(ctx context.Context, actor ProbeActor, snapshot AIHealthSnapshot, candidate bool, outcome string) error {
	return r.transaction(ctx, actor, func(ctx context.Context, q *store.Queries) error {
		current, err := healthSnapshot(ctx, q, actor)
		if err != nil {
			return err
		}
		// Compare configuration version, all credential/governance inputs and current
		// consent. A candidate using a reused key has the same protection.
		if !reflect.DeepEqual(current, snapshot) {
			return ErrProbeChanged
		}
		if !candidate && (!current.Present || !current.Configuration.Enabled || (current.Configuration.Provider != "ollama" && !current.CloudAuthorized)) {
			return ErrProbeForbidden
		}
		return probeAudit(ctx, q, actor, "ai.health", "tenant", outcome)
	})
}
func probeAudit(ctx context.Context, q *store.Queries, actor ProbeActor, action, target, outcome string) error {
	detail, _ := json.Marshal(map[string]string{"outcome": outcome})
	_, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: actor.TenantID, Valid: true}, ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, Action: action, TargetType: pgtype.Text{String: target, Valid: true}, Detail: detail})
	return err
}
