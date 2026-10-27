package mailspam

import (
	"math"
	"strconv"
	"strings"
)

// Tokenizer bounds and behavior. Extraction is deterministic: the same input
// always produces the same token multiset, so learned statistics and verdicts
// are reproducible.
const (
	// TokenMinLen / TokenMaxLen bound one token; shorter tokens carry no
	// topical signal, longer ones are usually binary noise.
	TokenMinLen = 3
	TokenMaxLen = 20
	// TokenMaxPerMessage bounds work per message; the sample is already
	// bounded upstream by the milter body cap.
	TokenMaxPerMessage = 2000
	// tokenMinObservations: a token only influences verdicts after it has
	// been learned this many times, which keeps one reported message from
	// dominating the model.
	tokenMinObservations = 3
	// TokenWeightCap bounds the total log-odds contribution (and per-token
	// clamping) of token evidence.
	TokenWeightCap = 4.0
	// TokenMaxLearnedPerFeedback bounds how many distinct tokens one
	// feedback event may write to the training table.
	TokenMaxLearnedPerFeedback = 64
)

// TokenInput is the bounded text the tokenizer sees. v1 learning uses the
// subject and sender identity supplied with user feedback plus the milter's
// body sample; attachment and full-MIME decoding are explicitly out of scope.
type TokenInput struct {
	Subject    string
	FromDomain string
	Body       []byte
}

// TokenInputFromMessage projects a message sample onto tokenizer input.
func TokenInputFromMessage(m Message) TokenInput {
	var subject string
	for _, h := range m.Headers {
		if strings.EqualFold(h.Name, "Subject") {
			subject = h.Value
			break
		}
	}
	return TokenInput{Subject: subject, FromDomain: FromAddress(m), Body: m.BodySample}
}

// FromAddress returns the lowercased envelope sender, falling back to the
// first parseable From header address.
func FromAddress(m Message) string {
	address := strings.ToLower(strings.TrimSpace(m.EnvelopeFrom))
	if address != "" {
		return address
	}
	for _, h := range m.Headers {
		if strings.EqualFold(h.Name, "From") {
			return strings.ToLower(h.Value)
		}
	}
	return ""
}

// ExtractTokens returns the deterministic token multiset for the input.
func ExtractTokens(in TokenInput) []string {
	tokens := make([]string, 0, 64)
	appendTokens := func(text string) {
		for _, field := range strings.Fields(stripHTML(text)) {
			field = decodeEntities(field)
			if token := normalizeToken(field); token != "" {
				tokens = append(tokens, token)
				if len(tokens) >= TokenMaxPerMessage {
					return
				}
			}
		}
	}
	appendTokens(in.Subject)
	if in.FromDomain != "" {
		// The sender domain is a strong, repeatedly useful learning signal.
		for _, part := range strings.Split(in.FromDomain, ".") {
			if token := normalizeToken(part); token != "" {
				tokens = append(tokens, "dom:"+token)
				if len(tokens) >= TokenMaxPerMessage {
					break
				}
			}
		}
	}
	if len(tokens) < TokenMaxPerMessage {
		appendTokens(string(in.Body))
	}
	return tokens
}

// stripHTML removes <...> spans so markup tags do not pollute tokens. It is a
// bounded textual transform, not an HTML parser.
func stripHTML(text string) string {
	if !strings.ContainsAny(text, "<>") {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	depth := 0
	for _, r := range text {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// decodeEntities resolves the handful of entities that survive tokenization;
// numeric entities are dropped rather than mis-tokenized.
func decodeEntities(field string) string {
	if !strings.Contains(field, "&") {
		return field
	}
	replacer := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&apos;", "'", "&nbsp;", " ")
	field = replacer.Replace(field)
	if strings.Contains(field, "&#") {
		field = dropNumericEntities(field)
	}
	return field
}

func dropNumericEntities(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '&' {
			if end := strings.IndexByte(s[i:], ';'); end > 2 && end <= 8 {
				entity := s[i+1 : i+end]
				if entity[0] == '#' {
					if code, err := strconv.Atoi(entity[1:]); err == nil && code >= 32 && code < 0x300 {
						b.WriteByte(' ') // keep a word boundary; do not inject runes into tokens
						i += end + 1
						continue
					}
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// normalizeToken lowercases, trims surrounding punctuation and applies the
// documented length bounds. URLs collapse to their host so link farms and
// ordinary prose share one vocabulary.
func normalizeToken(field string) string {
	field = strings.ToLower(field)
	if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
		field = strings.TrimPrefix(strings.TrimPrefix(field, "https://"), "http://")
		if at := strings.IndexAny(field, "/?#"); at >= 0 {
			field = field[:at]
		}
		if at := strings.Index(field, "@"); at >= 0 && at+1 < len(field) {
			field = field[at+1:]
		}
		field = "url:" + field
	}
	field = strings.Trim(field, "\"'`([{<.,!?;:*)]}>")
	if len(field) < TokenMinLen || len(field) > TokenMaxLen {
		return ""
	}
	for _, r := range field {
		if r > 0x7f {
			return "" // keep the v1 vocabulary ASCII-deterministic
		}
	}
	return field
}

// TokenDelta is the learned adjustment for one token. The fields are int32
// to match the storage columns and keep the write path conversion-free.
type TokenDelta struct {
	Spam int32
	Ham  int32
}

// LearnFromFeedback derives training deltas from one user verdict. V1 input
// is the subject and sender identity the client already displays; the counts
// move by exactly one per feedback so repeated reports are stable.
func LearnFromFeedback(subject, fromAddress, verdict string) map[string]TokenDelta {
	in := TokenInput{Subject: subject, FromDomain: domainOf(fromAddress)}
	counts := make(map[string]int)
	for _, token := range ExtractTokens(in) {
		counts[token]++
	}
	deltas := make(map[string]TokenDelta, len(counts))
	var spamWeight, hamWeight int32
	switch verdict {
	case "spam":
		spamWeight = 1
	case "ham":
		hamWeight = 1
	default:
		return deltas
	}
	for token := range counts {
		if len(deltas) >= TokenMaxLearnedPerFeedback {
			break
		}
		deltas[token] = TokenDelta{Spam: spamWeight, Ham: hamWeight}
	}
	return deltas
}

// TokenEvidence renders the token multiset with counts for scoring.
func tokenCounts(tokens []string) map[string]int {
	counts := make(map[string]int, len(tokens))
	for _, token := range tokens {
		counts[token]++
	}
	return counts
}

// logOdds returns the Laplace-smoothed log-odds of spam for one token.
func logOdds(count TokenCount) float64 {
	p := float64(count.Spam+1) / float64(count.Spam+count.Ham+2)
	return math.Log(p / (1 - p))
}
