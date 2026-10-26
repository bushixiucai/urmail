package webmail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestVaultExpiryCapacityAndMailboxRevocation(t *testing.T) {
	capacityCount := func() float64 {
		t.Helper()
		metrics, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, metric := range metrics {
			if metric.GetName() == "urmail_webmail_vault_capacity_rejections_total" {
				if len(metric.Metric) != 1 || len(metric.Metric[0].Label) != 0 {
					t.Fatal("vault capacity counter must have no identity labels")
				}
				return metric.Metric[0].GetCounter().GetValue()
			}
		}
		t.Fatal("missing vault capacity counter")
		return 0
	}
	before := capacityCount()
	now := time.Now()
	v := NewMemoryVault()
	v.now = func() time.Time { return now }
	v.maxEntries = 3
	v.maxMailbox = 2
	put := func(token string, mailbox int64) error {
		return v.PutSession(Session{TokenHash: token, TenantID: 1, MailboxID: mailbox, ExpiresAt: now.Add(time.Minute)}, "password")
	}
	if put("a", 1) != nil || put("b", 1) != nil || put("c", 2) != nil {
		t.Fatal("initial capacity")
	}
	if got := capacityCount(); got != before {
		t.Fatalf("successful vault admission increased capacity rejections: before=%v got=%v", before, got)
	}
	if put("d", 1) == nil || put("e", 3) == nil {
		t.Fatal("capacity did not reject")
	}
	if got := capacityCount(); got != before+2 {
		t.Fatalf("vault capacity rejections: want=%v got=%v", before+2, got)
	}
	for id := int64(1); id <= 3; id++ {
		v.PutSchedule(id, "password")
	}
	if got := capacityCount(); got != before+2 {
		t.Fatal("successful schedule cache admission increased capacity rejections")
	}
	v.PutSchedule(4, "password")
	if got := capacityCount(); got != before+3 {
		t.Fatal("schedule cache capacity rejection was not counted")
	}
	if _, ok := v.Get("a"); !ok {
		t.Fatal("capacity rejection evicted active credential")
	}
	v.DeleteMailbox(1, 1)
	if _, ok := v.Get("b"); ok {
		t.Fatal("mailbox revoke retained password")
	}
	now = now.Add(time.Minute)
	v.Sweep(context.Background(), nil)
	if len(v.byID) != 0 {
		t.Fatal("unvisited expiry retained passwords")
	}
}

func TestVaultSweepClearsExternallyRevokedAndFailedLookups(t *testing.T) {
	v := NewMemoryVault()
	expiry := time.Now().Add(time.Hour)
	for _, token := range []string{"valid", "revoked"} {
		if err := v.PutSession(Session{TokenHash: token, TenantID: 1, MailboxID: 1, ExpiresAt: expiry}, "password"); err != nil {
			t.Fatal(err)
		}
	}
	v.Sweep(context.Background(), func(_ context.Context, token string) (Session, error) {
		if token == "valid" {
			return Session{TokenHash: token}, nil
		}
		return Session{}, errors.New("revoked")
	})
	if _, ok := v.Get("valid"); !ok {
		t.Fatal("live session disappeared")
	}
	if _, ok := v.Get("revoked"); ok {
		t.Fatal("revoked secret retained")
	}
	v.Sweep(context.Background(), func(context.Context, string) (Session, error) { return Session{}, errors.New("database unavailable") })
	if len(v.byID) != 0 {
		t.Fatal("lookup failure retained secrets")
	}
}
func TestScheduledVaultCacheExpiresIndependently(t *testing.T) {
	now := time.Now()
	v := NewMemoryVault()
	v.now = func() time.Time { return now }
	v.PutSchedule(1, "password")
	now = now.Add(time.Minute)
	v.Sweep(context.Background(), nil)
	if _, ok := v.GetSchedule(1); ok {
		t.Fatal("expired schedule secret retained")
	}
}
