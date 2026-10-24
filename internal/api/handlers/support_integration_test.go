package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestSupportAuthorIsolationStaffReplyAndNotification(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, tenant, author, other, staff int64
	for _, seed := range []struct {
		sql  string
		dest *int64
	}{
		{"INSERT INTO brands(slug,name) VALUES('support','Support') RETURNING id", &brand},
		{"INSERT INTO users(email,password_hash) VALUES('author@support.test','unused') RETURNING id", &author},
		{"INSERT INTO users(email,password_hash) VALUES('other@support.test','unused') RETURNING id", &other},
		{"INSERT INTO users(email,password_hash) VALUES('staff@support.test','unused') RETURNING id", &staff},
	} {
		if err := p.QueryRow(ctx, seed.sql).Scan(seed.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Support','support','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$3,'member'),($2,$3,'member')", author, other, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO platform_admins(user_id) VALUES($1)", staff); err != nil {
		t.Fatal(err)
	}
	h := &SupportHandler{Store: store.New(a.Pool), Platform: store.New(p.Pool), PlatformPool: p}
	route := func(uid int64) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set(middleware.ContextUserID, uid)
			c.Set(middleware.ContextTenantID, tenant)
			c.Next()
		})
		g := r.Group("")
		g.Use(middleware.RequireTenantTx(a, zap.NewNop()))
		g.POST("/tickets", h.Create)
		g.GET("/tickets/:ticket_id", h.Get)
		g.POST("/tickets/:ticket_id/messages", h.Reply)
		return r
	}
	call := func(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	response := call(route(author), http.MethodPost, "/tickets", `{"subject":"Help","body":"<script>alert(1)</script>","is_staff":true,"opened_by":999}`)
	if response.Code != 201 {
		t.Fatalf("create %d %s", response.Code, response.Body)
	}
	var result struct {
		Ticket store.Tickets `json:"ticket"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	id := result.Ticket.ID
	response = call(route(other), http.MethodGet, fmt.Sprintf("/tickets/%d", id), "")
	if response.Code != 404 {
		t.Fatalf("same-tenant other author saw ticket: %d %s", response.Code, response.Body)
	}
	response = call(route(other), http.MethodPost, fmt.Sprintf("/tickets/%d/messages", id), `{"body":"intruder"}`)
	if response.Code < 400 {
		t.Fatalf("other author reply accepted: %d", response.Code)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextUserID, staff) })
	r.POST("/tenants/:id/tickets/:ticket_id/messages", h.PlatformReply)
	response = call(r, http.MethodPost, fmt.Sprintf("/tenants/%d/tickets/%d/messages", tenant, id), `{"body":"We are investigating."}`)
	if response.Code != 201 {
		t.Fatalf("staff reply: %d %s", response.Code, response.Body)
	}
	response = call(route(author), http.MethodGet, fmt.Sprintf("/tickets/%d", id), "")
	if response.Code != 200 {
		t.Fatalf("get %d %s", response.Code, response.Body)
	}
	var details struct {
		Messages []store.TicketMessages `json:"messages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	if len(details.Messages) != 2 || details.Messages[0].IsStaff || !details.Messages[1].IsStaff || details.Messages[0].AuthorID != author {
		t.Fatalf("forged staff/author or missing reply: %+v", details.Messages)
	}
	var count int
	if err := p.QueryRow(ctx, "SELECT count(*) FROM notification_outbox WHERE tenant_id=$1 AND user_id=$2 AND kind='support_reply' AND state='delivered'", tenant, author).Scan(&count); err != nil || count != 1 {
		t.Fatalf("notification count %d: %v", count, err)
	}
	tx, err := a.UserTx(ctx, tenant, author)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO ticket_attachments(tenant_id,message_id,filename,content_type,content) VALUES($1,$2,'fake.txt','text/plain','fake')", tenant, details.Messages[1].ID)
	if err == nil {
		t.Fatal("author attached content to staff message")
	}
}

func TestAnnouncementsBrandDraftAndTimeFiltering(t *testing.T) {
	p, a := authIntegrationDatabase(t)
	ctx := context.Background()
	var brand, otherBrand, tenant, uid int64
	for i, dest := range []*int64{&brand, &otherBrand} {
		if err := p.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES($1,'Support') RETURNING id", fmt.Sprintf("brand%d", i)).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Support','support','active') RETURNING id", brand).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('reader@support.test','unused') RETURNING id").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	q := store.New(p.Pool)
	past := pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}
	future := pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}
	for _, tc := range []struct {
		brand        pgtype.Int8
		state        string
		starts, ends pgtype.Timestamptz
	}{{pgtype.Int8{}, "published", past, pgtype.Timestamptz{}}, {pgtype.Int8{Int64: brand, Valid: true}, "published", past, pgtype.Timestamptz{}}, {pgtype.Int8{Int64: otherBrand, Valid: true}, "published", past, pgtype.Timestamptz{}}, {pgtype.Int8{}, "draft", past, pgtype.Timestamptz{}}, {pgtype.Int8{}, "withdrawn", past, pgtype.Timestamptz{}}, {pgtype.Int8{}, "published", future, pgtype.Timestamptz{}}, {pgtype.Int8{}, "published", pgtype.Timestamptz{Time: time.Now().Add(-2 * time.Hour), Valid: true}, past}} {
		_, err := q.PlatformCreateAnnouncement(ctx, store.PlatformCreateAnnouncementParams{BrandID: tc.brand, Title: []byte(`{"en":"Title","zh-CN":"标题"}`), Body: []byte(`{"en":"Body","zh-CN":"正文"}`), State: tc.state, PublishedAt: tc.starts, EndsAt: tc.ends})
		if err != nil {
			t.Fatal(err)
		}
	}
	tx, err := a.UserTx(ctx, tenant, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	rows, err := store.New(tx).ListVisibleAnnouncements(ctx, tenant)
	if err != nil || len(rows) != 2 {
		t.Fatalf("visible announcements %d err=%v", len(rows), err)
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM announcements").Scan(&count); err != nil || count != 2 {
		t.Fatalf("direct RLS exposed private announcements %d %v", count, err)
	}
}

func TestSupportAttachmentValidation(t *testing.T) {
	if validSupportReply(supportReply{Body: "x", Attachments: []supportAttachment{{Filename: "bad.html", ContentType: "image/png", Content: []byte("<script>x</script>")}}}) {
		t.Fatal("mismatched content type accepted")
	}
	if validSupportReply(supportReply{Body: "x", Attachments: []supportAttachment{{Filename: "../bad.txt", ContentType: "text/plain", Content: []byte("text")}}}) {
		t.Fatal("unsafe filename accepted")
	}
	if !validSupportReply(supportReply{Body: "<b>inert plain text</b>", Attachments: []supportAttachment{{Filename: "proof.txt", ContentType: "text/plain", Content: []byte("proof")}}}) {
		t.Fatal("valid inert content rejected")
	}
}
