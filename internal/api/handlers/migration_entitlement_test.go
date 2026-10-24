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
