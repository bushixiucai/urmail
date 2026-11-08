package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	netmail "net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/domain/aianswer"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type nativeAIFixtureMail struct {
	webmail.Client
	mu       sync.Mutex
	raw      map[uint32][]byte
	messages map[string]map[uint32]*webmail.Message
	next     uint32
}

func (f *nativeAIFixtureMail) ListFolders(context.Context) ([]webmail.Folder, error) {
	return webmail.WithEncodedNames([]webmail.Folder{{Name: "INBOX", Delimiter: "/"}, {Name: "Drafts", Delimiter: "/", Flags: []string{`\Drafts`}}}), nil
}
func (f *nativeAIFixtureMail) FolderUIDValidity(context.Context, string) (uint32, error) {
	return 9, nil
}
func (f *nativeAIFixtureMail) ListMessages(_ context.Context, folder string, opts webmail.ListOpts) (webmail.ListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := webmail.ListResult{UIDValidity: 9, Messages: []webmail.MessageSummary{}, Total: len(f.messages[folder])}
	for uid, msg := range f.messages[folder] {
		if opts.CursorUID == 0 || uid < opts.CursorUID {
			result.Messages = append(result.Messages, msg.MessageSummary)
		}
	}
	sort.Slice(result.Messages, func(i, j int) bool { return result.Messages[i].UID > result.Messages[j].UID })
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	if len(result.Messages) > limit {
		result.Messages = result.Messages[:limit]
		result.NextCursor = result.Messages[len(result.Messages)-1].UID
	}
	return result, nil
}
func (f *nativeAIFixtureMail) GetMessage(_ context.Context, folder string, uid uint32) (*webmail.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msg, ok := f.messages[folder][uid]
	if !ok {
		return nil, webmail.ErrMessageNotFound
	}
	cloned := *msg
	return &cloned, nil
}
func (f *nativeAIFixtureMail) SetFlags(context.Context, string, []uint32, []string, []string) error {
	return nil
}
func (f *nativeAIFixtureMail) SaveDraft(_ context.Context, from string, msg webmail.Outgoing, replace, validity uint32) (webmail.DraftSaveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if replace != 0 && (validity != 9 || f.messages["Drafts"][replace] == nil) {
		return webmail.DraftSaveResult{}, webmail.ErrDraftChanged
	}
	if replace != 0 {
		delete(f.messages["Drafts"], replace)
	}
	f.next++
	uid := f.next
	addrs := func(values []string) []webmail.Address {
		out := make([]webmail.Address, 0, len(values))
		for _, value := range values {
			out = append(out, webmail.Address{Email: value})
		}
		return out
	}
	identity, err := netmail.ParseAddress(msg.From)
	if err != nil {
		identity = &netmail.Address{Address: from}
	}
	f.messages["Drafts"][uid] = &webmail.Message{MessageSummary: webmail.MessageSummary{UID: uid, Subject: msg.Subject, InReplyTo: msg.InReplyTo, From: []webmail.Address{{Name: identity.Name, Email: identity.Address}}, Flags: []string{`\Draft`}, Date: fixtureNow()}, UIDValidity: 9, To: addrs(msg.To), Cc: addrs(msg.Cc), Bcc: addrs(msg.Bcc), Text: msg.Text, HTML: msg.HTML, Attachments: []webmail.AttachmentMeta{}}
	return webmail.DraftSaveResult{Folder: "Drafts", NameEnc: webmail.EncodeFolderName("Drafts"), UID: uid, UIDValidity: 9}, nil
}
func (f *nativeAIFixtureMail) EnsureDraftsFolder(context.Context) (string, error) {
	return "Drafts", nil
}
func (f *nativeAIFixtureMail) AppendPreparedDraft(ctx context.Context, _ string, raw []byte) (webmail.DraftSaveResult, error) {
	parsed, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return webmail.DraftSaveResult{}, err
	}
	out := webmail.Outgoing{From: parsed.Header.Get("From"), Subject: parsed.Header.Get("Subject"), InReplyTo: parsed.Header.Get("In-Reply-To")}
	for _, field := range []struct {
		name   string
		target *[]string
	}{{"To", &out.To}, {"Cc", &out.Cc}, {"Bcc", &out.Bcc}} {
		addresses, _ := parsed.Header.AddressList(field.name)
		for _, address := range addresses {
			*field.target = append(*field.target, address.Address)
		}
	}
	var walk func(textproto.MIMEHeader, io.Reader) error
	walk = func(header textproto.MIMEHeader, body io.Reader) error {
		contentType, params, _ := mime.ParseMediaType(header.Get("Content-Type"))
		if strings.HasPrefix(contentType, "multipart/") {
			reader := multipart.NewReader(body, params["boundary"])
			for {
				part, e := reader.NextPart()
				if errors.Is(e, io.EOF) {
					return nil
				}
				if e != nil {
					return e
				}
				if e = walk(part.Header, part); e != nil {
					return e
				}
			}
		}
		content, e := io.ReadAll(body)
		if e != nil {
			return e
		}
		switch contentType {
		case "text/html":
			out.HTML = string(content)
		case "text/plain":
			out.Text = string(content)
		}
		return nil
	}
	if err = walk(textproto.MIMEHeader(parsed.Header), parsed.Body); err != nil {
		return webmail.DraftSaveResult{}, err
	}
	result, err := f.SaveDraft(ctx, out.From, out, 0, 0)
	if err != nil {
		return result, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.raw == nil {
		f.raw = map[uint32][]byte{}
	}
	f.raw[result.UID] = append([]byte(nil), raw...)
	return result, nil
}
func (f *nativeAIFixtureMail) FindDraftCopy(_ context.Context, folder, id, digest string) (webmail.DraftSaveResult, bool, error) {
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
func (f *nativeAIFixtureMail) GetRawMessage(_ context.Context, folder string, uid uint32) ([]byte, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if folder != "Drafts" || f.raw[uid] == nil {
		return nil, 0, webmail.ErrMessageNotFound
	}
	return append([]byte(nil), f.raw[uid]...), 9, nil
}
func (f *nativeAIFixtureMail) DeleteMessages(_ context.Context, folder string, uids []uint32, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, uid := range uids {
		delete(f.messages[folder], uid)
		if folder == "Drafts" {
			delete(f.raw, uid)
		}
	}
	return nil
}

func (f *nativeAIFixtureMail) Close() error { return nil }

type nativeAIFixtureDialer struct{ client *nativeAIFixtureMail }

func (f nativeAIFixtureDialer) Dial(context.Context, string, string) (webmail.Client, error) {
	return f.client, nil
}

type nativeAIFixtureModel struct {
	calls atomic.Int64
	hook  func()
	// respond optionally overrides the canned answer; the second return
	// value reports whether the override handled the request.
	respond func(request ai.CompletionRequest) (string, bool)
}

func (*nativeAIFixtureModel) Name() string { return "deterministic-test-provider" }
func (model *nativeAIFixtureModel) Complete(ctx context.Context, request ai.CompletionRequest) (ai.CompletionResponse, error) {
	select {
	case <-ctx.Done():
		return ai.CompletionResponse{}, ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}
	model.calls.Add(1)
	if model.respond != nil {
		if content, ok := model.respond(request); ok {
			return ai.CompletionResponse{Content: content, PromptTokens: 12, OutputTokens: 18, TotalTokens: 30, UsageKnown: true}, nil
		}
	}
	// Dispatch on the system prompt's task constraint: user messages are
	// registry-built untrusted-content envelopes, no longer the marshaled
	// operation input.
	system := ""
	if len(request.Messages) > 0 {
		system = request.Messages[0].Content
	}
	var output any
	switch {
	case strings.Contains(system, "Classify the email category"):
		output = map[string]any{"category": "finance", "score": 0.95}
	case strings.Contains(system, "Summarize the key facts"):
		output = map[string]any{"bullets": []string{"Invoice total: 42 euros.", "Payment is due on Friday."}, "todos": []map[string]any{}, "key_dates": []map[string]any{}, "language": "en"}
	case strings.Contains(system, "Translate the email text faithfully"):
		output = map[string]any{"translated_text": "发票总金额为42欧元，请于星期五之前付款。", "detected_source": "en"}
	case strings.Contains(system, "Evaluate the email for phishing"):
		output = map[string]any{"score": 0.2, "reasons": []string{"Verify the sender and invoice details independently."}}
	case strings.Contains(system, "Refine the provided draft"):
		output = map[string]any{"revised_text": "Hello, thank you for the invoice. I will arrange payment of 42 euros by Friday.", "changes": []map[string]any{}, "tone": "neutral"}
	case strings.Contains(system, "Suggest a draft reply"):
		output = map[string]any{"subject": "Invoice follow-up", "body": "Hello, thank you for the invoice. I will arrange payment of 42 euros by Friday.\n\nKind regards"}
	case strings.Contains(system, "Extract concrete action items"):
		output = map[string]any{"items": []map[string]any{{"task": "Pay the invoice by Friday", "assignee": nil, "due_date": "Friday"}}}
	case strings.Contains(system, "Execute the user instruction"):
		output = map[string]any{"task_type": "other", "result": "Done."}
	default:
		return ai.CompletionResponse{}, ai.ErrInvalidRequest
	}
	if model.hook != nil {
		model.hook()
	}
	raw, _ := json.Marshal(output)
	return ai.CompletionResponse{Content: string(raw), PromptTokens: 12, OutputTokens: 18, TotalTokens: 30, UsageKnown: true}, nil
}

func TestNativeAIBrowserFixture(t *testing.T) {
	directory := os.Getenv("URMAIL_AI_BROWSER_FIXTURE_DIR")
	if directory == "" || testing.Short() {
		t.Skip("explicit browser fixture directory required")
	}
	f := newNativeAIHTTPFixture(t)
	r, p, principal, password := f.router, f.pool, f.principal, f.password
	_ = p
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	r.GET("/api/v1/i18n/:locale", func(c *gin.Context) {
		locale := c.Param("locale")
		if locale != "en" && locale != "zh-CN" {
			c.Status(404)
			return
		}
		c.File(filepath.Join(root, "internal/i18n/locales", locale+".json"))
	})
	r.GET("/api/v1/branding", func(c *gin.Context) { c.JSON(200, gin.H{"name": "urMail", "primary_color": "#2563eb"}) })
	r.Static("/assets", filepath.Join(root, "web/dist/assets"))
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(404, gin.H{"error": gin.H{"code": "NOT_FOUND"}})
			return
		}
		c.File(filepath.Join(root, "web/dist/index.html"))
	})
	server := httptest.NewServer(r)
	defer server.Close()
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(map[string]any{"url": server.URL, "address": principal.Address, "password": password, "tenant_id": principal.TenantID})
	if err = os.WriteFile(filepath.Join(directory, "fixture.json"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("NATIVE_AI_BROWSER_READY " + server.URL)
	deadline := time.NewTimer(25 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser fixture deadline elapsed")
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(directory, "stop")); err == nil {
				return
			}
		}
	}
}

type nativeAIHTTPFixture struct {
	router    *gin.Engine
	pool      *db.Pool
	principal store.PlatformConsumeMailboxTicketRow
	password  string
	mail      *nativeAIFixtureMail
	model     *nativeAIFixtureModel
	native    *MailboxNativeHandler
	aiService *MailboxAIService
	search    *AISearchAsk
}

func newNativeAIHTTPFixture(t *testing.T) nativeAIHTTPFixture {
	p, principal := mailboxAIFixture(t, 1000)
	seatAIPlatformBudget(t, p, principal.TenantID)
	password := "native-ai-fixture-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	hash, err := domainMail.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Exec(t.Context(), "UPDATE mailboxes SET password_hash=$1 WHERE tenant_id=$2 AND id=$3", hash, principal.TenantID, principal.MailboxID); err != nil {
		t.Fatal(err)
	}
	mail := &nativeAIFixtureMail{next: 1000, messages: map[string]map[uint32]*webmail.Message{"INBOX": {5: {MessageSummary: webmail.MessageSummary{UID: 5, MessageID: "<invoice@ai.example.test>", Subject: "Invoice for October — 42 euros", From: []webmail.Address{{Name: "Billing team", Email: "billing@example.test"}}, Flags: []string{`\Seen`}, Date: fixtureNow(), Size: 120}, UIDValidity: 9, To: []webmail.Address{{Email: principal.Address}}, Text: "The October invoice total is 42 euros. Payment is due on Friday. Please confirm the bank details independently.", Attachments: []webmail.AttachmentMeta{}}}, "Drafts": {}}}
	q := store.New(p)
	sessions := StoreMailboxNative{Q: q}
	vault := webmail.NewMemoryVault()
	native := &MailboxNativeHandler{Enabled: true, Pool: p, Auth: sessions, Sessions: sessions, Vault: vault, Secrets: newTestSecrets(t), Dialer: nativeAIFixtureDialer{mail}, SelfSettings: StoreMailboxSelfSettings{Pool: p}, Rules: StoreMailboxRules{Pool: p}, Sieve: &fakeActivator{}}
	model := &nativeAIFixtureModel{}
	service := &MailboxAIService{Pool: p, Provider: func(store.AiProviderConfigs) (ai.Provider, error) { return model, nil }}
	answer := &AISearchAsk{Pool: p, Dialer: native.Dialer, Vault: vault, Accounting: AISearchAskAccounting{Pool: p}, ModelFor: func(context.Context, int64) (aianswer.Model, error) {
		return func(_ context.Context, _ string, sources []aianswer.Source) (string, []string, error) {
			if len(sources) == 0 {
				return "No supporting mail", nil, nil
			}
			return "The invoice total is 42 euros.", []string{sources[0].ID}, nil
		}, nil
	}}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/api/v1/mailbox/status", native.Status)
	r.POST("/api/v1/mailbox/session", native.CreateSession)
	r.DELETE("/api/v1/mailbox/session", native.DeleteSession)
	authed := r.Group("/api/v1/mailbox")
	authed.Use(middleware.RequireMailboxAuth(sessions, vault))
	authed.GET("/folders", native.ListFolders)
	authed.GET("/folders/:name/messages", native.ListMessages)
	authed.GET("/folders/:name/messages/:uid", native.GetMessage)
	authed.POST("/folders/:name/messages/flags", native.FlagMessages)
	authed.PUT("/drafts", native.SaveDraft)
	authed.DELETE("/drafts/:uid", native.DeleteDraft)
	authed.GET("/send-receipts", native.ListSendReceipts)
	authed.GET("/send-receipts/:id", native.GetSendReceipt)
	authed.POST("/send-receipts/:id/repair-sent-copy", native.RepairSendReceiptCopy)
	authed.GET("/settings", native.GetSettings)
	authed.PUT("/settings", native.PutSettings)
	authed.GET("/identities", native.ListIdentities)
	authed.GET("/quota", native.GetQuota)
	authed.GET("/rules", native.GetRules)
	authed.PUT("/rules", native.PutRules)
	authed.POST("/send", native.SendMessage)

	authed.GET("/drafts", native.ListDrafts)
	RegisterNativeMailboxAI(authed, native, service, answer)
	authed.POST("/ai/search-ask", answer.Handle)
	authed.GET("/ai/search-ask/status", answer.Status)
	authed.POST("/ai/search-ask/clear", answer.Clear)
	authed.DELETE("/ai/search-ask/index", answer.Clear)
	authed.GET("/ai/index/status", answer.Status)
	authed.POST("/ai/index/clear", answer.Clear)
	authed.DELETE("/ai/index", answer.Clear)
	authed.GET("/ai/budget", (&AISearchAskAccounting{Pool: p}).BudgetStatus)
	return nativeAIHTTPFixture{router: r, pool: p, principal: principal, password: password, mail: mail, model: model, native: native, aiService: service, search: answer}
}

// fixtureNow returns a pointer date for MessageSummary fixtures.
func fixtureNow() *time.Time {
	t := time.Now()
	return &t
}
