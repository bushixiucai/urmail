package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestAttributionCheckoutSettlementCommissionIsAtomicAndIdempotent(t *testing.T) {
	p, app := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tid, uid, channel int64
	for _, step := range []struct {
		sql    string
		args   []any
		target *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('distribution','Distribution') RETURNING id", nil, &brand},
		{"INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Distribution','distribution','active') RETURNING id", []any{&brand}, &tid},
		{"INSERT INTO users(email,password_hash) VALUES('distribution@example.test','x') RETURNING id", nil, &uid},
		{"INSERT INTO channels(code,name,commission_bps) VALUES('distribution','Distribution',1000) RETURNING id", nil, &channel},
	} {
		args := []any{}
		for _, arg := range step.args {
			if ptr, ok := arg.(*int64); ok {
				args = append(args, *ptr)
			} else {
				args = append(args, arg)
			}
		}
		if err := p.QueryRow(ctx, step.sql, args...).Scan(step.target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", uid, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO plans(code,name,price_cents,quota_bytes,mailbox_limit,domain_limit,daily_send_limit) VALUES('distribution','{}',10000,1073741824,5,2,100)"); err != nil {
		t.Fatal(err)
	}
	q := store.New(p.Pool)
	if _, err := q.CreateChannelAttribution(ctx, store.CreateChannelAttributionParams{TenantID: tid, ChannelID: channel, RefCode: pgtype.Text{String: "campaign", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tid); c.Set(middleware.ContextUserID, uid) })
	engine.Use(middleware.RequireTenantTx(app, zap.NewNop()))
	engine.POST("/checkout", NewBillingHandler(store.New(app.Pool)).Checkout)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/checkout", strings.NewReader(`{"plan_code":"distribution"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("checkout: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Order struct {
			ID        int64 `json:"id"`
			ChannelID int64 `json:"channel_id"`
		} `json:"order"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Order.ChannelID != channel {
		t.Fatalf("attribution not captured: %s", response.Body.String())
	}
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := SettleOrder(ctx, p, payload.Order.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	var count, amount int64
	if err := p.QueryRow(ctx, "SELECT count(*),COALESCE(sum(amount_cents),0) FROM commissions WHERE tenant_id=$1 AND order_id=$2", tid, payload.Order.ID).Scan(&count, &amount); err != nil || count != 1 || amount != 1000 {
		t.Fatalf("commissions count=%d amount=%d err=%v", count, amount, err)
	}
	if err := p.QueryRow(ctx, "SELECT count(*) FROM invoices WHERE tenant_id=$1", tid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invoice count=%d err=%v", count, err)
	}
}
