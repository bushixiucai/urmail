// Package mailspam implements the MAIL-002 self-hosted spam verdict service.
//
// The verdict engine in this file is a PURE function: message sample +
// authentication context + per-mailbox/per-tenant policy snapshot in, verdict
// out. It never performs I/O, never resolves DNS (no DNSBL in v1) and never
// executes external programs, so it is deterministic and unit-testable with
// fixtures.
//
// The milter transport (milter.go/server.go) only ever ADDS headers and, when
// explicitly enabled by configuration (default OFF), rejects. Nothing in this
// package discards or silently drops mail.
package mailspam

import (
	"net/mail"
	"sort"
	"strings"
)

// Class is the final classification.
type Class string

// Classification values. A ham verdict delivers normally, spam is filed to
// Junk by the global sieve before-script, unsure delivers normally with the
// score header present for clients and users to act on.
const (
	ClassHam    Class = "ham"
	ClassSpam   Class = "spam"
	ClassUnsure Class = "unsure"
)

// Classification thresholds. A score at or above SpamScoreThreshold is spam,
// at or above UnsureScoreThreshold is unsure, otherwise ham. Both constants
// are part of the documented behavior; they are not configurable per tenant
// in v1 so verdicts stay comparable across the fleet.
const (
	SpamScoreThreshold   = 5.0
	UnsureScoreThreshold = 2.0

	// ScoreClamp bounds the contribution of the content heuristics (never
	// the list rules, which short-circuit) to keep one weird message from
	// producing absurd numbers in the X-UrMail-Spam-Score header.
	ScoreClamp = 12.0
)

// Weighted signals. Every constant is documented so an operator can reason
// about a verdict from its reason codes alone.
const (
	// Sender list rules short-circuit: the first matching rule decides.
	WeightSenderList = 100.0

	// Authentication-Results signals (trusted authserv-ids only; see
	// ParseAuthResults). Failures add, passes subtract modestly: absence of
	// authentication is common for legitimate mail, so it is never scored.
	WeightDMARCReject     = 3.0
	WeightDMARCQuarantine = 2.0
	WeightDKIMFail        = 2.0
	WeightDKIMPass        = -1.0
	WeightSPFFail         = 2.5
	WeightSPFSoftFail     = 1.5
	WeightSPFPass         = -1.0

	// A submission authenticated by one of our own users that claims a From
	// domain hosted here is very likely legitimate outbound mail.
	WeightSASLLocalDomain = -2.0

	// Structural heuristics.
	WeightMissingMessageID = 1.0
	WeightMissingDate      = 1.0
	WeightSubjectAllCaps   = 1.0
	WeightManyURLs         = 1.0
	WeightEmptyBody        = 0.5

	// manyURLsThreshold is the number of distinct http(s) URLs in the
	// sampled body beyond which WeightManyURLs applies once.
	manyURLsThreshold = 5

	// subjectAllCapsMinLength avoids flagging "RE:", "FW:", "ASAP".
	subjectAllCapsMinLength = 10
)

// Header is one RFC 5322 header captured from the message sample.
type Header struct {
	Name  string
	Value string
}

// AuthResult is one parsed authentication statement.
type AuthResult struct {
	Method string // "spf" | "dkim" | "dmarc"
	Result string // e.g. "pass", "fail", "softfail", "reject", "none"
	Domain string // the domain the statement is about (may be empty)
}

// AuthContext carries the connection and message authentication facts.
type AuthContext struct {
	// SASLUser is the authenticated SMTP login, empty when the session has
	// none.
	SASLUser string
	// Results are trusted authentication statements (see ParseAuthResults).
	Results []AuthResult
	// LocalDomains lists the mail domains this platform hosts; used only for
	// the outbound SASL signal.
	LocalDomains []string
}

// TokenCount is the learned observation count for one token.
type TokenCount struct {
	Spam int
	Ham  int
}

// SenderRule is one allow/deny entry from the policy snapshot.
type SenderRule struct {
	List    string // "allow" | "deny"
	Scope   string // "mailbox" | "tenant"
	Address string // exact sender address (lowercase); "" for domain rules
	Domain  string // sender domain (lowercase); "" for address rules
}

// ListSnapshot is the per-recipient policy input: sender rules plus learned
// token statistics for the recipient mailbox.
type ListSnapshot struct {
	Rules  []SenderRule
	Tokens map[string]TokenCount
}

// Message is the classified message sample.
type Message struct {
	Headers    []Header
	BodySample []byte
	// EnvelopeFrom is the reverse-path address (lowercase, may be empty for
	// bounce messages).
	EnvelopeFrom string
}

// Verdict is the deterministic classification result.
type Verdict struct {
	Class   Class
	Score   float64
	Reasons []string
}

// FromDomain extracts the lowercased domain of the first From header, falling
// back to the envelope sender when the header is absent or unparseable.
func (m Message) FromDomain() string {
	address := m.EnvelopeFrom
	for _, h := range m.Headers {
		if strings.EqualFold(h.Name, "From") {
			if parsed, err := mail.ParseAddress(h.Value); err == nil && parsed.Address != "" {
				address = parsed.Address
			}
			break
		}
	}
	return domainOf(address)
}

func domainOf(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	if at := strings.LastIndex(address, "@"); at >= 0 && at+1 < len(address) {
		return address[at+1:]
	}
	return ""
}

// rule evaluation order: mailbox scope first (an explicit per-mailbox user
// decision shadows the operator's tenant-wide entry), then tenant scope; at
// equal scope deny wins over allow, and an exact-address rule precedes a
// domain rule. The first match short-circuits and is the only list signal
// applied.
var rulePriority = []struct {
	list    string // "deny" then "allow"
	scope   string // "mailbox" then "tenant"
	address bool   // exact address first
}{
	{"deny", "mailbox", true},
	{"deny", "mailbox", false},
	{"allow", "mailbox", true},
	{"allow", "mailbox", false},
	{"deny", "tenant", true},
	{"deny", "tenant", false},
	{"allow", "tenant", true},
	{"allow", "tenant", false},
}

const (
	reasonSenderDeniedMailbox  = "sender_denied_mailbox"
	reasonSenderDeniedTenant   = "sender_denied_tenant"
	reasonSenderAllowedMailbox = "sender_allowed_mailbox"
	reasonSenderAllowedTenant  = "sender_allowed_tenant"
	reasonDMARCReject          = "dmarc_reject"
	reasonDMARCQuarantine      = "dmarc_quarantine"
	reasonDKIMFail             = "dkim_fail"
	reasonDKIMPass             = "dkim_pass"
	reasonSPFFail              = "spf_fail"
	reasonSPFSoftFail          = "spf_softfail"
	reasonSPFPass              = "spf_pass"
	reasonSASLLocalDomain      = "sasl_local_domain"
	reasonMissingMessageID     = "missing_message_id"
	reasonMissingDate          = "missing_date"
	reasonSubjectAllCaps       = "subject_all_caps"
	reasonManyURLs             = "many_urls"
	reasonEmptyBody            = "empty_body"
	reasonTokenSpam            = "token_spam_signals"
	reasonTokenHam             = "token_ham_signals"
	reasonNone                 = "no_signal"
)

// Classify computes the deterministic verdict for one message.
func Classify(m Message, auth AuthContext, lists ListSnapshot) Verdict {
	score := 0.0
	var reasons []string
	spamHit := false
	allowHit := false

	fromDomain := m.FromDomain()

ruleLoop:
	for _, p := range rulePriority {
		for _, rule := range lists.Rules {
			if rule.List != p.list || rule.Scope != p.scope {
				continue
			}
			matched := false
			if p.address {
				matched = rule.Address != "" && strings.EqualFold(rule.Address, m.EnvelopeFrom)
			} else {
				matched = rule.Domain != "" && rule.Domain == fromDomain
			}
			if !matched {
				continue
			}
			if p.list == "deny" {
				score += WeightSenderList
			} else {
				score -= WeightSenderList
			}
			switch {
			case p.list == "deny" && p.scope == "mailbox":
				reasons = append(reasons, reasonSenderDeniedMailbox)
				spamHit = true
			case p.list == "deny":
				reasons = append(reasons, reasonSenderDeniedTenant)
				spamHit = true
			case p.scope == "mailbox":
				reasons = append(reasons, reasonSenderAllowedMailbox)
				allowHit = true
			default:
				reasons = append(reasons, reasonSenderAllowedTenant)
				allowHit = true
			}
			break ruleLoop
		}
	}

	if !spamHit && !allowHit {
		// Authentication and content heuristics only apply when no explicit
		// user decision covers the sender: the mailbox owner's word is final.
		score += authScore(m, auth, &reasons)
		score += contentScore(m, &reasons)
		score += tokenScore(lists.Tokens, m, &reasons)
	}

	// Clamp the heuristic (non-list) portion so the reported score stays in a
	// bounded, documented range.
	if !spamHit && !allowHit {
		score = clamp(score, -ScoreClamp, ScoreClamp)
	}

	class := ClassHam
	switch {
	case score >= SpamScoreThreshold:
		class = ClassSpam
	case score >= UnsureScoreThreshold:
		class = ClassUnsure
	}
	if len(reasons) == 0 {
		reasons = append(reasons, reasonNone)
	}
	return Verdict{Class: class, Score: score, Reasons: reasons}
}

func clamp(v, low, high float64) float64 {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

// authScore evaluates trusted Authentication-Results statements. Unknown
// results are ignored: absence of authentication is not evidence of spam.
func authScore(m Message, auth AuthContext, reasons *[]string) float64 {
	score := 0.0
	spfDone, dkimDone, dmarcDone := false, false, false
	for _, r := range auth.Results {
		switch r.Method {
		case "spf":
			if spfDone {
				continue
			}
			switch r.Result {
			case "fail":
				score += WeightSPFFail
				*reasons = append(*reasons, reasonSPFFail)
			case "softfail":
				score += WeightSPFSoftFail
				*reasons = append(*reasons, reasonSPFSoftFail)
			case "pass":
				score += WeightSPFPass
				*reasons = append(*reasons, reasonSPFPass)
			}
			spfDone = true
		case "dkim":
			if dkimDone {
				continue
			}
			switch r.Result {
			case "fail":
				score += WeightDKIMFail
				*reasons = append(*reasons, reasonDKIMFail)
			case "pass":
				score += WeightDKIMPass
				*reasons = append(*reasons, reasonDKIMPass)
			}
			dkimDone = true
		case "dmarc":
			if dmarcDone {
				continue
			}
			switch r.Result {
			case "reject":
				score += WeightDMARCReject
				*reasons = append(*reasons, reasonDMARCReject)
			case "quarantine":
				score += WeightDMARCQuarantine
				*reasons = append(*reasons, reasonDMARCQuarantine)
			}
			dmarcDone = true
		}
	}
	if auth.SASLUser != "" && domainOf(auth.SASLUser) != "" {
		if domainOf(auth.SASLUser) == m.FromDomain() && containsFold(auth.LocalDomains, m.FromDomain()) {
			score += WeightSASLLocalDomain
			*reasons = append(*reasons, reasonSASLLocalDomain)
		}
	}
	return score
}

// ParseAuthResults extracts authentication statements from
// Authentication-Results headers. Only headers whose authserv-id (the token
// before the first ';') matches one of the trusted ids are considered: a
// sender-controlled header must never be able to award itself a DKIM pass.
// Parsing is bounded and deterministic; malformed headers contribute nothing.
func ParseAuthResults(headers []Header, trustedAuthServIDs []string) []AuthResult {
	if len(trustedAuthServIDs) == 0 {
		return nil
	}
	trusted := make(map[string]struct{}, len(trustedAuthServIDs))
	for _, id := range trustedAuthServIDs {
		trusted[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	var results []AuthResult
	for _, h := range headers {
		if !strings.EqualFold(h.Name, "Authentication-Results") {
			continue
		}
		authserv, statements, ok := strings.Cut(h.Value, ";")
		if !ok {
			continue
		}
		if _, ok := trusted[strings.ToLower(strings.TrimSpace(authserv))]; !ok {
			continue
		}
		for _, statement := range strings.Split(statements, ";") {
			fields := strings.Fields(statement)
			if len(fields) == 0 {
				continue
			}
			method, result, ok := strings.Cut(fields[0], "=")
			if !ok {
				continue
			}
			method = strings.ToLower(strings.TrimSpace(method))
			result = strings.ToLower(strings.TrimSpace(result))
			switch method {
			case "spf", "dkim", "dmarc":
			default:
				continue
			}
			domain := ""
			if len(fields) > 1 && !strings.Contains(fields[1], "=") {
				domain = strings.Trim(fields[1], "()")
			}
			results = append(results, AuthResult{Method: method, Result: result, Domain: domain})
		}
	}
	return results
}

func containsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}

// contentScore applies bounded structural heuristics to the header/body
// sample. It never looks at the network and never decodes attachments.
func contentScore(m Message, reasons *[]string) float64 {
	score := 0.0
	var messageID, date, subject string
	for _, h := range m.Headers {
		switch {
		case messageID == "" && strings.EqualFold(h.Name, "Message-ID"):
			messageID = h.Value
		case date == "" && strings.EqualFold(h.Name, "Date"):
			date = h.Value
		case subject == "" && strings.EqualFold(h.Name, "Subject"):
			subject = h.Value
		}
	}
	if strings.TrimSpace(messageID) == "" {
		score += WeightMissingMessageID
		*reasons = append(*reasons, reasonMissingMessageID)
	}
	if strings.TrimSpace(date) == "" {
		score += WeightMissingDate
		*reasons = append(*reasons, reasonMissingDate)
	}
	if len(subject) >= subjectAllCapsMinLength && subject == strings.ToUpper(subject) && hasLetters(subject) {
		score += WeightSubjectAllCaps
		*reasons = append(*reasons, reasonSubjectAllCaps)
	}
	if countURLs(m.BodySample) > manyURLsThreshold {
		score += WeightManyURLs
		*reasons = append(*reasons, reasonManyURLs)
	}
	if len(strings.TrimSpace(string(m.BodySample))) == 0 {
		score += WeightEmptyBody
		*reasons = append(*reasons, reasonEmptyBody)
	}
	return score
}

func hasLetters(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

func countURLs(body []byte) int {
	count := 0
	for _, field := range strings.Fields(string(body)) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			count++
			if count > manyURLsThreshold {
				return count
			}
		}
	}
	return count
}

// tokenScore folds the learned token statistics in as a bounded log-odds sum.
func tokenScore(tokens map[string]TokenCount, m Message, reasons *[]string) float64 {
	if len(tokens) == 0 {
		return 0
	}
	sum := 0.0
	for token := range tokenCounts(ExtractTokens(TokenInputFromMessage(m))) {
		observed, ok := tokens[token]
		if !ok || observed.Spam+observed.Ham < tokenMinObservations {
			continue
		}
		sum += logOdds(observed)
	}
	if sum == 0 {
		return 0
	}
	if sum > 0 {
		*reasons = append(*reasons, reasonTokenSpam)
	} else {
		*reasons = append(*reasons, reasonTokenHam)
	}
	return clamp(sum, -TokenWeightCap, TokenWeightCap)
}

// NormalizeRules returns a deterministic copy of a sender-rule list sorted
// by scope, list, address and domain, for callers that build snapshots from
// unordered sources and want stable reason ordering.
func NormalizeRules(rules []SenderRule) []SenderRule {
	out := make([]SenderRule, len(rules))
	copy(out, rules)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.List != b.List {
			return a.List < b.List
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.Domain < b.Domain
	})
	return out
}
