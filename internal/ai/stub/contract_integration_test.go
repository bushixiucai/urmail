// Package stub provides HTTP stubs for AI provider contract tests.
package stub

import (
	"context"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai"
)

func TestModelStub_DedicatedTaskPromptsAndMinimalPayload(t *testing.T) {
	stub := NewModelStub()
	defer stub.Close()

	cases := []struct {
		op          string
		instruction string
		body        string
		lang        string
		expectSub   string
	}{
		{"summarize", "", "The project launched on Friday with 5 members.", "", "Summarize the key facts strictly"},
		{"todos", "", "Bob needs to finish the audit by Oct 31.", "", "Extract concrete action items"},
		{"translate", "", "Invoice amount is $100.00 USD.", "zh-CN", "Preserve formatting, proper nouns, numbers, currencies"},
		{"phishing", "", "Urgent: Click here to verify password immediately.", "", "Evaluate the email for phishing or security risks based on verifiable evidence"},
		{"custom", "Extract all email addresses and phone numbers.", "Contact support@example.com or call 555-1234.", "", "Execute the user instruction regarding the email"},
	}

	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			stub.Reset()
			req, err := ai.BuildOperation(ai.OperationInput{
				Operation:      tc.op,
				Instruction:    tc.instruction,
				Body:           tc.body,
				TargetLanguage: tc.lang,
			})
			if err != nil {
				t.Fatalf("BuildOperation failed: %v", err)
			}

			// System prompt must contain specific task constraint
			sysPrompt := req.Messages[0].Content
			if !strings.Contains(sysPrompt, tc.expectSub) {
				t.Errorf("system prompt missing %q, got: %s", tc.expectSub, sysPrompt)
			}

			// User message must not contain draft field
			userMsg := req.Messages[1].Content
			if strings.Contains(userMsg, "\"draft\"") {
				t.Errorf("user message should omit empty draft, got: %s", userMsg)
			}
		})
	}
}

func TestModelStub_ProviderErrorsMappedCorrectly(t *testing.T) {
	stub := NewModelStub()
	defer stub.Close()

	provider, err := stub.Provider("sk-test", "gpt-4o")
	if err != nil {
		t.Fatalf("provider failed: %v", err)
	}

	// 1. Rate Limit 429
	stub.SetRateLimitResponse()
	_, err = provider.Complete(context.Background(), ai.CompletionRequest{
		Messages: []ai.Message{{Role: "user", Content: "test"}},
	})
	if err == nil {
		t.Fatal("expected 429 rate limit error")
	}
	if ai.ClassifyFailure(err) != ai.ClassRateLimited {
		t.Errorf("expected ClassRateLimited, got: %v", ai.ClassifyFailure(err))
	}

	// 2. Server Error 500
	stub.SetServerErrorResponse()
	_, err = provider.Complete(context.Background(), ai.CompletionRequest{
		Messages: []ai.Message{{Role: "user", Content: "test"}},
	})
	if err == nil {
		t.Fatal("expected 500 server error")
	}
	if ai.ClassifyFailure(err) != ai.ClassUnavailable {
		t.Errorf("expected ClassUnavailable, got: %v", ai.ClassifyFailure(err))
	}

	// 3. Unauthorized 401
	stub.SetUnauthorizedResponse()
	_, err = provider.Complete(context.Background(), ai.CompletionRequest{
		Messages: []ai.Message{{Role: "user", Content: "test"}},
	})
	if err == nil {
		t.Fatal("expected 401 unauthorized error")
	}
	if ai.ClassifyFailure(err) != ai.ClassFatal {
		t.Errorf("expected ClassFatal, got: %v", ai.ClassifyFailure(err))
	}
}
