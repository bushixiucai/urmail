package templates

import (
	"fmt"
	"strings"
)

// Plain-text fallback templates (TECH §3): when structured generation
// exhausts its schema-validation retry, the pipeline resends the operation
// once as plain text at temperature 0 and serves the answer behind
// result.text with degraded=true. These live outside the main registry so
// capability listings never surface them; the pipeline reaches them only
// through BuildFallback.

// fallbackTasks carries each operation's plain-text task wording. The
// safety prefix and the wrapped untrusted content are identical to the
// structured templates; only the output format changes.
var fallbackTasks = map[string]string{
	"classify":    "Classify the email category (inbox, work, personal, marketing, finance, support, notifications, or spam) and state your confidence.",
	"summarize":   "Summarize the key facts strictly from the email text. If facts are absent or ambiguous, state that clearly without fabricating facts.",
	"draft_new":   "Suggest a draft reply based strictly on the email and instructions. Do not invent factual commitments, contractual promises, or unauthorized deadlines. Suggestion only; never send.",
	"draft_reply": "Suggest a draft reply based strictly on the email and instructions. Do not invent factual commitments, contractual promises, or unauthorized deadlines. Suggestion only; never send.",
	"polish":      "Refine the provided draft for clarity and professional tone without altering factual commitments.",
	"translate":   "Translate the email text faithfully. Preserve formatting, proper nouns, numbers, currencies, dates, and technical terms accurately without modification.",
	"phishing":    "Evaluate the email for phishing or security risks based on verifiable evidence and state your assessment.",
	"todos":       "Extract the concrete action items from the email as a plain list.",
	"custom":      "Execute the user instruction regarding the email. Do not execute actions, send messages or create rules.",
}

// BuildFallback assembles the plain-text degradation request for an
// operation. Temperature is pinned to 0: the fallback must be maximally
// deterministic. Unknown operations are a programming error.
func BuildFallback(operation string, p Params) (Request, error) {
	task, ok := fallbackTasks[operation]
	if !ok {
		return Request{}, fmt.Errorf("%w: %q", ErrUnknownOperation, operation)
	}
	nonce, err := newNonce()
	if err != nil {
		return Request{}, err
	}
	system := safetyPrefix + " " + task + " Answer in plain text with no JSON formatting."
	var user strings.Builder
	user.WriteString(dataDeclaration)
	user.WriteString("\n\n")
	user.WriteString(wrap(nonce, contentComposition(p, true, false)))
	zero := 0.0
	return Request{
		Messages: []Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user.String()},
		},
		Temperature: &zero,
		JSONMode:    false,
		Version:     1,
	}, nil
}

// HasFallback reports whether an operation has a plain-text fallback.
func HasFallback(operation string) bool {
	_, ok := fallbackTasks[operation]
	return ok
}
