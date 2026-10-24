package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/store"
)

func TestPublicPricingUsesIndependentCatalogWithoutPrivateMetadata(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `INSERT INTO plans(code,name,description,price_cents,currency,quota_bytes,mailbox_limit,domain_limit,daily_send_limit,features) VALUES('public','{"en":"Public"}','{}',10000,'CNY',1000,5,2,100,'{"operator_note":"private-feature-metadata"}'); INSERT INTO plan_prices(plan_code,currency,billing_cycle,amount_cents) VALUES('public','CNY','monthly',10000),('public','CNY','quarterly',27500),('public','CNY','annual',90000),('public','USD','monthly',1500);`); err != nil {
		t.Fatal(err)
	}
	h := &PublicPricingHandler{Store: store.New(a)}
	r := gin.New()
	r.GET("/public/plans", h.Plans)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/public/plans", nil))
	if w.Code != 200 {
		t.Fatalf("public catalog %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Plans []struct {
			Code   string                    `json:"code"`
			Prices []store.ListPlanPricesRow `json:"prices"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, plan := range result.Plans {
		if plan.Code == "free" {
			t.Fatal("platform gift plan became a public offer")
		}
		if plan.Code != "public" {
			continue
		}
		found = true
		prices := map[string]int64{}
		for _, price := range plan.Prices {
			prices[price.Currency+":"+price.BillingCycle] = price.AmountCents
		}
		if prices["CNY:quarterly"] != 27500 || prices["CNY:annual"] != 90000 || prices["USD:monthly"] != 1500 {
			t.Fatalf("independent prices lost: %v", prices)
		}
	}
	if !found || strings.Contains(w.Body.String(), "private-feature-metadata") || strings.Contains(w.Body.String(), "price_version_secret") {
		t.Fatalf("invalid public DTO: %s", w.Body.String())
	}
	if _, err := store.New(p).PlatformBillingLegacyIssues(ctx); err != nil {
		t.Fatalf("legacy inventory query: %v", err)
	}
}
