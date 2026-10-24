package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestAuditResponseDetailIsBoundedJSONObject(t *testing.T) {
	for _, raw := range []string{`{"status":200,"outcome":"succeeded","phase":"transaction","private_payload":"DO_NOT_RETURN"}`, `invalid DO_NOT_RETURN`, `["DO_NOT_RETURN"]`, `"DO_NOT_RETURN"`, `{"status":99999999,"private_payload":"DO_NOT_RETURN"}`} {
		result := auditResponses([]store.AuditLogs{{ID: 1, Detail: []byte(raw)}})
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "DO_NOT_RETURN") || strings.Contains(string(encoded), "private_payload") {
			t.Fatalf("unexpected audit data%s", encoded)
		}
		var decoded []map[string]any
		if err = json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(raw, `{"status":200`) {
			detail, ok := decoded[0]["detail"].(map[string]any)
			if !ok || detail["status"] != float64(200) || detail["outcome"] != "succeeded" || detail["phase"] != "transaction" {
				t.Fatalf("non-object detail%s", encoded)
			}
		} else if decoded[0]["detail"] != nil {
			t.Fatalf("invalid detail did not fail safely%s", encoded)
		}
	}
}

func TestTenantAndPlatformAuditHTTPReturnOutcomeObjects(t *testing.T) {
	p, app := authIntegrationDatabase(t)
	ctx := t.Context()
	q := store.New(p.Pool)
	var brand, tenant, user int64
	for _, entry := range []struct {
		sql    string
		target *int64
	}{{"INSERT INTO brands(slug,name) VALUES('audit-json','Audit') RETURNING id", &brand}, {"INSERT INTO users(email,password_hash) VALUES('audit-json@example.test','unused') RETURNING id", &user}} {
		if err := p.QueryRow(ctx, entry.sql).Scan(entry.target); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,owner_user_id) VALUES($1,'Audit','audit-json',$2) RETURNING id", brand, user).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, "INSERT INTO user_tenant_roles(user_id,tenant_id,role) VALUES($1,$2,'owner')", user, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateAuditLog(ctx, store.CreateAuditLogParams{TenantID: pgtype.Int8{Int64: tenant, Valid: true}, ActorUserID: pgtype.Int8{Int64: user, Valid: true}, Action: "VERIFY billing.calendar", TargetType: pgtype.Text{String: "tenants", Valid: true}, TargetID: pgtype.Text{String: "fixture", Valid: true}, Detail: []byte(`{"status":200,"outcome":"succeeded","phase":"transaction","private_payload":"DO_NOT_RETURN"}`)}); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) })
	r.GET("/audit", middleware.RequireTenantTx(app, zap.NewNop()), NewAuditHandler(store.New(app.Pool)).List)
	r.GET("/platform/audit", NewPlatformHandler(q).AuditLogs)
	for _, path := range []string{"/audit", "/platform/audit"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
		if w.Code != 200 {
			t.Fatalf("audit%s HTTP%d %s", path, w.Code, w.Body)
		}
		var response struct {
			Logs []struct {
				Action string         `json:"action"`
				Detail map[string]any `json:"detail"`
			} `json:"logs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("audit%s detail not JSON object: %v", path, err)
		}
		found := false
		for _, row := range response.Logs {
			if row.Action == "VERIFY billing.calendar" {
				found = true
				if row.Detail["status"] != float64(200) || row.Detail["outcome"] != "succeeded" || row.Detail["phase"] != "transaction" {
					t.Fatalf("audit%s missing facts%v", path, row.Detail)
				}
			}
		}
		if !found || strings.Contains(w.Body.String(), "DO_NOT_RETURN") || strings.Contains(w.Body.String(), "private_payload") {
			t.Fatalf("audit%s exposed extras/lost row%s", path, w.Body)
		}
	}
}
