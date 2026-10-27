package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

// MKT-001: one invalid row must not fail the whole import with a generic
// error, and it must never leave a partially imported list. The batch is
// all-or-nothing: every rejected row is reported with its index and reason,
// and nothing is persisted for a rejected batch.
func TestImportContactsRejectsBatchWithPerRowReasons(t *testing.T) {
	platform, app := authIntegrationDatabase(t)
	ctx := context.Background()
	var brandID, tenantID, userID, listID int64
	if err := platform.QueryRow(ctx, "INSERT INTO brands(slug,name) VALUES('mkt-import','Mkt') RETURNING id").Scan(&brandID); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO tenants(brand_id,name,slug,status) VALUES($1,'Mkt','mkt-import','active') RETURNING id", brandID).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('mkt-import@example.test','unused') RETURNING id").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, "INSERT INTO user_tenant_roles VALUES($1,$2,'owner')", userID, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := platform.QueryRow(ctx, "INSERT INTO marketing_lists(tenant_id,name) VALUES($1,'Import') RETURNING id", tenantID).Scan(&listID); err != nil {
		t.Fatal(err)
	}

	h := &MarketingHandler{Store: store.New(app.Pool)}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, tenantID)
		c.Set(middleware.ContextUserID, userID)
	}, middleware.RequireTenantTx(app, zap.NewNop()))
	router.POST("/marketing/lists/:list_id/contacts", h.ImportContacts)
	path := "/marketing/lists/" + strconv.FormatInt(listID, 10) + "/contacts"
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}

	rejected := post(`{"contacts":[` +
		`{"email":"valid@example.test","consent_source":"csv-2026","opted_in":true},` +
		`{"email":"not-an-email","consent_source":"csv-2026","opted_in":true},` +
		`{"email":"trailing@newline.test\n","consent_source":"csv-2026","opted_in":true},` +
		`{"email":"no-consent@example.test","consent_source":"   ","opted_in":true},` +
		`{"email":"long@consent.test","consent_source":"` + strings.Repeat("x", 257) + `","opted_in":true}]}`)
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid batch status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Details []struct {
				Row    int    `json:"row"`
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rejected.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "CONTACTS_INVALID" {
		t.Fatalf("error code = %q, want CONTACTS_INVALID", payload.Error.Code)
	}
	want := []struct {
		row    int
		reason string
	}{{1, "invalid_email"}, {2, "invalid_email"}, {3, "missing_consent_source"}, {4, "consent_source_too_long"}}
	if len(payload.Error.Details) != len(want) {
		t.Fatalf("row errors = %+v, want %d entries", payload.Error.Details, len(want))
	}
	for index, entry := range payload.Error.Details {
		if entry.Row != want[index].row || entry.Reason != want[index].reason {
			t.Fatalf("row error %d = {%d %s}, want {%d %s}", index, entry.Row, entry.Reason, want[index].row, want[index].reason)
		}
	}
	var persisted int64
	if err := platform.QueryRow(ctx, "SELECT count(*) FROM marketing_contacts WHERE tenant_id=$1 AND list_id=$2", tenantID, listID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 0 {
		t.Fatalf("rejected batch left %d rows behind, want 0", persisted)
	}

	imported := post(`{"contacts":[` +
		`{"email":"Valid@Example.test","consent_source":"csv-2026","opted_in":true},` +
		`{"email":"second@example.test","consent_source":"event-desk","opted_in":false}]}`)
	if imported.Code != http.StatusCreated {
		t.Fatalf("valid batch status=%d body=%s", imported.Code, imported.Body.String())
	}
	var created struct {
		Imported int `json:"imported"`
	}
	if err := json.Unmarshal(imported.Body.Bytes(), &created); err != nil || created.Imported != 2 {
		t.Fatalf("imported=%+v err=%v", created, err)
	}
	rows, err := platform.Query(ctx, "SELECT email,opted_in FROM marketing_contacts WHERE tenant_id=$1 AND list_id=$2 ORDER BY email", tenantID, listID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var email string
		var opted bool
		if err := rows.Scan(&email, &opted); err != nil {
			t.Fatal(err)
		}
		seen[email] = opted
	}
	if len(seen) != 2 || !seen["valid@example.test"] || seen["second@example.test"] {
		t.Fatalf("stored contacts = %v, want lowercased valid@example.test and second@example.test", seen)
	}
}
