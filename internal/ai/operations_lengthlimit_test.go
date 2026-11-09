package ai

// DEF-A regression (v0.6.13): the summarize "shorter" control used to die
// at the handler boundary — length_limit was never parsed, so the prompt
// never carried the cap and the result-cache digest could not tell a
// capped call from a default one. These tests pin the normalization, the
// prompt injection and the template-version bump the cache relies on.

import (
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/ai/templates"
)

func TestNormalizeLengthLimit(t *testing.T) {
	cases := []struct {
		raw, want int
	}{
		{0, 0},
		{100, 100},
		{-1, 0},
		{-1000, 0},
		{LengthLimitCeiling, LengthLimitCeiling},
		{LengthLimitCeiling + 1, LengthLimitCeiling},
		{1 << 20, LengthLimitCeiling},
	}
	for _, tc := range cases {
		if got := NormalizeLengthLimit(tc.raw); got != tc.want {
			t.Fatalf("NormalizeLengthLimit(%d)=%d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestSummarizeLengthLimitReachesPrompt(t *testing.T) {
	base := OperationInput{Operation: "summarize", Body: "invoice body"}
	capped := base
	capped.LengthLimit = 100

	reqDefault, err := StructuredRequest(base)
	if err != nil {
		t.Fatalf("build default: %v", err)
	}
	reqCapped, err := StructuredRequest(capped)
	if err != nil {
		t.Fatalf("build capped: %v", err)
	}
	systemDefault := reqDefault.Messages[0].Content
	systemCapped := reqCapped.Messages[0].Content

	if strings.Contains(systemDefault, "at most") {
		t.Fatalf("default prompt carries a length cap: %q", systemDefault)
	}
	if !strings.Contains(systemCapped, "at most 100 characters") {
		t.Fatalf("capped prompt lacks the bullet cap: %q", systemCapped)
	}
}

// The version bump is the cache-side half of the fix: the key rides
// operation:v<version>:digest, so a stale v2 answer can never satisfy a
// v3 capped request even before the digest differs.
func TestSummarizeTemplateVersionBumped(t *testing.T) {
	if got := StructuredTemplateVersion("summarize"); got != 3 {
		t.Fatalf("summarize template version %d, want 3", got)
	}
	if got := templates.Version("summarize_v2"); got != 3 {
		t.Fatalf("summarize_v2 registered version %d, want 3", got)
	}
}

// Negative and over-ceiling client values must not split the cache: after
// normalization they marshal to the identical OperationInput.
func TestNormalizedLengthLimitMarshalsIdentically(t *testing.T) {
	neg := OperationInput{Operation: "summarize", Body: "b", LengthLimit: -5}
	zero := OperationInput{Operation: "summarize", Body: "b"}
	neg.LengthLimit = NormalizeLengthLimit(neg.LengthLimit)
	if neg.LengthLimit != zero.LengthLimit {
		t.Fatalf("normalized %d != default %d", neg.LengthLimit, zero.LengthLimit)
	}
}
