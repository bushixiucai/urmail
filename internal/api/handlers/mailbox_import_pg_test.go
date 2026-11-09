package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/testdb"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type importIMAPFixture struct {
	fakeIMAPClient
	mu                                 sync.Mutex
	messages                           map[uint32][]byte
	next, validity                     uint32
	appends                            int
	failBefore, failAfter, scanFailure bool
	hook                               func() error
}

func (c *importIMAPFixture) Close() error { return nil }
func (c *importIMAPFixture) GetThreadMembers(context.Context, string, uint32, webmail.ThreadMembersOpts) (webmail.ThreadMembersResult, error) {
	return webmail.ThreadMembersResult{}, nil
}
func (c *importIMAPFixture) FolderUIDValidity(context.Context, string) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.validity, nil
}
func (c *importIMAPFixture) FindImportCopies(_ context.Context, folder string, raw []byte, validity uint32) ([]webmail.DraftSaveResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scanFailure || validity != c.validity {
		return nil, errors.New("explicit incomplete inventory fixture")
	}
	results := []webmail.DraftSaveResult{}
	for uid, stored := range c.messages {
		if bytes.Equal(raw, stored) {
			results = append(results, webmail.DraftSaveResult{Folder: folder, UID: uid, UIDValidity: c.validity})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].UID < results[j].UID })
	return results, nil
}
func (c *importIMAPFixture) GetRawMessage(_ context.Context, _ string, uid uint32) ([]byte, uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, ok := c.messages[uid]
	if !ok {
		return nil, c.validity, webmail.ErrMessageNotFound
	}
	return append([]byte(nil), raw...), c.validity, nil
}
func (c *importIMAPFixture) ImportMessage(_ context.Context, folder string, raw []byte) (webmail.DraftSaveResult, error) {
	c.mu.Lock()
	hook := c.hook
	c.mu.Unlock()
	if hook != nil {
		if e := hook(); e != nil {
			return webmail.DraftSaveResult{}, e
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appends++
	if c.failBefore {
		return webmail.DraftSaveResult{}, errors.New("injected disconnect before APPEND acceptance")
	}
	c.next++
	uid := c.next
	c.messages[uid] = append([]byte(nil), raw...)
	if c.failAfter {
		return webmail.DraftSaveResult{}, errors.New("injected disconnect after durable APPEND")
	}
	return webmail.DraftSaveResult{Folder: folder, UID: uid, UIDValidity: c.validity}, nil
}

type importFixtureDialer struct{ clients map[string]*importIMAPFixture }

func (d importFixtureDialer) Dial(_ context.Context, address, _ string) (webmail.Client, error) {
	c, ok := d.clients[address]
	if !ok {
		return nil, webmail.ErrDialFailed
	}
	return c, nil
}

type importHTTPFixture struct {
	platform, app                 *db.Pool
	server                        *httptest.Server
	native                        *MailboxNativeHandler
	client                        *importIMAPFixture
	tenant, mailbox               int64
	token, bob, foreign, password string
}

func newImportHTTPFixture(t *testing.T) *importHTTPFixture {
	t.Helper()
	platform, app := testdb.OpenMigrated(t)
	f := &importHTTPFixture{platform: platform, app: app, password: "Synthetic-import-password!42"}
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var id int64
		if e := platform.QueryRow(t.Context(), sql, args...).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	brand := scalar("INSERT INTO brands(slug,name)VALUES('mail-import','Import')RETURNING id")
	f.tenant = scalar("INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'Import','mail-import','active')RETURNING id", brand)
	other := scalar("INSERT INTO tenants(brand_id,name,slug,status)VALUES($1,'Other','mail-import-other','active')RETURNING id", brand)
	domain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,provisioning_state,verification_token)VALUES($1,'mail-import.test','active',now(),'ready','fixture')RETURNING id", f.tenant)
	foreignDomain := scalar("INSERT INTO domains(tenant_id,fqdn,status,verified_at,provisioning_state,verification_token)VALUES($1,'other-import.test','active',now(),'ready','fixture')RETURNING id", other)
	hash, e := domainMail.HashPassword(f.password)
	if e != nil {
		t.Fatal(e)
	}
	clients := map[string]*importIMAPFixture{}
	for _, account := range []struct {
		local, address string
		tenant, domain int64
	}{{"alice", "alice@mail-import.test", f.tenant, domain}, {"bob", "bob@mail-import.test", f.tenant, domain}, {"other", "other@other-import.test", other, foreignDomain}} {
		id := scalar("INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,quota_bytes,status)VALUES($1,$2,$3,$4,$5,16777216,'active')RETURNING id", account.tenant, account.domain, account.local, account.address, hash)
		if account.local == "alice" {
			f.mailbox = id
		}
		clients[account.address] = &importIMAPFixture{messages: map[uint32][]byte{}, validity: 9}
	}
	f.client = clients["alice@mail-import.test"]
	nativeStore := StoreMailboxNative{Q: store.New(platform)}
	f.native = &MailboxNativeHandler{Enabled: true, Pool: platform, Auth: nativeStore, Sessions: nativeStore, Secrets: authFixtureSecrets(), Vault: webmail.NewMemoryVault(), Dialer: importFixtureDialer{clients}}
	router := gin.New()
	router.Use(middleware.BrandHostBoundary(platform))
	router.POST("/api/v1/mailbox/session", f.native.CreateSession)
	group := router.Group("/api/v1/mailbox", middleware.RequireMailboxAuth(nativeStore, f.native.Vault))
	RegisterMailboxImportRoutes(group, f.native)
	group.POST("/folders/:name/import", f.native.ImportMessage)
	f.server = httptest.NewServer(router)
	t.Cleanup(f.server.Close)
	login := func(email string) string {
		status, out := f.call(t, "POST", "/session", "", map[string]string{"email": email, "password": f.password}, "")
		if status != 200 {
			t.Fatalf("real native PG login %d %v", status, out)
		}
		return out["token"].(string)
	}
	f.token = login("alice@mail-import.test")
	f.bob = login("bob@mail-import.test")
	f.foreign = login("other@other-import.test")
	return f
}
func (f *importHTTPFixture) call(t *testing.T, method, path, token string, body any, key string) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	request, e := http.NewRequestWithContext(t.Context(), method, f.server.URL+"/api/v1/mailbox"+path, bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, e := f.server.Client().Do(request)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	data, e := io.ReadAll(response.Body)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(data, []byte("raw_secret")) || bytes.Contains(data, []byte("enc:v1:")) {
		t.Fatal("sealed source leaked to API")
	}
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return response.StatusCode, out
}
func (f *importHTTPFixture) preview(t *testing.T, raw []byte, format, key, policy string) (int, map[string]any) {
	return f.call(t, "POST", "/imports/preview", f.token, map[string]any{"format": format, "filename": "source." + format, "content_base64": base64.StdEncoding.EncodeToString(raw), "folder_enc": webmail.EncodeFolderName("INBOX"), "uid_validity": 9, "duplicate_policy": policy}, key)
}
func importFixtureMessage(id string) []byte {
	return []byte("Message-ID: <" + id + "@fixture.test>\r\nFrom: sender@fixture.test\r\nTo: alice@mail-import.test\r\nSubject: Synthetic " + id + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOriginal " + id + " payload.\r\n")
}
func importJobMap(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	job, ok := out["job"].(map[string]any)
	if !ok {
		t.Fatalf("missing job: %v", out)
	}
	return job
}
func importJobPath(job map[string]any, action string) string {
	return "/imports/" + strconv.FormatInt(int64(job["id"].(float64)), 10) + action
}
func importConfirmBody(job map[string]any, password string, indices ...int) map[string]any {
	return map[string]any{"version": job["version"], "preview_digest": job["preview_digest"], "selected_indices": indices, "confirmed": true, "current_password": password}
}

func TestMailboxImportReviewedMBOXPartialDuplicatesFreshAuthAndRLS_PG(t *testing.T) {
	f := newImportHTTPFixture(t)
	raw := importFixtureMessage("mixed")
	delimiter := []byte("From sender@fixture.test Thu Jan  1 00:00:00 1970\r\n")
	source := append(append(append(append(append([]byte{}, delimiter...), raw...), []byte("\r\n")...), delimiter...), []byte("Broken message row\r\n\r\n")...)
	source = append(source, delimiter...)
	source = append(source, raw...)
	source = append(source, []byte("\r\n")...)
	status, out := f.preview(t, source, "mbox", "import-mixed-key", "skip")
	if status != 201 {
		t.Fatalf("preview %d %v", status, out)
	}
	job := importJobMap(t, out)
	items := job["items"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["status"] != "ready" || items[1].(map[string]any)["status"] != "invalid" || items[2].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("partial preview lost facts: %v", items)
	}
	if f.client.appends != 0 {
		t.Fatal("preview appended mail")
	}
	if status, _ = f.preview(t, source, "mbox", "import-mixed-key", "keep_both"); status != 409 {
		t.Fatal("same source key accepted changed policy")
	}
	for _, token := range []string{f.bob, f.foreign} {
		if status, _ = f.call(t, "GET", importJobPath(job, ""), token, nil, ""); status != 404 {
			t.Fatal("job leaked across mailbox/tenant")
		}
	}
	if status, _ = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, "wrong-password", 1), ""); status != 401 {
		t.Fatal("fresh owner proof bypassed")
	}
	if status, _ = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 2), ""); status != 409 {
		t.Fatal("invalid item selected")
	}
	f.client.mu.Lock()
	f.client.hook = func() error {
		tx, e := f.platform.Begin(t.Context())
		if e != nil {
			return e
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var current string
		e = tx.QueryRow(t.Context(), "SELECT status FROM mailbox_import_items WHERE tenant_id=$1 AND mailbox_id=$2 AND job_id=$3 AND item_index=1", f.tenant, f.mailbox, int64(job["id"].(float64))).Scan(&current)
		if e != nil {
			return e
		}
		if current != "importing" {
			return fmt.Errorf("APPEND began before durable intent: %s", current)
		}
		var locked int64
		e = tx.QueryRow(t.Context(), "SELECT id FROM mailbox_import_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3 FOR UPDATE NOWAIT", f.tenant, f.mailbox, int64(job["id"].(float64))).Scan(&locked)
		return e
	}
	f.client.mu.Unlock()
	status, out = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), "")
	if status != 200 {
		t.Fatalf("confirm %d %v", status, out)
	}
	done := importJobMap(t, out)
	if done["status"] != "completed" || done["counts"].(map[string]any)["imported"] != float64(1) {
		t.Fatalf("completion %v", done)
	}
	if status, _ = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), ""); status != 200 || f.client.appends != 1 {
		t.Fatal("completed replay appended twice")
	}
	var payloads int
	if e := f.platform.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_import_items WHERE tenant_id=$1 AND mailbox_id=$2 AND raw_secret<>''", f.tenant, f.mailbox).Scan(&payloads); e != nil || payloads != 0 {
		t.Fatal("terminal payload retained", payloads, e)
	}
	var count int
	if e := f.app.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_import_jobs").Scan(&count); e != nil || count != 0 {
		t.Fatal("RLS missing-context failure", count, e)
	}
	tx, e := f.app.MailboxTx(t.Context(), f.tenant, f.mailbox)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if e = tx.QueryRow(t.Context(), "SELECT count(*) FROM mailbox_import_jobs WHERE tenant_id=$1 AND mailbox_id=$2", f.tenant, f.mailbox).Scan(&count); e != nil || count != 1 {
		t.Fatal("own scoped receipt missing", count, e)
	}
}

func TestMailboxImportUnknownNeedsCompleteReadAndExplicitReview_PG(t *testing.T) {
	f := newImportHTTPFixture(t)
	for _, accepted := range []bool{true, false} {
		key := fmt.Sprintf("unknown-accept-%v", accepted)
		status, out := f.preview(t, importFixtureMessage(key), "eml", key, "skip")
		if status != 201 {
			t.Fatalf("preview %d %v", status, out)
		}
		job := importJobMap(t, out)
		f.client.mu.Lock()
		f.client.failAfter = accepted
		f.client.failBefore = !accepted
		f.client.mu.Unlock()
		status, out = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), "")
		if status != 200 {
			t.Fatalf("confirm unknown %d %v", status, out)
		}
		uncertain := importJobMap(t, out)
		if uncertain["status"] != "uncertain" {
			t.Fatalf("lost acknowledgement collapsed %v", uncertain)
		}
		attempts := f.client.appends
		status, out = f.call(t, "POST", importJobPath(job, "/resume"), f.token, map[string]any{"version": uncertain["version"], "current_password": f.password}, "")
		if status != 200 || f.client.appends != attempts {
			t.Fatal("resume blindly retried unknown", status, out)
		}
		uncertain = importJobMap(t, out)
		review := func(j map[string]any) map[string]any {
			return map[string]any{"version": j["version"], "indices": []int{1}, "confirmed_not_appended": true, "reason": "Independent operator reviewed the complete absence proof.", "current_password": f.password}
		}
		if status, _ = f.call(t, "POST", importJobPath(job, "/retry"), f.token, review(uncertain), ""); status != 409 {
			t.Fatal("retry accepted without absence proof")
		}
		f.client.mu.Lock()
		f.client.scanFailure = true
		f.client.mu.Unlock()
		status, out = f.call(t, "POST", importJobPath(job, "/reconcile"), f.token, map[string]any{"version": uncertain["version"], "current_password": f.password}, "")
		if status != 200 {
			t.Fatalf("bounded unavailable scan %d %v", status, out)
		}
		uncertain = importJobMap(t, out)
		if uncertain["items"].([]any)[0].(map[string]any)["reconcile_absent"] == true {
			t.Fatal("incomplete scan asserted absence")
		}
		f.client.mu.Lock()
		f.client.scanFailure = false
		f.client.failAfter = false
		f.client.failBefore = false
		f.client.mu.Unlock()
		status, out = f.call(t, "POST", importJobPath(job, "/reconcile"), f.token, map[string]any{"version": uncertain["version"], "current_password": f.password}, "")
		if status != 200 {
			t.Fatalf("reconcile %d %v", status, out)
		}
		reconciled := importJobMap(t, out)
		if accepted {
			if reconciled["status"] != "completed" || f.client.appends != attempts {
				t.Fatal("accepted APPEND not found exactly once", reconciled)
			}
		} else {
			if reconciled["items"].([]any)[0].(map[string]any)["reconcile_absent"] != true {
				t.Fatal("complete absence was not retained", reconciled)
			}
			f.client.mu.Lock()
			f.client.scanFailure = true
			f.client.mu.Unlock()
			status, out = f.call(t, "POST", importJobPath(job, "/reconcile"), f.token, map[string]any{"version": reconciled["version"], "current_password": f.password}, "")
			if status != 200 {
				t.Fatal(status, out)
			}
			reconciled = importJobMap(t, out)
			if reconciled["items"].([]any)[0].(map[string]any)["reconcile_absent"] == true {
				t.Fatal("new failed scan retained obsolete absence proof")
			}
			if status, _ = f.call(t, "POST", importJobPath(job, "/retry"), f.token, review(reconciled), ""); status != 409 {
				t.Fatal("obsolete absence allowed retry")
			}
			f.client.mu.Lock()
			f.client.scanFailure = false
			f.client.mu.Unlock()
			status, out = f.call(t, "POST", importJobPath(job, "/reconcile"), f.token, map[string]any{"version": reconciled["version"], "current_password": f.password}, "")
			if status != 200 {
				t.Fatal(status, out)
			}
			reconciled = importJobMap(t, out)
			status, out = f.call(t, "POST", importJobPath(job, "/retry"), f.token, review(reconciled), "")
			if status != 200 || importJobMap(t, out)["status"] != "completed" || f.client.appends != attempts+1 {
				t.Fatal("manual reviewed retry failed", status, out)
			}
		}
	}
}

func TestMailboxImportCancelFencesWorkerButRetainsAcceptedTruth_PG(t *testing.T) {
	f := newImportHTTPFixture(t)
	status, out := f.preview(t, importFixtureMessage("cancel-in-flight"), "eml", "cancel-in-flight-key", "skip")
	if status != 201 {
		t.Fatal(status, out)
	}
	job := importJobMap(t, out)
	f.client.mu.Lock()
	f.client.hook = func() error {
		var version int64
		if e := f.platform.QueryRow(t.Context(), "SELECT version FROM mailbox_import_jobs WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3", f.tenant, f.mailbox, int64(job["id"].(float64))).Scan(&version); e != nil {
			return e
		}
		s, o := f.call(t, "POST", importJobPath(job, "/cancel"), f.token, map[string]any{"version": version}, "")
		if s != 200 {
			return fmt.Errorf("cancel %d %v", s, o)
		}
		return nil
	}
	f.client.mu.Unlock()
	status, out = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), "")
	if status != 200 {
		t.Fatal(status, out)
	}
	canceled := importJobMap(t, out)
	item := canceled["items"].([]any)[0].(map[string]any)
	if canceled["status"] != "canceled" || item["status"] != "unknown" || f.client.appends != 1 {
		t.Fatal("stale executor overwrote cancellation/accepted uncertainty", canceled)
	}
	status, out = f.call(t, "POST", importJobPath(job, "/reconcile"), f.token, map[string]any{"version": canceled["version"], "current_password": f.password}, "")
	if status != 200 {
		t.Fatal(status, out)
	}
	final := importJobMap(t, out)
	if final["status"] != "canceled" || final["items"].([]any)[0].(map[string]any)["status"] != "imported" || f.client.appends != 1 {
		t.Fatal("canceled accepted fact disappeared", final)
	}
}

func TestMailboxImportQuotaGenerationExpiryAndLeaseFence_PG(t *testing.T) {
	f := newImportHTTPFixture(t)
	status, out := f.preview(t, importFixtureMessage("quota-bound"), "eml", "quota-bound-key", "skip")
	if status != 201 {
		t.Fatal(status, out)
	}
	job := importJobMap(t, out)
	if _, e := f.platform.Exec(t.Context(), "UPDATE mailboxes SET quota_bytes=1 WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox); e != nil {
		t.Fatal(e)
	}
	if status, _ = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), ""); status != 409 || f.client.appends != 0 {
		t.Fatal("quota bypass", status)
	}
	if _, e := f.platform.Exec(t.Context(), "UPDATE mailboxes SET quota_bytes=16777216 WHERE tenant_id=$1 AND id=$2", f.tenant, f.mailbox); e != nil {
		t.Fatal(e)
	}
	f.client.mu.Lock()
	f.client.validity = 10
	f.client.mu.Unlock()
	status, out = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), "")
	if status != 409 || f.client.appends != 0 {
		t.Fatal("changed UIDVALIDITY appended", status, out)
	}
	// The confirmed intent remains pending, never rewritten as not sent.
	if _, e := f.platform.Exec(t.Context(), "UPDATE mailbox_import_jobs SET lease_token='replacement',lease_until=now()+interval '1 minute' WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3", f.tenant, f.mailbox, int64(job["id"].(float64))); e != nil {
		t.Fatal(e)
	}
	q := store.New(f.platform)
	rows, e := q.SetMailboxImportItem(t.Context(), store.SetMailboxImportItemParams{Selected: true, Status: "imported", BaselineUids: []int64{}, TenantID: f.tenant, MailboxID: f.mailbox, JobID: int64(job["id"].(float64)), ItemIndex: 1, LeaseFence: "obsolete-worker"})
	if e != nil || rows != 0 {
		t.Fatal("stale lease overwrote receipt", rows, e)
	}
	// Expired preview cannot be confirmed even with fresh owner credentials.
	f.client.mu.Lock()
	f.client.validity = 9
	f.client.mu.Unlock()
	status, out = f.preview(t, importFixtureMessage("expired-proof"), "eml", "expired-proof-key", "skip")
	if status != 201 {
		t.Fatal(status, out)
	}
	expired := importJobMap(t, out)
	if _, e = f.platform.Exec(t.Context(), "UPDATE mailbox_import_jobs SET preview_expires_at=now()-interval '1 second' WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3", f.tenant, f.mailbox, int64(expired["id"].(float64))); e != nil {
		t.Fatal(e)
	}
	if status, _ = f.call(t, "POST", importJobPath(expired, "/confirm"), f.token, importConfirmBody(expired, f.password, 1), ""); status != 409 || f.client.appends != 0 {
		t.Fatal("expired preview accepted", status)
	}
}

func TestMailboxImportExpiredUnknownPayloadRetainsUncertainty_PG(t *testing.T) {
	f := newImportHTTPFixture(t)
	status, out := f.preview(t, importFixtureMessage("expired-unknown"), "eml", "expired-unknown-key", "skip")
	if status != 201 {
		t.Fatal(status, out)
	}
	job := importJobMap(t, out)
	f.client.mu.Lock()
	f.client.failAfter = true
	f.client.mu.Unlock()
	status, out = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), "")
	if status != 200 {
		t.Fatal(status, out)
	}
	uncertain := importJobMap(t, out)
	if _, e := f.platform.Exec(t.Context(), "UPDATE mailbox_import_items SET raw_secret='' WHERE tenant_id=$1 AND mailbox_id=$2 AND job_id=$3", f.tenant, f.mailbox, int64(job["id"].(float64))); e != nil {
		t.Fatal(e)
	}
	status, out = f.call(t, "POST", importJobPath(job, "/reconcile"), f.token, map[string]any{"version": uncertain["version"], "current_password": f.password}, "")
	if status != 200 || importJobMap(t, out)["items"].([]any)[0].(map[string]any)["status"] != "unknown" || f.client.appends != 1 {
		t.Fatal("expired source converted unknown acceptance to failure", status, out)
	}
}

func TestMailboxImportExpiredWorkerCannotOverwriteAcceptedUncertainty_PG(t *testing.T) {
	f := newImportHTTPFixture(t)
	status, out := f.preview(t, importFixtureMessage("expired-worker"), "eml", "expired-worker-key", "skip")
	if status != 201 {
		t.Fatal(status, out)
	}
	job := importJobMap(t, out)
	f.client.mu.Lock()
	f.client.hook = func() error {
		_, e := f.platform.Exec(t.Context(), "UPDATE mailbox_import_jobs SET lease_until=now()-interval '1 second' WHERE tenant_id=$1 AND mailbox_id=$2 AND id=$3", f.tenant, f.mailbox, int64(job["id"].(float64)))
		return e
	}
	f.client.mu.Unlock()
	status, out = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), "")
	if status != 200 && status != 202 {
		t.Fatal(status, out)
	}
	uncertain := importJobMap(t, out)
	if uncertain["items"].([]any)[0].(map[string]any)["status"] != "unknown" || f.client.appends != 1 {
		t.Fatal("expired worker result overwrote uncertainty", uncertain)
	}
	status, out = f.call(t, "POST", importJobPath(job, "/reconcile"), f.token, map[string]any{"version": uncertain["version"], "current_password": f.password}, "")
	if status != 200 || importJobMap(t, out)["items"].([]any)[0].(map[string]any)["status"] != "imported" || f.client.appends != 1 {
		t.Fatal("expired worker actual accepted fact could not be recovered", status, out)
	}
}
