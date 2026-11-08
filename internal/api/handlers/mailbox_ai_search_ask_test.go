package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/airetrieval"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/aianswer"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// fakeAskIMAP implements the slice of webmail.Client the handler reads.
type fakeAskIMAP struct {
	webmail.Client
	messages     map[string]map[uint32]string
	richMessages map[string]map[uint32]*webmail.Message
	validity     map[string]uint32
	fetchCount   atomic.Int64
}

func (f *fakeAskIMAP) ListFolders(context.Context) ([]webmail.Folder, error) {
	seen := map[string]bool{}
	for name := range f.messages {
		seen[name] = true
	}
	for name := range f.richMessages {
		seen[name] = true
	}
	names := make([]webmail.Folder, 0, len(seen))
	for name := range seen {
		names = append(names, webmail.Folder{Name: name})
	}
	return names, nil
}

func (f *fakeAskIMAP) FolderUIDValidity(_ context.Context, folder string) (uint32, error) {
	return f.validity[folder], nil
}

func (f *fakeAskIMAP) ListMessages(_ context.Context, folder string, _ webmail.ListOpts) (webmail.ListResult, error) {
	result := webmail.ListResult{}
	for uid := range f.messages[folder] {
		result.Messages = append(result.Messages, webmail.MessageSummary{UID: uid})
	}
	for uid, msg := range f.richMessages[folder] {
		summary := msg.MessageSummary
		summary.UID = uid
		summary.UIDValidity = f.validity[folder]
		result.Messages = append(result.Messages, summary)
	}
	return result, nil
}

func (f *fakeAskIMAP) GetMessage(_ context.Context, folder string, uid uint32) (*webmail.Message, error) {
	f.fetchCount.Add(1)
	if f.richMessages != nil && f.richMessages[folder] != nil {
		if msg, ok := f.richMessages[folder][uid]; ok {
			return msg, nil
		}
	}
	message := &webmail.Message{MessageSummary: webmail.MessageSummary{UID: uid}, UIDValidity: f.validity[folder]}
	if text, ok := f.messages[folder][uid]; ok {
		message.Text = text
	}
	return message, nil
}

func (f *fakeAskIMAP) Close() error { return nil }

type fakeAskDialer struct{ client *fakeAskIMAP }

func (d fakeAskDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return d.client, nil
}

func newAskRouter(t *testing.T, build func(webmail.CredentialVault) *AISearchAsk) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	loginAttempts = newLoginLimiter()
	password := "correct-horse-battery"
	hash, err := mail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &fakeMailboxSessions{}
	vault := webmail.NewMemoryVault()
	native := &MailboxNativeHandler{Enabled: true, Auth: fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "alice@example.test", hash: hash}, Sessions: sessions, Vault: vault}
	r := newNativeTestRouter(t, native, sessions)
	auth := r.Group("/api/v1/mailbox")
	auth.Use(middleware.RequireMailboxAuth(sessions))
	h := build(vault)
	auth.POST("/ai/search-ask", h.Handle)
	auth.GET("/ai/search-ask/status", h.Status)
	auth.POST("/ai/search-ask/clear", h.Clear)
	auth.GET("/ai/index/status", h.Status)
	auth.POST("/ai/index/clear", h.Clear)
	t.Cleanup(func() { loginAttempts = newLoginLimiter() })
	return r
}

func askOnce(t *testing.T, r *gin.Engine, token, question string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/ai/search-ask", bytes.NewBufferString(`{"question":"`+question+`"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAISearchAskGroundedAnswerCitesRetrievedChunks(t *testing.T) {
	calls := 0
	source := &airetrieval.FakeSource{Mail: map[string]map[uint32]string{"INBOX": {5: "the invoice total is 42 euros"}}, Validity: map[string]uint32{"INBOX": 4}}
	store := airetrieval.NewFakeStore()
	principal := airetrieval.Principal{TenantID: 7, MailboxID: 9}
	if _, err := (airetrieval.Indexer{Store: store, Source: source}).Reindex(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	model := func(context.Context, string, []aianswer.Source) (string, []string, error) {
		calls++
		return "42 euros.", []string{"1"}, nil
	}
	handler := &AISearchAsk{Pool: nil, Store: store, Dialer: fakeAskDialer{client: &fakeAskIMAP{messages: map[string]map[uint32]string{"INBOX": {5: "the invoice total is 42 euros"}}, validity: map[string]uint32{"INBOX": 4}}}, Vault: webmail.NewMemoryVault(), ModelFor: func(context.Context, int64) (aianswer.Model, error) { return model, nil }}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk { handler.Vault = vault; return handler })
	native := loginNative(t, r, "alice@example.test", "correct-horse-battery")

	// Vault the session password exactly like the native login flow does.
	nativeSessions := middleware.ContextMailboxTokenHash
	_ = nativeSessions

	rec := askOnce(t, r, native, "invoice total")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("model calls = %d", calls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "\"grounded\":true") || !strings.Contains(body, "\"citations\":[\"1\"]") {
		t.Fatal(body)
	}
}

func TestAISearchAskEmptyMailboxMakesZeroModelCalls(t *testing.T) {
	calls := 0
	store := airetrieval.NewFakeStore()
	model := func(context.Context, string, []aianswer.Source) (string, []string, error) {
		calls++
		return "invented", nil, nil
	}
	handler := &AISearchAsk{Pool: nil, Store: store, Dialer: fakeAskDialer{client: &fakeAskIMAP{messages: map[string]map[uint32]string{"INBOX": {}}}}, Vault: webmail.NewMemoryVault(), ModelFor: func(context.Context, int64) (aianswer.Model, error) { return model, nil }}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk { handler.Vault = vault; return handler })
	native := loginNative(t, r, "alice@example.test", "correct-horse-battery")
	rec := askOnce(t, r, native, "anything")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if calls != 0 || !strings.Contains(rec.Body.String(), "\"grounded\":false") {
		t.Fatalf("calls=%d %s", calls, rec.Body.String())
	}
}

func TestAISearchAskRequiresMailboxSession(t *testing.T) {
	handler := &AISearchAsk{}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk { handler.Vault = vault; return handler })
	rec := askOnce(t, r, "", "hello")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestMailboxAISearchAskGroundedAnswerCitesRetrievedChunks(t *testing.T) {
	TestAISearchAskGroundedAnswerCitesRetrievedChunks(t)
}

func TestMailboxAISearchAskPayloadMinimization(t *testing.T) {
	f := newNativeAIHTTPFixture(t)
	token := loginNative(t, f.router, f.principal.Address, f.password)
	cfg, err := store.New(f.pool).PlatformGetAIProvider(t.Context(), f.principal.TenantID)
	if err != nil {
		t.Fatal(err)
	}

	// Reading context: req.UID = 5.
	// Original message: "The October invoice total is 42 euros. Payment is due on Friday. Please confirm the bank details independently."
	// Selection: "The October invoice total is 42 euros."
	// Caller passes the full message body in "body" and also "draft": "Payment is due on Friday..."
	input := map[string]any{
		"operation":            "summarize",
		"configuration_digest": cfg.ConfigurationDigest,
		"confirmed":            true,
		"preview_only":         true,
		"request_key":          "native-ai-payload-min-001",
		"folder_enc":           webmail.EncodeFolderName("INBOX"),
		"uid":                  5,
		"uid_validity":         9,
		"selection":            "The October invoice total is 42 euros.",
		"body":                 "The October invoice total is 42 euros. Payment is due on Friday. Please confirm the bank details independently.",
		"draft":                "Payment is due on Friday. Please confirm the bank details independently.",
	}
	response := nativeAIJSON(t, f, token, "/api/v1/mailbox/ai/assist", input)
	if response.Code != 200 {
		t.Fatalf("assist status=%d %s", response.Code, response.Body.String())
	}
	var preview struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || len(preview.Messages) < 2 {
		t.Fatalf("invalid preview: %s", response.Body.String())
	}
	// The user message is the registry envelope (parameters + wrapped
	// untrusted content), so payload minimization is asserted on the
	// wrapped content itself.
	content := preview.Messages[1].Content
	if !strings.Contains(content, "The October invoice total is 42 euros.") {
		t.Fatalf("expected selection in model prompt: %s", content)
	}
	if strings.Contains(content, "Payment is due on Friday") {
		t.Fatalf("unselected body or draft leaked into model prompt: %s", content)
	}
}

func TestMailboxAISearchAskHTMLContentNormalization(t *testing.T) {
	rawHTML := "<html><body><style>.hide{display:none}</style><h1>Monthly Statement</h1><p>Your invoice total is <b>88 euros</b>.</p><script>console.log('secret')</script></body></html>"
	extracted := airetrieval.ExtractHTMLText(rawHTML)
	if !strings.Contains(extracted, "Monthly Statement") || !strings.Contains(extracted, "Your invoice total is 88 euros.") {
		t.Fatalf("failed to extract text: %s", extracted)
	}
	if strings.Contains(extracted, "console.log") || strings.Contains(extracted, "display:none") {
		t.Fatalf("script or style not stripped: %s", extracted)
	}

	source := &airetrieval.FakeSource{
		HTML:     map[string]map[uint32]string{"INBOX": {10: rawHTML}},
		Validity: map[string]uint32{"INBOX": 1},
	}
	store := airetrieval.NewFakeStore()
	principal := airetrieval.Principal{TenantID: 7, MailboxID: 9}
	count, err := (airetrieval.Indexer{Store: store, Source: source}).Reindex(context.Background(), principal)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	chunks, err := airetrieval.Search(context.Background(), store, source, principal, "88 euros")
	if err != nil || len(chunks) == 0 {
		t.Fatalf("HTML-only message not found in search: %v", err)
	}
	if !strings.Contains(chunks[0].Content, "88 euros") {
		t.Fatalf("unexpected chunk content: %s", chunks[0].Content)
	}
}

func TestMailboxAISearchAskSubjectAndSenderIndexing(t *testing.T) {
	source := &airetrieval.FakeSource{
		Mail:     map[string]map[uint32]string{"INBOX": {20: "The project deliverables are attached for review."}},
		Subject:  map[string]map[uint32]string{"INBOX": {20: "Q4 Financial Audit Deliverable"}},
		From:     map[string]map[uint32]string{"INBOX": {20: "Audit Partner <auditor@deloitte.test>"}},
		Validity: map[string]uint32{"INBOX": 1},
	}
	store := airetrieval.NewFakeStore()
	principal := airetrieval.Principal{TenantID: 7, MailboxID: 9}
	count, err := (airetrieval.Indexer{Store: store, Source: source}).Reindex(context.Background(), principal)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}

	chunksBySubject, err := airetrieval.Search(context.Background(), store, source, principal, "Financial Audit")
	if err != nil || len(chunksBySubject) == 0 {
		t.Fatalf("search by subject failed: %v", err)
	}
	if !strings.Contains(chunksBySubject[0].Content, "Subject: Q4 Financial Audit Deliverable") {
		t.Fatalf("chunk does not contain subject: %s", chunksBySubject[0].Content)
	}

	chunksBySender, err := airetrieval.Search(context.Background(), store, source, principal, "auditor@deloitte.test")
	if err != nil || len(chunksBySender) == 0 {
		t.Fatalf("search by sender failed: %v", err)
	}
	if !strings.Contains(chunksBySender[0].Content, "auditor@deloitte.test") {
		t.Fatalf("chunk does not contain sender: %s", chunksBySender[0].Content)
	}
}

func TestMailboxAISearchAskFolderExclusionsAndFilters(t *testing.T) {
	fakeIMAP := &fakeAskIMAP{
		richMessages: map[string]map[uint32]*webmail.Message{
			"INBOX": {
				1: {
					MessageSummary: webmail.MessageSummary{UID: 1, Subject: "Invoice received"},
					Text:           "Normal email text in inbox.",
				},
			},
			"Trash": {
				2: {
					MessageSummary: webmail.MessageSummary{UID: 2, Subject: "Secret trash"},
					Text:           "Secret trash text in trash.",
				},
			},
			"Junk": {
				3: {
					MessageSummary: webmail.MessageSummary{UID: 3, Subject: "Spammy offer"},
					Text:           "Spammy junk text in spam.",
				},
			},
			"Drafts": {
				4: {
					MessageSummary: webmail.MessageSummary{UID: 4, Subject: "Unsent draft"},
					Text:           "Unsent draft text in drafts.",
				},
			},
		},
		validity: map[string]uint32{
			"INBOX": 1, "Trash": 1, "Junk": 1, "Drafts": 1,
		},
	}
	store := airetrieval.NewFakeStore()
	_ = airetrieval.Principal{TenantID: 7, MailboxID: 9}
	// Let the handler's reindex run via dialer/WebmailSource during Handle call.

	modelCalls := 0
	model := aianswer.Model(func(_ context.Context, _ string, sources []aianswer.Source) (string, []string, error) {
		modelCalls++
		cites := make([]string, 0, len(sources))
		for _, s := range sources {
			cites = append(cites, s.ID)
		}
		return "Answer", cites, nil
	})
	handler := &AISearchAsk{
		Store:  store,
		Dialer: fakeAskDialer{client: fakeIMAP},
		Vault:  webmail.NewMemoryVault(),
		ModelFor: func(context.Context, int64) (aianswer.Model, error) {
			return model, nil
		},
	}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk {
		handler.Vault = vault
		return handler
	})
	token := loginNative(t, r, "alice@example.test", "correct-horse-battery")

	// 1. By default, searching for "trash" in cross-folder search returns 0 grounded citations
	rec := askOnce(t, r, token, "trash")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "\"grounded\":true") {
		t.Fatalf("trash searched by default: %s", rec.Body.String())
	}

	// 2. By default, searching for "spammy" returns 0 grounded citations
	rec = askOnce(t, r, token, "spammy")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "\"grounded\":true") {
		t.Fatalf("spam searched by default: %s", rec.Body.String())
	}

	// 3. By default, searching for "unsent" returns 0 grounded citations
	rec = askOnce(t, r, token, "unsent")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "\"grounded\":true") {
		t.Fatalf("drafts searched by default: %s", rec.Body.String())
	}

	// 4. Searching for "inbox" succeeds
	rec = askOnce(t, r, token, "invoice")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "\"grounded\":true") {
		t.Fatalf("inbox not found: %s", rec.Body.String())
	}

	// 5. With include_trash: true, trash is included
	askJSON := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/ai/search-ask", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}
	rec = askJSON(map[string]any{"question": "trash", "include_trash": true})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "\"grounded\":true") {
		t.Fatalf("include_trash failed: %s", rec.Body.String())
	}

	// 6. Explicit folder_enc targets the requested folder even if it is Trash
	rec = askJSON(map[string]any{"question": "trash", "folder_enc": webmail.EncodeFolderName("Trash")})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "\"grounded\":true") {
		t.Fatalf("folder_enc targeting Trash failed: %s", rec.Body.String())
	}
}

func TestMailboxAISearchAskIncrementalIndexingNoRedundantFetch(t *testing.T) {
	ctx := context.Background()
	source := &airetrieval.FakeSource{
		Mail: map[string]map[uint32]string{
			"INBOX": {
				1: "message one body",
				2: "message two body",
				3: "message three body",
			},
		},
		Validity: map[string]uint32{"INBOX": 10},
	}
	store := airetrieval.NewFakeStore()
	principal := airetrieval.Principal{TenantID: 7, MailboxID: 9}
	var progressReports []airetrieval.IndexProgress
	indexer := airetrieval.Indexer{
		Store:  store,
		Source: source,
		Progress: func(p airetrieval.IndexProgress) {
			progressReports = append(progressReports, p)
		},
	}

	count, err := indexer.Reindex(ctx, principal)
	if err != nil || count != 3 {
		t.Fatalf("expected 3 reindexed, got %d %v", count, err)
	}
	if source.FetchCount != 3 {
		t.Fatalf("expected 3 fetches, got %d", source.FetchCount)
	}
	if len(progressReports) != 3 {
		t.Fatalf("expected 3 progress reports, got %d", len(progressReports))
	}

	// Second reindex pass: unchanged UIDVALIDITY and live UIDs
	source.FetchCount = 0
	progressReports = nil
	count, err = indexer.Reindex(ctx, principal)
	if err != nil || count != 0 {
		t.Fatalf("expected 0 reindexed, got %d %v", count, err)
	}
	if source.FetchCount != 0 {
		t.Fatalf("CRITICAL F07/D10 VIOLATION: reindex fetched %d bodies over IMAP for unchanged messages", source.FetchCount)
	}

	// Add 1 new message
	source.Mail["INBOX"][4] = "message four body"
	count, err = indexer.Reindex(ctx, principal)
	if err != nil || count != 1 {
		t.Fatalf("expected 1 reindexed, got %d %v", count, err)
	}
	if source.FetchCount != 1 {
		t.Fatalf("expected exactly 1 fetch for the new message, got %d", source.FetchCount)
	}
}

func TestMailboxAISearchAskIndexStatusAndClearPurge(t *testing.T) {
	store := airetrieval.NewFakeStore()
	principal := airetrieval.Principal{TenantID: 7, MailboxID: 9}
	if err := store.ReplaceMessage(context.Background(), principal, "INBOX", 1, 1, 1, "d1", []string{"chunk 1", "chunk 2"}); err != nil {
		t.Fatal(err)
	}
	otherPrincipal := airetrieval.Principal{TenantID: 8, MailboxID: 99}
	if err := store.ReplaceMessage(context.Background(), otherPrincipal, "INBOX", 1, 1, 1, "d2", []string{"foreign chunk"}); err != nil {
		t.Fatal(err)
	}

	handler := &AISearchAsk{Store: store, Vault: webmail.NewMemoryVault(), Dialer: fakeAskDialer{client: &fakeAskIMAP{}}}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk {
		handler.Vault = vault
		return handler
	})
	token := loginNative(t, r, "alice@example.test", "correct-horse-battery")

	// GET status
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/mailbox/ai/search-ask/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status code=%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"indexed_messages\":1") {
		t.Fatalf("expected 1 indexed message in status: %s", rec.Body.String())
	}

	// POST clear
	req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/mailbox/ai/search-ask/clear", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "\"purged\":true") {
		t.Fatalf("clear failed: %d %s", rec.Code, rec.Body.String())
	}

	states, _ := store.MessageStates(context.Background(), principal)
	if len(states) != 0 {
		t.Fatalf("chunks were not purged: %v", states)
	}

	// Other principal's chunks must remain intact
	otherStates, _ := store.MessageStates(context.Background(), otherPrincipal)
	if len(otherStates) != 1 {
		t.Fatalf("cross-tenant chunk leaked or deleted: %v", otherStates)
	}
}

func TestMailboxAISearchAskAttachmentOptIn(t *testing.T) {
	ctx := context.Background()
	msg := &webmail.Message{
		MessageSummary: webmail.MessageSummary{UID: 1, Subject: "Offer Letter"},
		Text:           "Please see attached offer.",
		Attachments: []webmail.AttachmentMeta{
			{Filename: "offer_letter_confidential.pdf", ContentType: "application/pdf"},
		},
	}
	fakeIMAP := &fakeAskIMAP{
		richMessages: map[string]map[uint32]*webmail.Message{"INBOX": {1: msg}},
		validity:     map[string]uint32{"INBOX": 1},
	}

	defaultSource := airetrieval.WebmailSource{Client: fakeIMAP, IndexAttachments: false}
	textDefault, err := defaultSource.MessageText(ctx, "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(textDefault, "offer_letter_confidential.pdf") {
		t.Fatalf("attachments indexed by default without opt-in: %s", textDefault)
	}

	optedInSource := airetrieval.WebmailSource{Client: fakeIMAP, IndexAttachments: true}
	textOptedIn, err := optedInSource.MessageText(ctx, "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOptedIn, "offer_letter_confidential.pdf") {
		t.Fatalf("attachments omitted when opted in: %s", textOptedIn)
	}
}
