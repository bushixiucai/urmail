package mailspam

import (
	"testing"
)

func normalMessage() Message {
	return Message{
		EnvelopeFrom: "colleague@partner.example",
		Headers: []Header{
			{Name: "From", Value: "Colleague <colleague@partner.example>"},
			{Name: "Subject", Value: "Quarterly planning notes"},
			{Name: "Date", Value: "Mon, 26 Oct 2026 10:00:00 +0800"},
			{Name: "Message-ID", Value: "<q3-notes@partner.example>"},
			{Name: "Authentication-Results", Value: "mx.urmail.cn; spf=pass smtp.mailfrom=partner.example; dkim=pass header.d=partner.example; dmarc=pass header.from=partner.example"},
		},
		BodySample: []byte("Hi, attaching the planning notes for Thursday. Regards, Wei"),
	}
}

func trustedAuth() AuthContext {
	return AuthContext{LocalDomains: []string{"urmail.cn"}}
}

func TestClassifyNormalMailIsHam(t *testing.T) {
	headers := normalMessage().Headers
	auth := trustedAuth()
	auth.Results = ParseAuthResults(headers, []string{"mx.urmail.cn"})
	verdict := Classify(normalMessage(), auth, ListSnapshot{})
	if verdict.Class != ClassHam {
		t.Fatalf("normal mail classified %q score=%.2f reasons=%v", verdict.Class, verdict.Score, verdict.Reasons)
	}
	if verdict.Score >= 0 {
		t.Fatalf("authenticated normal mail should land at or below zero, got %.2f", verdict.Score)
	}
	if !containsReason(verdict.Reasons, reasonSPFPass) || !containsReason(verdict.Reasons, reasonDKIMPass) {
		t.Fatalf("pass signals missing from reasons: %v", verdict.Reasons)
	}
}

func TestClassifyHeaderlessSpamIsUnsureOrSpamNotDropped(t *testing.T) {
	// No authentication, no structural headers: unscored absence keeps this
	// out of spam on its own; the verdict must still be produced and the
	// message must never be silently discarded by design.
	msg := Message{
		EnvelopeFrom: "unknown@bulk.example",
		Headers:      []Header{{Name: "From", Value: "unknown@bulk.example"}},
		BodySample:   []byte("please buy now"),
	}
	verdict := Classify(msg, AuthContext{}, ListSnapshot{})
	switch verdict.Class {
	case ClassHam, ClassUnsure, ClassSpam:
	default:
		t.Fatalf("impossible class %q", verdict.Class)
	}
	if verdict.Class == ClassSpam {
		t.Fatalf("structural absence alone must not reach spam: %.2f %v", verdict.Score, verdict.Reasons)
	}
}

func TestDenyListSenderIsSpamRegardlessOfContent(t *testing.T) {
	msg := normalMessage()
	lists := ListSnapshot{Rules: []SenderRule{{List: "deny", Scope: "mailbox", Address: "colleague@partner.example"}}}
	verdict := Classify(msg, AuthContext{}, lists)
	if verdict.Class != ClassSpam {
		t.Fatalf("denied sender classified %q", verdict.Class)
	}
	if !containsReason(verdict.Reasons, reasonSenderDeniedMailbox) {
		t.Fatalf("deny reason missing: %v", verdict.Reasons)
	}
	if verdict.Score < WeightSenderList {
		t.Fatalf("deny rule score %.2f below documented weight", verdict.Score)
	}
}

func TestDenyBySenderDomainMatchesFromHeaderDomain(t *testing.T) {
	msg := normalMessage()
	lists := ListSnapshot{Rules: []SenderRule{{List: "deny", Scope: "mailbox", Domain: "partner.example"}}}
	verdict := Classify(msg, AuthContext{}, lists)
	if verdict.Class != ClassSpam || !containsReason(verdict.Reasons, reasonSenderDeniedMailbox) {
		t.Fatalf("domain deny not applied: %v %.2f", verdict.Reasons, verdict.Score)
	}
}

func TestAllowListOverridesSpammyContent(t *testing.T) {
	msg := normalMessage()
	msg.BodySample = []byte("FREE MONEY click http://a.example http://b.example http://c.example http://d.example http://e.example http://f.example NOW!!!")
	msg.Headers = append(msg.Headers[:2], msg.Headers[4:]...) // drop Date/Message-ID
	lists := ListSnapshot{Rules: []SenderRule{{List: "allow", Scope: "mailbox", Address: "colleague@partner.example"}}}
	verdict := Classify(msg, AuthContext{}, lists)
	if verdict.Class != ClassHam || !containsReason(verdict.Reasons, reasonSenderAllowedMailbox) {
		t.Fatalf("allow-list mail not ham: %v %.2f", verdict.Reasons, verdict.Score)
	}
}

func TestDenyWinsOverAllowAtSameScope(t *testing.T) {
	msg := normalMessage()
	lists := ListSnapshot{Rules: []SenderRule{
		{List: "allow", Scope: "mailbox", Address: "colleague@partner.example"},
		{List: "deny", Scope: "mailbox", Address: "colleague@partner.example"},
	}}
	verdict := Classify(msg, AuthContext{}, lists)
	if verdict.Class != ClassSpam || !containsReason(verdict.Reasons, reasonSenderDeniedMailbox) {
		t.Fatalf("deny must win at equal scope: %v", verdict.Reasons)
	}
}

func TestMailboxScopeShadowsTenantScope(t *testing.T) {
	msg := normalMessage()
	lists := ListSnapshot{Rules: []SenderRule{
		{List: "deny", Scope: "tenant", Address: "colleague@partner.example"},
		{List: "allow", Scope: "mailbox", Address: "colleague@partner.example"},
	}}
	verdict := Classify(msg, AuthContext{}, lists)
	if verdict.Class != ClassHam || !containsReason(verdict.Reasons, reasonSenderAllowedMailbox) {
		t.Fatalf("mailbox allow must shadow tenant deny: %v", verdict.Reasons)
	}

	lists.Rules = []SenderRule{
		{List: "allow", Scope: "tenant", Address: "colleague@partner.example"},
		{List: "deny", Scope: "mailbox", Address: "colleague@partner.example"},
	}
	if verdict := Classify(msg, AuthContext{}, lists); verdict.Class != ClassSpam {
		t.Fatalf("mailbox deny must shadow tenant allow: %v", verdict.Reasons)
	}
}

func TestTenantScopeFallback(t *testing.T) {
	msg := normalMessage()
	lists := ListSnapshot{Rules: []SenderRule{{List: "deny", Scope: "tenant", Domain: "partner.example"}}}
	if verdict := Classify(msg, AuthContext{}, lists); verdict.Class != ClassSpam || !containsReason(verdict.Reasons, reasonSenderDeniedTenant) {
		t.Fatalf("tenant deny fallback missing: %v", verdict.Reasons)
	}
	lists.Rules = []SenderRule{{List: "allow", Scope: "tenant", Address: "colleague@partner.example"}}
	if verdict := Classify(msg, AuthContext{}, lists); verdict.Class != ClassHam || !containsReason(verdict.Reasons, reasonSenderAllowedTenant) {
		t.Fatalf("tenant allow fallback missing: %v", verdict.Reasons)
	}
}

func TestLearnedTokenStatisticsInfluenceVerdict(t *testing.T) {
	msg := normalMessage()
	// With enough ham observations, message tokens push toward ham.
	tokens := map[string]TokenCount{
		"planning": {Spam: 0, Ham: 9},
		"thursday": {Spam: 1, Ham: 12},
	}
	ham := Classify(msg, AuthContext{}, ListSnapshot{Tokens: tokens})
	baseline := Classify(msg, AuthContext{}, ListSnapshot{})
	if ham.Score >= baseline.Score {
		t.Fatalf("ham-trained tokens did not lower score: %.2f vs %.2f", ham.Score, baseline.Score)
	}
	if !containsReason(ham.Reasons, reasonTokenHam) {
		t.Fatalf("token ham reason missing: %v", ham.Reasons)
	}

	spamTokens := map[string]TokenCount{
		"planning": {Spam: 12, Ham: 0},
		"thursday": {Spam: 14, Ham: 1},
		"notes":    {Spam: 11, Ham: 0},
	}
	spammy := Classify(msg, AuthContext{}, ListSnapshot{Tokens: spamTokens})
	if spammy.Score <= baseline.Score {
		t.Fatalf("spam-trained tokens did not raise score: %.2f vs %.2f", spammy.Score, baseline.Score)
	}
	if !containsReason(spammy.Reasons, reasonTokenSpam) {
		t.Fatalf("token spam reason missing: %v", spammy.Reasons)
	}
}

func TestTokenCapAndMinimumObservations(t *testing.T) {
	msg := Message{EnvelopeFrom: "a@b.example", Headers: []Header{{Name: "Subject", Value: "winner winner"}}}
	// Below the observation floor the token is ignored entirely.
	quiet := Classify(msg, AuthContext{}, ListSnapshot{Tokens: map[string]TokenCount{"winner": {Spam: 2, Ham: 0}}})
	baseline := Classify(msg, AuthContext{}, ListSnapshot{})
	if quiet.Score != baseline.Score {
		t.Fatalf("token below observation floor influenced verdict")
	}
	// Even huge counts are capped at TokenWeightCap.
	heavy := Classify(msg, AuthContext{}, ListSnapshot{Tokens: map[string]TokenCount{"winner": {Spam: 100000, Ham: 0}}})
	if heavy.Score-baseline.Score > TokenWeightCap+0.001 {
		t.Fatalf("token contribution exceeded cap: %.2f", heavy.Score-baseline.Score)
	}
}

func TestSASLLocalDomainSignal(t *testing.T) {
	msg := Message{
		EnvelopeFrom: "wei@urmail.cn",
		Headers: []Header{
			{Name: "From", Value: "Wei <wei@urmail.cn>"},
			{Name: "Subject", Value: "Lunch tomorrow"},
		},
		BodySample: []byte("see you at noon"),
	}
	auth := AuthContext{SASLUser: "wei@urmail.cn", LocalDomains: []string{"urmail.cn"}}
	verdict := Classify(msg, auth, ListSnapshot{})
	if !containsReason(verdict.Reasons, reasonSASLLocalDomain) {
		t.Fatalf("sasl local domain signal missing: %v", verdict.Reasons)
	}
}

func TestParseAuthResultsTrustBoundary(t *testing.T) {
	headers := []Header{
		// Forged self-award must be ignored.
		{Name: "Authentication-Results", Value: "evil.example; dkim=pass header.d=evil.example; spf=pass"},
		// Trusted header is parsed fully.
		{Name: "Authentication-Results", Value: "mx.urmail.cn; spf=fail smtp.mailfrom=bulk.example; dkim=pass header.d=bulk.example; dmarc=reject header.from=bulk.example"},
		// Malformed contributes nothing.
		{Name: "Authentication-Results", Value: "no-semicolon-here"},
		{Name: "Subject", Value: "unrelated"},
	}
	results := ParseAuthResults(headers, []string{"MX.urmail.cn"})
	if len(results) != 3 {
		t.Fatalf("expected 3 trusted statements, got %d: %+v", len(results), results)
	}
	if results[0].Method != "spf" || results[0].Result != "fail" {
		t.Fatalf("spf statement mis-parsed: %+v", results[0])
	}
	if results[1].Method != "dkim" || results[1].Result != "pass" {
		t.Fatalf("dkim statement mis-parsed: %+v", results[1])
	}
	if results[2].Method != "dmarc" || results[2].Result != "reject" {
		t.Fatalf("dmarc statement mis-parsed: %+v", results[2])
	}
	if none := ParseAuthResults(headers, nil); none != nil {
		t.Fatalf("no trusted ids must disable authentication evidence: %+v", none)
	}

	// The forged pass must not soften the verdict.
	msg := normalMessage()
	msg.Headers = headers
	auth := AuthContext{}
	auth.Results = ParseAuthResults(headers, []string{"mx.urmail.cn"})
	verdict := Classify(msg, auth, ListSnapshot{})
	if !containsReason(verdict.Reasons, reasonSPFFail) || !containsReason(verdict.Reasons, reasonDMARCReject) {
		t.Fatalf("trusted failure statements not applied: %v", verdict.Reasons)
	}
	for _, reason := range verdict.Reasons {
		if reason == reasonSPFPass {
			// spf=pass exists ONLY in the forged (untrusted) header.
			t.Fatalf("forged pass leaked into verdict: %v", verdict.Reasons)
		}
	}
}

func TestClassifyIsDeterministic(t *testing.T) {
	msg := normalMessage()
	auth := trustedAuth()
	auth.Results = ParseAuthResults(msg.Headers, []string{"mx.urmail.cn"})
	lists := ListSnapshot{
		Rules:  []SenderRule{{List: "allow", Scope: "tenant", Domain: "partner.example"}, {List: "deny", Scope: "mailbox", Domain: "other.example"}},
		Tokens: map[string]TokenCount{"planning": {Spam: 4, Ham: 2}},
	}
	first := Classify(msg, auth, lists)
	for i := 0; i < 25; i++ {
		again := Classify(msg, auth, lists)
		if again.Class != first.Class || again.Score != first.Score || !slicesEqual(again.Reasons, first.Reasons) {
			t.Fatalf("verdict not deterministic: %+v vs %+v", again, first)
		}
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestVerdictHeadersRendering(t *testing.T) {
	cases := []struct {
		class Class
		flag  string
	}{
		{ClassSpam, "yes"},
		{ClassHam, "no"},
		{ClassUnsure, "unsure"},
	}
	for _, tc := range cases {
		headers := VerdictHeaders(Verdict{Class: tc.class, Score: 5.25})
		if len(headers) != 2 {
			t.Fatalf("expected 2 headers, got %d", len(headers))
		}
		if headers[0][0] != HeaderNameSpam || headers[0][1] != tc.flag {
			t.Fatalf("%s: wrong flag header %+v", tc.class, headers[0])
		}
		if headers[1][0] != HeaderNameScore || headers[1][1] != "5.25" {
			t.Fatalf("%s: wrong score header %+v", tc.class, headers[1])
		}
	}
}

func TestFromDomainFallbacks(t *testing.T) {
	msg := Message{EnvelopeFrom: "", Headers: []Header{{Name: "From", Value: "Alice <alice@fallback.example>"}}}
	if got := msg.FromDomain(); got != "fallback.example" {
		t.Fatalf("From-header fallback wrong: %q", got)
	}
	msg = Message{EnvelopeFrom: "ENVELOPE@Fallback.Example"}
	if got := msg.FromDomain(); got != "fallback.example" {
		t.Fatalf("envelope fallback wrong: %q", got)
	}
}

func containsReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
