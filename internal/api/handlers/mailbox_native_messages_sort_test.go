package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// sortCursorClient records ListMessages opts for parameter assertions.
type sortCursorClient struct {
	fakeIMAPClient
	gotOpts webmail.ListOpts
	page    []webmail.MessageSummary
	nextUID uint32
}

func (f *sortCursorClient) ListMessages(_ context.Context, _ string, opts webmail.ListOpts) (webmail.ListResult, error) {
	f.gotOpts = opts
	// Echo what a real conn reports so the handler passthrough is observable.
	return webmail.ListResult{
		Messages:     f.page,
		Total:        len(f.page),
		NextCursor:   f.nextUID,
		Sort:         opts.SortBy,
		ServerSorted: opts.SortBy != "",
		ThreadMode:   map[bool]string{true: "client", false: ""}[opts.Thread],
	}, nil
}

func newSortTestEnv(t *testing.T, client webmail.Client) (*gin.Engine, string) {
	t.Helper()
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sessions := &fakeMailboxSessions{}
	h := &MailboxNativeHandler{
		Enabled:  true,
		Auth:     fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash},
		Sessions: sessions,
		Vault:    webmail.NewMemoryVault(),
		Dialer:   &fakeDialer{wantAddress: "alice@example.test", client: client},
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.POST("/api/v1/mailbox/session", h.CreateSession)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	auth.GET("/folders/:name/messages", h.ListMessages)
	auth.GET("/search", h.CrossFolderSearch)
	return r, loginNative(t, r, "alice@example.test", password)
}

func authedGet(r *gin.Engine, token, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	return w
}

func TestListMessagesSortCursorThreadParams(t *testing.T) {
	client := &sortCursorClient{
		page:    []webmail.MessageSummary{{UID: 5, Subject: "s", From: []webmail.Address{{Email: "b@x"}}}},
		nextUID: 5,
	}
	r, token := newSortTestEnv(t, client)
	folder := webmail.EncodeFolderName("INBOX")
	w := authedGet(r, token,
		"/api/v1/mailbox/folders/"+folder+"/messages?limit=10&offset=20&sort=size&order=asc&cursor=7&thread=1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	opts := client.gotOpts
	if opts.SortBy != "size" || opts.SortDesc || opts.CursorUID != 7 || !opts.Thread {
		t.Fatalf("opts wrong: %+v", opts)
	}
	if opts.Offset != 20 || opts.Limit != 10 {
		t.Fatalf("paging wrong: %+v", opts)
	}
	var body struct {
		NextCursor   uint32 `json:"next_cursor"`
		Sort         string `json:"sort"`
		ServerSorted bool   `json:"server_sorted"`
		ThreadMode   string `json:"thread_mode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.NextCursor != 5 || body.Sort != "size" || !body.ServerSorted || body.ThreadMode != "client" {
		t.Fatalf("body wrong: %+v", body)
	}
}

func TestListMessagesInvalidSortRejected(t *testing.T) {
	r, token := newSortTestEnv(t, &sortCursorClient{})
	folder := webmail.EncodeFolderName("INBOX")
	// Note: raw ";" in a query is dropped by Go's URL parser, so use a plain
	// unknown key to exercise rejection.
	w := authedGet(r, token, "/api/v1/mailbox/folders/"+folder+"/messages?sort=uid")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// searchAllClient implements webmail.FolderLister for the fan-out endpoint.
type searchAllClient struct {
	fakeIMAPClient
	byFolder map[string]webmail.ListResult
	errs     map[string]error
	visited  []string
}

func (f *searchAllClient) ListFolders(context.Context) ([]webmail.Folder, error) {
	return webmail.WithEncodedNames([]webmail.Folder{
		{Name: "INBOX", Delimiter: "/"},
		{Name: "Sent", Delimiter: "/"},
		{Name: "INBOX/Sub", Delimiter: "/"},
	}), nil
}

func (f *searchAllClient) ListMessages(_ context.Context, folder string, _ webmail.ListOpts) (webmail.ListResult, error) {
	f.visited = append(f.visited, folder)
	if err, ok := f.errs[folder]; ok {
		return webmail.ListResult{}, err
	}
	return f.byFolder[folder], nil
}

func TestCrossFolderSearchFanoutMerge(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := &searchAllClient{
		byFolder: map[string]webmail.ListResult{
			"INBOX": {Messages: []webmail.MessageSummary{{UID: 1, Subject: "match", Date: base}}},
			"Sent":  {Messages: []webmail.MessageSummary{{UID: 2, Subject: "match sent", Date: base.Add(time.Hour)}}},
		},
		errs: map[string]error{"Broken": context.DeadlineExceeded},
	}
	r, token := newSortTestEnv(t, client)
	w := authedGet(r, token, "/api/v1/mailbox/search?q=match&folders=INBOX,Sent,"+webmail.EncodeFolderName("Broken"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Messages []webmail.MessageSummary `json:"messages"`
		Total    int                      `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 || len(body.Messages) != 2 {
		t.Fatalf("merge wrong: %+v", body)
	}
	if body.Messages[0].Folder != "Sent" || !body.Messages[0].Date.After(body.Messages[1].Date) {
		t.Fatalf("date merge wrong: %+v %+v", body.Messages[0], body.Messages[1])
	}
	if body.Messages[0].FolderEnc == "" || body.Messages[1].FolderEnc == "" {
		t.Fatalf("folder_enc missing")
	}
	if len(client.visited) != 3 || client.visited[0] != "INBOX" || client.visited[1] != "Sent" || client.visited[2] != "Broken" {
		t.Fatalf("visited wrong: %v", client.visited)
	}
}

func TestCrossFolderSearchDefaultsAndValidation(t *testing.T) {
	client := &searchAllClient{byFolder: map[string]webmail.ListResult{}}
	r, token := newSortTestEnv(t, client)
	w := authedGet(r, token, "/api/v1/mailbox/search")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty q: want 400 got %d", w.Code)
	}
	w = authedGet(r, token, "/api/v1/mailbox/search?q=hi")
	if w.Code != http.StatusOK {
		t.Fatalf("default scope failed: %d %s", w.Code, w.Body.String())
	}
	if len(client.visited) != 2 || client.visited[0] != "INBOX" || client.visited[1] != "Sent" {
		t.Fatalf("top-level scope wrong: %v", client.visited)
	}
}
