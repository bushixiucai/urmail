package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestServiceEntitlementDistinguishesNullAndInfinity(t *testing.T) {
	now := time.Now()
	sub := store.Subscriptions{EntitlementSource: billing.EntitlementMigration, MigrationManifestSha256: pgtype.Text{String: strings.Repeat("a", 64), Valid: true}, Status: "active", PeriodStart: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}}
	if !serviceEntitlement(sub).CurrentAt(now) {
		t.Fatal("valid preserved entitlement rejected")
	}
	sub.PeriodEnd = pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}
	if serviceEntitlement(sub).CurrentAt(now) {
		t.Fatal("infinite end accepted as SQL NULL")
	}
	sub.PeriodEnd = pgtype.Timestamptz{}
	sub.PeriodStart = pgtype.Timestamptz{InfinityModifier: pgtype.NegativeInfinity, Valid: true}
	if serviceEntitlement(sub).CurrentAt(now) {
		t.Fatal("infinite start accepted as finite proof")
	}
}

// serviceEntitlement preserves SQL NULL without accepting PostgreSQL infinity as an end.
func serviceEntitlement(sub store.Subscriptions) billing.ServiceEntitlement {
	e := billing.ServiceEntitlement{Source: sub.EntitlementSource, Status: sub.Status}
	if sub.MigrationManifestSha256.Valid {
		e.ManifestSHA256 = sub.MigrationManifestSha256.String
	}
	if sub.PeriodStart.Valid && sub.PeriodStart.InfinityModifier == pgtype.Finite {
		e.PeriodStart = sub.PeriodStart.Time
	}
	if sub.PeriodEnd.Valid {
		end := time.Time{}
		if sub.PeriodEnd.InfinityModifier == pgtype.Finite {
			end = sub.PeriodEnd.Time
		}
		e.PeriodEnd = &end
	}
	if sub.TrialEndsAt.Valid {
		end := time.Time{}
		if sub.TrialEndsAt.InfinityModifier == pgtype.Finite {
			end = sub.TrialEndsAt.Time
		}
		e.TrialEndsAt = &end
	}
	return e
}
