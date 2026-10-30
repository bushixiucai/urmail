package stub

import (
	"context"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
)

func TestModelStub_CapturesAndInspects(t *testing.T) {
	stub := NewModelStub()
	defer stub.Close()

	stub.SetCompletionResponse(`{"summary":"Test summary"}`)

	provider, err := stub.Provider("sk-test", "gpt-4o")
	if err != nil {
		t.Fatalf("configured provider failed: %v", err)
	}

	res, err := provider.Complete(context.Background(), ai.CompletionRequest{
		Messages: []ai.Message{
			{Role: "user", Content: "Hello model"},
		},
	})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	if res.Content != `{"summary":"Test summary"}` {
		t.Errorf("unexpected content: %s", res.Content)
	}

	req := stub.LastRequest()
	if req == nil {
		t.Fatal("expected request recorded")
	}
	if req.Headers.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("expected Bearer sk-test, got %s", req.Headers.Get("Authorization"))
	}
	if !stub.LastBodyContains("Hello model") {
		t.Error("expected body to contain prompt")
	}
}
