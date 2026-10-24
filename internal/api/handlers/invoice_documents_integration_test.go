package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/billing"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestInvoiceDocumentCaptureFreezeAndDownloadIsolation(t *testing.T) {
	p, principal := mailboxAIFixture(t, 1)
	ctx := context.Background()
	q := store.New(p)
	var user int64
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('invoice@example.test','x') RETURNING id").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, principal.TenantID); err != nil {
		t.Fatal(err)
	}
	seller := `{"party":{"name":"Original seller","tax_id":"SENSITIVE1234"},"tax":{"configured":true,"included_in_total":true,"rate_basis_points":600},"locale":"zh-CN"}`
	if err := q.PlatformSetDocumentProfile(ctx, store.PlatformSetDocumentProfileParams{Scope: "platform", ScopeID: 0, Value: []byte(seller)}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CaptureInvoiceDocumentSnapshot(ctx, q, principal.TenantID, 10600, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	var doc billing.DocumentSnapshot
	if err := json.Unmarshal(snapshot, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Seller.Name != "Original seller" || doc.BuyerSource != "workspace_name" || *doc.Amounts.TaxCents != 600 {
		t.Fatalf("bad snapshot %+v", doc)
	}
	var invoice int64
	if err := p.QueryRow(ctx, "INSERT INTO invoices(tenant_id,number,period_start,period_end,amount_cents,currency,status,issued_at) VALUES($1,'TEST-PDF',now(),now()+interval '1 month',10600,'CNY','paid',now()) RETURNING id", principal.TenantID).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if n, err := q.PlatformFreezeInvoiceDocumentSnapshot(ctx, store.PlatformFreezeInvoiceDocumentSnapshotParams{TenantID: principal.TenantID, ID: invoice, DocumentSnapshot: snapshot}); err != nil || n != 1 {
		t.Fatalf("freeze %d %v", n, err)
	}
	if _, err := p.Exec(ctx, "UPDATE invoices SET amount_cents=1 WHERE id=$1", invoice); err == nil {
		t.Fatal("frozen monetary history changed")
	}
	if _, err := p.Exec(ctx, "UPDATE invoices SET document_snapshot='{}' WHERE id=$1", invoice); err == nil {
		t.Fatal("frozen party history cleared")
	}
	seller = strings.ReplaceAll(seller, "Original seller", "Changed seller")
	if err := q.PlatformSetDocumentProfile(ctx, store.PlatformSetDocumentProfileParams{Scope: "platform", ScopeID: 0, Value: []byte(seller)}); err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenApp(ctx, p.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h := &BillingHandler{Store: q}
	makeEngine := func(tenant int64, role string) *gin.Engine {
		engine := gin.New()
		engine.GET("/:id", func(c *gin.Context) {
			tx, err := app.UserTx(c, tenant, user)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(c) }()
			c.Set(middleware.ContextTenantID, tenant)
			c.Set(middleware.ContextUserID, user)
			c.Set(middleware.ContextRole, role)
			c.Set(middleware.ContextQueries, store.New(tx))
			c.Next()
			if c.Writer.Status() < 400 {
				if err := tx.Commit(c); err != nil {
					t.Error(err)
				}
			}
		}, h.invoiceDocumentPDF)
		return engine
	}
	engine := makeEngine(principal.TenantID, "owner")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, "GET", "/"+strconv.FormatInt(invoice, 10), nil))
	if response.Code != 200 || !strings.HasPrefix(response.Body.String(), "%PDF-") {
		t.Fatalf("download %d %s", response.Code, response.Body.String())
	}
	var count int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id=$1 AND action='READ invoice.pdf'", principal.TenantID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("mandatory READ audit missing %d %v", count, err)
	}
	// Authoritative tenant predicate must hold even with the privileged fixture pool.
	other := makeEngine(principal.TenantID+99999, "owner")
	response = httptest.NewRecorder()
	other.ServeHTTP(response, httptest.NewRequestWithContext(ctx, "GET", "/"+strconv.FormatInt(invoice, 10), nil))
	if response.Code != 404 {
		t.Fatalf("cross-tenant download: %d", response.Code)
	}
	// A finance-incapable panel role is rejected even before a row can render.
	member := makeEngine(principal.TenantID, "member")
	response = httptest.NewRecorder()
	member.ServeHTTP(response, httptest.NewRequestWithContext(ctx, "GET", "/"+strconv.FormatInt(invoice, 10), nil))
	if response.Code != 403 {
		t.Fatalf("member PDF access: %d", response.Code)
	}
	// Audit storage failure must never release a PDF attachment.
	if _, err := p.Exec(ctx, "REVOKE EXECUTE ON FUNCTION urmail_record_audit(bigint,bigint,inet,text,text,integer) FROM urmail_app"); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, "GET", "/"+strconv.FormatInt(invoice, 10), nil))
	if response.Code != 500 || strings.HasPrefix(response.Body.String(), "%PDF-") || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("audit failure released PDF: %d", response.Code)
	}

}

func TestInvoiceProfileMaskPatchAndRLS(t *testing.T) {
	p, principal := mailboxAIFixture(t, 1)
	ctx := context.Background()
	q := store.New(p)
	var owner, member int64
	for _, entry := range []struct {
		email, role string
		id          *int64
	}{{"billing-owner@example.test", "owner", &owner}, {"billing-member@example.test", "member", &member}} {
		if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id", entry.email).Scan(entry.id); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,$3)", *entry.id, principal.TenantID, entry.role); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte(`{"party":{"name":"Buyer","tax_id":"SECRETTAX1234","bank_account":"PRIVATE1234"}}`)
	if err := q.UpsertInvoiceBuyerProfile(ctx, store.UpsertInvoiceBuyerProfileParams{TenantID: principal.TenantID, Value: raw}); err != nil {
		t.Fatal(err)
	}
	if err := q.PlatformSetDocumentProfile(ctx, store.PlatformSetDocumentProfileParams{Scope: "platform", ScopeID: 0, Value: raw}); err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenApp(ctx, p.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h := &InvoiceProfileHandler{Store: q}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		tx, err := app.UserTx(c, principal.TenantID, owner)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(c) }()
		c.Set(middleware.ContextTenantID, principal.TenantID)
		c.Set(middleware.ContextUserID, owner)
		c.Set(middleware.ContextRole, "owner")
		c.Set(middleware.ContextQueries, store.New(tx))
		c.Next()
		if c.Writer.Status() < 400 {
			if err := tx.Commit(c); err != nil {
				t.Error(err)
			}
		}
	})
	engine.GET("/", h.GetBuyer)
	engine.PUT("/", h.PutBuyer)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequestWithContext(ctx, "GET", "/", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "SECRETTAX") || !strings.Contains(response.Body.String(), "1234") {
		t.Fatalf("mask failed %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequestWithContext(ctx, "PUT", "/", strings.NewReader(`{"party":{"name":"Changed buyer"}}`))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(response, request)
	if response.Code != 204 {
		t.Fatalf("patch %d %s", response.Code, response.Body.String())
	}
	saved, err := q.PlatformGetDocumentProfile(ctx, store.PlatformGetDocumentProfileParams{Scope: "tenant", ScopeID: principal.TenantID})
	if err != nil || !strings.Contains(string(saved), "SECRETTAX1234") {
		t.Fatalf("omitted sensitive field lost: %s %v", saved, err)
	}
	for _, user := range []int64{owner, member} {
		tx, err := app.UserTx(ctx, principal.TenantID, user)
		if err != nil {
			t.Fatal(err)
		}
		tq := store.New(tx)
		if _, err := tq.PlatformGetDocumentProfile(ctx, store.PlatformGetDocumentProfileParams{Scope: "platform", ScopeID: 0}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("platform private identity leaked to tenant user: %v", err)
		}
		if user == member {
			if _, err := tq.PlatformGetDocumentProfile(ctx, store.PlatformGetDocumentProfileParams{Scope: "tenant", ScopeID: principal.TenantID}); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("buyer private identity leaked to member: %v", err)
			}
		}
		_ = tx.Rollback(ctx)
	}
}

func TestProductionQuoteFreezesPrivilegedSellerAndBuyerProfiles(t *testing.T) {
	s := newBillingScenario(t)
	ctx := t.Context()
	q := store.New(s.p)
	seller := `{"party":{"name":"Seller at quote","tax_id":"SELLER-PRIVATE-123"},"tax":{"configured":true,"included_in_total":true,"rate_basis_points":600},"locale":"zh-CN"}`
	buyer := `{"party":{"name":"Buyer at quote","tax_id":"BUYER-PRIVATE-456"},"locale":"zh-CN"}`
	for _, profile := range []store.PlatformSetDocumentProfileParams{{Scope: "platform", ScopeID: 0, Value: []byte(seller)}, {Scope: "tenant", ScopeID: s.tid, Value: []byte(buyer)}} {
		if err := q.PlatformSetDocumentProfile(ctx, profile); err != nil {
			t.Fatal(err)
		}
	}
	response := s.post(t, "/quote", `{"plan_code":"high","billing_cycle":"monthly"}`, "")
	if response.Code != 200 {
		t.Fatalf("quote: %d %s", response.Code, response.Body.String())
	}
	var quote billing.Quote
	if err := json.Unmarshal(response.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	saved, err := q.GetBillingQuote(ctx, store.GetBillingQuoteParams{TenantID: s.tid, Digest: quote.Digest})
	if err != nil {
		t.Fatal(err)
	}
	var captured billing.DocumentSnapshot
	if err = json.Unmarshal(saved.DocumentSnapshot, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Seller.Name != "Seller at quote" || captured.Seller.TaxID != "SELLER-PRIVATE-123" || captured.Buyer.Name != "Buyer at quote" || captured.Buyer.TaxID != "BUYER-PRIVATE-456" || captured.Amounts.TaxCents == nil {
		t.Fatalf("production quote silently omitted privileged profiles: %+v", captured)
	}
	if err = q.PlatformSetDocumentProfile(ctx, store.PlatformSetDocumentProfileParams{Scope: "platform", ScopeID: 0, Value: []byte(strings.ReplaceAll(seller, "Seller at quote", "Seller after quote"))}); err != nil {
		t.Fatal(err)
	}
	if err = q.PlatformSetDocumentProfile(ctx, store.PlatformSetDocumentProfileParams{Scope: "tenant", ScopeID: s.tid, Value: []byte(strings.ReplaceAll(buyer, "Buyer at quote", "Buyer after quote"))}); err != nil {
		t.Fatal(err)
	}
	checkout := s.post(t, "/checkout", `{"quote":`+response.Body.String()+`}`, "frozen-document-quote")
	if checkout.Code != 201 {
		t.Fatalf("checkout: %s", checkout.Body.String())
	}
	var order struct {
		Order store.Orders `json:"order"`
	}
	if err = json.Unmarshal(checkout.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	if err = SettleOrder(ctx, s.p, order.Order.ID); err != nil {
		t.Fatal(err)
	}
	var snapshot []byte
	if err = s.p.QueryRow(ctx, "SELECT document_snapshot FROM invoices WHERE tenant_id=$1 AND order_id=$2", s.tid, order.Order.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var frozen billing.DocumentSnapshot
	if err = json.Unmarshal(snapshot, &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Seller.Name != captured.Seller.Name || frozen.Buyer.Name != captured.Buyer.Name || frozen.Seller.TaxID != captured.Seller.TaxID || frozen.Provenance != "quote" {
		t.Fatalf("settlement rewrote quote history: %+v", frozen)
	}
}
