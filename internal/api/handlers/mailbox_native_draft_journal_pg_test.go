package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type draftJournalIMAP struct {
	*fakeIMAPClient
	mu        sync.Mutex
	raw       map[uint32][]byte
	next      uint32
	appends   int
	loseReply bool
}

func (*draftJournalIMAP) EnsureDraftsFolder(context.Context) (string, error) { return "Drafts", nil }
func (*draftJournalIMAP) Close() error                                       { return nil }
func (*draftJournalIMAP) GetThreadMembers(context.Context, string, uint32, webmail.ThreadMembersOpts) (webmail.ThreadMembersResult, error) {
	return webmail.ThreadMembersResult{}, nil
}

type draftJournalDialer struct{ client webmail.Client }

func (d draftJournalDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return d.client, nil
}

func (f *draftJournalIMAP) AppendPreparedDraft(_ context.Context, folder string, raw []byte) (webmail.DraftSaveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.appends++
	f.raw[f.next] = append([]byte(nil), raw...)
	if f.loseReply {
		return webmail.DraftSaveResult{}, webmail.ErrDraftUncertain
	}
	return webmail.DraftSaveResult{Folder: folder, NameEnc: webmail.EncodeFolderName(folder), UID: f.next, UIDValidity: 9}, nil
}
func (f *draftJournalIMAP) FindDraftCopy(_ context.Context, folder, id, digest string) (webmail.DraftSaveResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for uid, raw := range f.raw {
		sum := sha256.Sum256(raw)
		if strings.Contains(string(raw), "Message-ID: "+id) && hex.EncodeToString(sum[:]) == digest {
			return webmail.DraftSaveResult{Folder: folder, NameEnc: webmail.EncodeFolderName(folder), UID: uid, UIDValidity: 9}, true, nil
		}
	}
	return webmail.DraftSaveResult{}, false, nil
}
func (f *draftJournalIMAP) GetRawMessage(_ context.Context, _ string, uid uint32) ([]byte, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.raw[uid] == nil {
		return nil, 0, webmail.ErrMessageNotFound
	}
	return f.raw[uid], 9, nil
}
func (f *draftJournalIMAP) DeleteMessages(_ context.Context, _ string, uids []uint32, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, uid := range uids {
		delete(f.raw, uid)
	}
	return nil
}

func TestDraftSaveJournalReplayReconcileAndConflictPG(t *testing.T) {
	fixture := newNativeAIHTTPFixture(t)
	imap := &draftJournalIMAP{fakeIMAPClient: &fakeIMAPClient{}, raw: map[uint32][]byte{}, next: 100}
	vault := webmail.NewMemoryVault()
	vault.Put( //nolint:staticcheck // test adapter: Put is the interface under test
		"token", "password")
	h := &MailboxNativeHandler{Enabled: true, Pool: fixture.pool, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{}, Vault: vault, Dialer: &fakeDialer{wantAddress: fixture.principal.Address, client: imap}}
	request := func(key string, body string, status int) webmail.DraftSaveResult {
		t.Helper()
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: fixture.principal.TenantID, MailboxID: fixture.principal.MailboxID, Address: fixture.principal.Address})
			c.Set(middleware.ContextMailboxTokenHash, "token")
		})
		router.PUT("/drafts", h.SaveDraft)
		req := httptest.NewRequestWithContext(t.Context(), "PUT", "/drafts", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s: %d %s", key, w.Code, w.Body.String())
		}
		var result webmail.DraftSaveResult
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	original := request("draft-first-revision-0001", `{"text":"first"}`, 200)
	repeated := request("draft-first-revision-0001", `{"text":"first"}`, 200)
	if original.UID != repeated.UID || imap.appends != 1 {
		t.Fatal("lost HTTP response repeated APPEND")
	}
	request("draft-first-revision-0001", `{"text":"changed"}`, 409)
	replacement := `{"uid":101,"uid_validity":9,"text":"second"}`
	imap.loseReply = true
	request("draft-second-revision-0002", replacement, 409)
	imap.loseReply = false
	recovered := request("draft-second-revision-0002", replacement, 200)
	if recovered.UID != 102 || imap.appends != 2 || imap.raw[101] != nil {
		t.Fatal("unknown APPEND was not reconciled before cleanup")
	}
	request("draft-stale-client-0003", `{"uid":101,"uid_validity":9,"text":"stale"}`, 409)
	if imap.appends != 2 {
		t.Fatal("stale tab overwrote replaced draft")
	}
	// A missing or mismatched stored candidate must retain uncertainty, never append again.
	imap.loseReply = true
	request("draft-third-revision-0004", `{"text":"third"}`, 409)
	imap.loseReply = false
	imap.raw[103] = []byte("different bytes")
	request("draft-third-revision-0004", `{"text":"third"}`, 409)
	if imap.appends != 3 {
		t.Fatal(errors.New("unconfirmed save blindly repeated APPEND"))
	}
}

func TestDraftSaveJournalSingleConnectionConcurrentPG(t *testing.T) {
	fixture := newNativeAIHTTPFixture(t)
	config := fixture.pool.Config()
	config.MaxConns = 1
	config.MinConns = 0
	rawPool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	pool := &db.Pool{Pool: rawPool}
	defer pool.Close()
	imap := &draftJournalIMAP{fakeIMAPClient: &fakeIMAPClient{}, raw: map[uint32][]byte{}, next: 100}
	for uid := uint32(1); uid <= 8; uid++ {
		imap.raw[uid] = []byte("Original persisted draft")
	}
	vault := webmail.NewMemoryVault()
	vault.Put( //nolint:staticcheck // test adapter: Put is the interface under test
		"token", "password")
	h := &MailboxNativeHandler{Enabled: true, Pool: pool, Auth: fakeMailboxAuth{}, Sessions: &fakeMailboxSessions{}, Vault: vault, Dialer: draftJournalDialer{client: imap}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.ContextMailboxSession, webmail.Session{TenantID: fixture.principal.TenantID, MailboxID: fixture.principal.MailboxID, Address: fixture.principal.Address})
		c.Set(middleware.ContextMailboxTokenHash, "token")
	})
	router.PUT("/drafts", h.SaveDraft)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 8)
	for uid := 1; uid <= 8; uid++ {
		go func() {
			body := fmt.Sprintf(`{"uid":%d,"uid_validity":9,"text":"Concurrent revision"}`, uid)
			req := httptest.NewRequestWithContext(ctx, "PUT", "/drafts", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", fmt.Sprintf("single-connection-draft-%04d", uid))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != 200 {
				finished <- fmt.Errorf("request %d status %d: %s", uid, response.Code, response.Body.String())
				return
			}
			finished <- nil
		}()
	}
	for range 8 {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	if imap.appends != 8 {
		t.Fatal("concurrent append count", imap.appends)
	}
	var setting string
	var locks int
	if err = pool.QueryRow(t.Context(), "SELECT coalesce(current_setting('urmail.tenant_id',true),''), (SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory')").Scan(&setting, &locks); err != nil {
		t.Fatal(err)
	}
	if setting != "" || locks != 0 {
		t.Fatalf("pooled connection retained tenant or advisory scope: setting=%q locks=%d", setting, locks)
	}
}
