package mailspam

import (
	"strings"
	"testing"
)

func TestExtractTokensDeterministicAndBounded(t *testing.T) {
	in := TokenInput{
		Subject:    "WIN a FREE prize!!!",
		FromDomain: "bulk.example",
		Body:       []byte("<p>Click http://spam.example/win?x=1 now</p> &amp; win <b>prizes</b> &#65; more text"),
	}
	first := ExtractTokens(in)
	for i := 0; i < 20; i++ {
		if !slicesEqual(ExtractTokens(in), first) {
			t.Fatal("tokenizer not deterministic")
		}
	}
	joined := strings.Join(first, ",")
	for _, want := range []string{"win", "free", "prize", "dom:bulk", "dom:example", "url:spam.example"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("token %q missing from %v", want, first)
		}
	}
	// HTML tags and numeric entities must not leak into the vocabulary.
	for _, banned := range []string{"<p>", "b>", "&#65;"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("markup leaked into tokens: %v", first)
		}
	}
}

func TestExtractTokensLengthBounds(t *testing.T) {
	in := TokenInput{Subject: "ab abc " + strings.Repeat("x", 40) + " ok"}
	tokens := ExtractTokens(in)
	joined := strings.Join(tokens, ",")
	if strings.Contains(joined, ",ab,") || strings.HasPrefix(joined, "ab,") || strings.HasSuffix(joined, ",ab") {
		t.Fatalf("too-short token kept: %v", tokens)
	}
	if strings.Contains(joined, strings.Repeat("x", 40)) {
		t.Fatalf("too-long token kept: %v", tokens)
	}
	for _, token := range tokens {
		if len(token) < TokenMinLen || len(token) > TokenMaxLen {
			t.Fatalf("token %q outside bounds", token)
		}
	}
}

func TestExtractTokensCapPerMessage(t *testing.T) {
	body := strings.Repeat("alpha beta gamma delta epsilon ", 500)
	tokens := ExtractTokens(TokenInput{Body: []byte(body)})
	if len(tokens) > TokenMaxPerMessage {
		t.Fatalf("token cap exceeded: %d", len(tokens))
	}
}

func TestLearnFromFeedbackMovesOneCount(t *testing.T) {
	spam := LearnFromFeedback("Cheap watches now", "sales@bulk.example", "spam")
	if len(spam) == 0 {
		t.Fatal("no learning deltas produced")
	}
	for token, delta := range spam {
		if delta.Spam != 1 || delta.Ham != 0 {
			t.Fatalf("token %q delta wrong: %+v", token, delta)
		}
	}
	ham := LearnFromFeedback("Cheap watches now", "sales@bulk.example", "ham")
	for token, delta := range ham {
		if delta.Ham != 1 || delta.Spam != 0 {
			t.Fatalf("token %q ham delta wrong: %+v", token, delta)
		}
	}
	if unknown := LearnFromFeedback("x", "a@b.example", "maybe"); len(unknown) != 0 {
		t.Fatalf("invalid verdict produced deltas: %v", unknown)
	}
	// The sender domain token is part of the learned vocabulary.
	found := false
	for token := range spam {
		if token == "dom:bulk" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sender domain token not learned: %v", spam)
	}
}

func TestLearnFromFeedbackCap(t *testing.T) {
	subject := strings.Repeat("word ", 200)
	deltas := LearnFromFeedback(subject, "someone@example.test", "spam")
	if len(deltas) > TokenMaxLearnedPerFeedback {
		t.Fatalf("learning cap exceeded: %d", len(deltas))
	}
}

func TestLogOddsMonotonic(t *testing.T) {
	zero := logOdds(TokenCount{Spam: 0, Ham: 0})
	if zero != 0 {
		t.Fatalf("unbiased token must have zero log-odds, got %f", zero)
	}
	moreSpam := logOdds(TokenCount{Spam: 5, Ham: 1})
	moreHam := logOdds(TokenCount{Spam: 1, Ham: 5})
	if moreSpam <= 0 || moreHam >= 0 || moreSpam <= moreHam {
		t.Fatalf("log-odds not monotonic: %f %f", moreSpam, moreHam)
	}
}
