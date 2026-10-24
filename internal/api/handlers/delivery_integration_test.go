package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

type fixedQueueReader struct {
	Snapshot agentd.QueueSnapshot
	Err      error
}

func (r fixedQueueReader) QueueSnapshot(context.Context) (agentd.QueueSnapshot, error) {
	return r.Snapshot, r.Err
}

func TestQueueTenantScopeUsesAuthenticatedContextAndRedactsRecipients(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	scalar := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('queue','Queue') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('queue-owner@example.test','hash') RETURNING id")
	a := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'A','a','active') RETURNING id", brand)
	b := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'B','b','active') RETURNING id", brand)
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, a); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		tenant               int64
		fqdn, address, local string
	}{{a, "a.example.test", "a@a.example.test", "a"}, {b, "b.example.test", "b@b.example.test", "b"}} {
		domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,verification_token) VALUES($1,$2,'active',now(),'owned') RETURNING id", seed.tenant, seed.fqdn)
		if _, err := platform.Exec(ctx, "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status) VALUES($1,$2,$3,$4,'hash',1024,'active')", seed.tenant, domain, seed.local, seed.address); err != nil {
			t.Fatal(err)
		}
	}
	q := store.New(platform.Pool)
	if err := q.PlatformEnsureCollectorCheckpoint(ctx, "node"); err != nil {
		t.Fatal(err)
	}
	arrival := time.Now().UTC().Truncate(time.Second)
	if err := q.PlatformPutDeliveryQueueContext(ctx, store.PlatformPutDeliveryQueueContextParams{Stream: "node", Instance: "node/postfix", QueueID: "OWNED", LifecycleID: "life", AuthenticatedMailbox: "a@a.example.test", StartedAt: pgtype.Timestamptz{Time: arrival, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	reader := fixedQueueReader{Snapshot: agentd.QueueSnapshot{ObservedAt: arrival, Entries: []agentd.QueueEntry{
		{QueueID: "OWNED", ArrivalTime: arrival.Unix(), Sender: "a@a.example.test", Recipients: []agentd.QueueRecipient{{Address: "external@remote.test"}}},
		{QueueID: "FORGED", ArrivalTime: arrival.Unix(), Sender: "a@a.example.test", Recipients: []agentd.QueueRecipient{{Address: "b@b.example.test"}}},
		{QueueID: "LOCAL", ArrivalTime: arrival.Unix(), Sender: "outside@remote.test", Recipients: []agentd.QueueRecipient{{Address: "a@a.example.test"}, {Address: "b@b.example.test"}}},
	}}}
	handler := &DeliveryHandler{Store: store.New(app.Pool), Platform: q, Queue: reader, Stream: "node"}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, a); c.Set(middleware.ContextUserID, user) })
	router.Use(middleware.RequireTenantTx(app, zap.NewNop()))
	router.GET("/queue", handler.QueueForTenant)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequestWithContext(ctx, http.MethodGet, "/queue", nil))
	body := recorder.Body.String()
	if recorder.Code != 200 || !strings.Contains(body, "OWNED") || !strings.Contains(body, "LOCAL") || strings.Contains(body, "FORGED") || strings.Contains(body, "b@b.example.test") {
		t.Fatalf("queue leaked or hid tenant metadata: %d %s", recorder.Code, body)
	}
	// A recycled queue id must not inherit the old authenticated tenant.
	reader.Snapshot.Entries = reader.Snapshot.Entries[:1]
	reader.Snapshot.Entries[0].ArrivalTime = arrival.Add(time.Hour).Unix()
	handler.Queue = reader
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequestWithContext(ctx, http.MethodGet, "/queue", nil))
	if recorder.Code != 200 || strings.Contains(recorder.Body.String(), "OWNED") {
		t.Fatalf("recycled queue id inherited stale auth: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestDeliveryHealthUsesFinalRecipientsFreshnessAndExcludesFutureUnknown(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := platform.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name) VALUES('delivery-health','Health') RETURNING id")
	user := scalar("INSERT INTO users(email,password_hash) VALUES('health@example.test','hash') RETURNING id")
	tenant := scalar("INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Health','delivery-health','active') RETURNING id", brand)
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, tenant); err != nil {
		t.Fatal(err)
	}
	q := store.New(platform.Pool)
	now := time.Now().UTC()
	events := []struct {
		life, status string
		when         time.Time
		known        bool
	}{{"one", "deferred", now.Add(-2 * time.Minute), true}, {"one", "sent", now.Add(-time.Minute), true}, {"two", "bounced", now.Add(-time.Minute), true}, {"three", "expired", now.Add(-time.Minute), true}, {"four", "deferred", now.Add(-time.Minute), true}, {"future", "sent", now.Add(time.Hour), true}, {"unknown", "sent", now.Add(-time.Minute), false}}
	for index, event := range events {
		_, err := q.PlatformRecordCollectedDeliveryEvent(ctx, store.PlatformRecordCollectedDeliveryEventParams{TenantID: pgtype.Int8{Int64: tenant, Valid: event.known}, Instance: "health/postfix", QueueID: event.life, Recipient: pgtype.Text{String: "recipient@external.test", Valid: true}, Status: event.status, OccurredAt: pgtype.Timestamptz{Time: event.when, Valid: true}, SourceEventID: fmt.Sprintf("health-%d", index), LifecycleID: event.life})
		if err != nil {
			t.Fatal(err)
		}
	}
	h := &DeliveryHandler{Store: store.New(app.Pool), Platform: q, Stream: "health"}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) })
	router.Use(middleware.RequireTenantTx(app, zap.NewNop()))
	router.GET("/health", h.Health)
	request := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(ctx, "GET", path, nil))
		return response
	}
	response := request("/health?window=24h")
	var result struct {
		Terminal int64            `json:"terminal_recipients"`
		Accepted int64            `json:"accepted_recipients"`
		Rate     *float64         `json:"acceptance_rate"`
		Fresh    bool             `json:"fresh"`
		Observed *time.Time       `json:"observed_at"`
		Outcomes map[string]int64 `json:"recipient_outcomes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || result.Terminal != 3 || result.Accepted != 1 || result.Rate == nil || math.Abs(*result.Rate-1.0/3.0) > 0.00001 || result.Fresh || result.Observed != nil || result.Outcomes["deferred"] != 1 {
		t.Fatalf("misleading recipient health: %d %s %v", response.Code, response.Body.String(), err)
	}
	if err := q.PlatformEnsureCollectorCheckpoint(ctx, "health"); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "UPDATE collector_checkpoints SET observed_at=now() WHERE stream='health'"); err != nil {
		t.Fatal(err)
	}
	response = request("/health")
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !result.Fresh || result.Observed == nil {
		t.Fatalf("fresh source not shown: %s %v", response.Body.String(), err)
	}
	if response = request("/health?window=invalid"); response.Code != 400 {
		t.Fatal("unbounded health window accepted")
	}
}
