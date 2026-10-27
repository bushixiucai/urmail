package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
	"github.com/bushixiucai/urmail/internal/airetrieval"
	"github.com/bushixiucai/urmail/internal/domain/aianswer"
	"github.com/bushixiucai/urmail/internal/webmail"
)

// askErrorOnce builds a grounded search-ask route whose citation model always
// returns modelErr, then asks one question and returns the response.
func askErrorOnce(t *testing.T, modelErr error) *httptest.ResponseRecorder {
	t.Helper()
	source := &airetrieval.FakeSource{Mail: map[string]map[uint32]string{"INBOX": {5: "invoice total 42"}}, Validity: map[string]uint32{"INBOX": 4}}
	store := airetrieval.NewFakeStore()
	principal := airetrieval.Principal{TenantID: 7, MailboxID: 9}
	if _, err := (airetrieval.Indexer{Store: store, Source: source}).Reindex(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	model := func(context.Context, string, []aianswer.Source) (string, []string, error) { return "", nil, modelErr }
	handler := &AISearchAsk{Store: store, Dialer: fakeAskDialer{client: &fakeAskIMAP{messages: map[string]map[uint32]string{"INBOX": {5: "invoice total 42"}}, validity: map[string]uint32{"INBOX": 4}}}, Vault: webmail.NewMemoryVault(), ModelFor: func(context.Context, int64) (aianswer.Model, error) { return model, nil }}
	r := newAskRouter(t, func(vault webmail.CredentialVault) *AISearchAsk { handler.Vault = vault; return handler })
	native := loginNative(t, r, "alice@example.test", "correct-horse-battery")
	return askOnce(t, r, native, "invoice")
}

func TestAISearchAskRateLimitedSurfacesDistinctCode(t *testing.T) {
	rec := askErrorOnce(t, ai.ErrRateLimitExceeded)
	if rec.Code != http.StatusTooManyRequests || !contains(rec.Body.String(), "AI_PROVIDER_RATE_LIMITED") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestAISearchAskUnavailableSurfacesDistinctCode(t *testing.T) {
	rec := askErrorOnce(t, ai.ErrProviderUnavailable)
	if rec.Code != http.StatusServiceUnavailable || !contains(rec.Body.String(), "AI_PROVIDER_UNAVAILABLE") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestAISearchAskTimeoutSurfacesUnavailableCode(t *testing.T) {
	rec := askErrorOnce(t, context.DeadlineExceeded)
	if rec.Code != http.StatusServiceUnavailable || !contains(rec.Body.String(), "AI_PROVIDER_UNAVAILABLE") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestAISearchAskFatalProviderErrorKeepsLegacyCode(t *testing.T) {
	rec := askErrorOnce(t, ai.ErrInvalidResponse)
	if rec.Code != http.StatusBadGateway || !contains(rec.Body.String(), "AI_SEARCH_ASK_UNAVAILABLE") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
