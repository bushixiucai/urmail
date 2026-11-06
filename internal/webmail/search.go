package webmail

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidSearchQuery is returned when q cannot be mapped to IMAP SEARCH
// without risking command injection or exceeding safety limits.
var ErrInvalidSearchQuery = errors.New("invalid mailbox search query")

const (
	// MaxSearchQueryLen caps the raw q query parameter.
	MaxSearchQueryLen = 200
	// MaxSearchTokens caps how many parsed tokens we accept.
	MaxSearchTokens = 16
)

// BuildSearchCriteria maps a user-facing q string to an IMAP UID SEARCH
// criteria fragment (everything after "UID SEARCH ").
//
// Semantics for GET /mailbox/folders/:name/messages?q=:
//
//   - empty / whitespace → "ALL" (same as omitting q)
//   - is:unseen | unseen → UNSEEN
//   - is:flagged | flagged → FLAGGED
//   - since:YYYY-MM-DD / before:YYYY-MM-DD → internal-date inclusive/exclusive
//   - has:attachment → client BODYSTRUCTURE filtering before pagination
//   - to:<text> / body:<text> → TO/BODY quoted search
//   - from:<text>        → FROM "<quoted>"
//   - subject:<text>     → SUBJECT "<quoted>"
//   - text:<text>        → TEXT "<quoted>" (header+body substring)
//   - bare text / "phr"  → OR SUBJECT "<q>" FROM "<q>" (AND with other criteria)
//
// Multiple criteria are ANDed. String literals are always quoteIMAP-escaped;
// control characters and unknown key: prefixes are rejected. Callers must not
// append raw user input to the IMAP command — only the returned criteria.
// ListMessages also applies the metadata filter returned by buildSearchPlan.
func BuildSearchCriteria(q string) (string, error) {
	criteria, _, err := buildSearchPlan(q)
	return criteria, err
}

func buildSearchPlan(q string) (string, bool, error) {
	hasAttachment := false
	q = strings.TrimSpace(q)
	if q == "" {
		return "ALL", hasAttachment, nil
	}
	if utf8.RuneCountInString(q) > MaxSearchQueryLen {
		return "", false, fmt.Errorf("%w: too long", ErrInvalidSearchQuery)
	}
	if strings.ContainsFunc(q, isUnsafeSearchRune) {
		return "", false, fmt.Errorf("%w: forbidden characters", ErrInvalidSearchQuery)
	}

	tokens, err := tokenizeSearchQuery(q)
	if err != nil {
		return "", false, err
	}
	if len(tokens) == 0 {
		return "ALL", hasAttachment, nil
	}
	if len(tokens) > MaxSearchTokens {
		return "", false, fmt.Errorf("%w: too many terms", ErrInvalidSearchQuery)
	}

	var parts []string
	var bare []string
	for _, tok := range tokens {
		lower := strings.ToLower(tok)
		switch {
		case lower == "unseen" || lower == "is:unseen":
			parts = append(parts, "UNSEEN")
		case lower == "flagged" || lower == "is:flagged":
			parts = append(parts, "FLAGGED")
		case lower == "has:attachment":
			hasAttachment = true
		case hasSearchField(tok, "since") || hasSearchField(tok, "before"):
			key, value, _ := strings.Cut(tok, ":")
			date, dateErr := time.Parse("2006-01-02", value)
			if dateErr != nil || date.Year() < 1 {
				return "", false, fmt.Errorf("%w: invalid date", ErrInvalidSearchQuery)
			}
			parts = append(parts, strings.ToUpper(key)+" "+date.Format("02-Jan-2006"))
		case hasSearchField(tok, "to") || hasSearchField(tok, "body"):
			key, value, _ := strings.Cut(tok, ":")
			if value == "" {
				return "", false, fmt.Errorf("%w: empty field", ErrInvalidSearchQuery)
			}
			parts = append(parts, strings.ToUpper(key)+" "+quoteIMAP(value))
		case hasSearchField(tok, "from"):
			val := searchFieldValue(tok, "from")
			if val == "" {
				return "", false, fmt.Errorf("%w: empty from", ErrInvalidSearchQuery)
			}
			parts = append(parts, "FROM "+quoteIMAP(val))
		case hasSearchField(tok, "subject"):
			val := searchFieldValue(tok, "subject")
			if val == "" {
				return "", false, fmt.Errorf("%w: empty subject", ErrInvalidSearchQuery)
			}
			parts = append(parts, "SUBJECT "+quoteIMAP(val))
		case hasSearchField(tok, "text"):
			val := searchFieldValue(tok, "text")
			if val == "" {
				return "", false, fmt.Errorf("%w: empty text", ErrInvalidSearchQuery)
			}
			parts = append(parts, "TEXT "+quoteIMAP(val))
		case strings.Contains(tok, ":"):
			// Reject unknown key: prefixes so users cannot smuggle IMAP ops.
			return "", false, fmt.Errorf("%w: unsupported field", ErrInvalidSearchQuery)
		default:
			bare = append(bare, tok)
		}
	}

	if len(bare) > 0 {
		phrase := strings.Join(bare, " ")
		// OR SUBJECT/FROM without nesting parens that older servers mishandle:
		// "OR SUBJECT x FROM x" is valid IMAP (OR takes two keys).
		parts = append(parts, "OR SUBJECT "+quoteIMAP(phrase)+" FROM "+quoteIMAP(phrase))
	}

	if len(parts) == 0 {
		return "ALL", hasAttachment, nil
	}
	return strings.Join(parts, " "), hasAttachment, nil
}

func hasSearchField(tok, key string) bool {
	if len(tok) < len(key)+1 {
		return false
	}
	return strings.EqualFold(tok[:len(key)], key) && tok[len(key)] == ':'
}

func searchFieldValue(tok, key string) string {
	if !hasSearchField(tok, key) {
		return ""
	}
	return strings.TrimSpace(tok[len(key)+1:])
}

func isUnsafeSearchRune(r rune) bool {
	if r == '\n' || r == '\r' || r == '\x00' {
		return true
	}
	// Reject other C0 controls and DEL; allow tab/space via unicode.IsSpace filter below.
	if r < 0x20 && r != '\t' {
		return true
	}
	if r == 0x7f {
		return true
	}
	return false
}

// tokenizeSearchQuery splits on whitespace, respecting "double quotes".
// Quotes are stripped from the token; backslash escapes \" and \\ inside quotes.
func tokenizeSearchQuery(q string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	escaped := false
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tokens = append(tokens, cur.String())
		cur.Reset()
	}
	for _, r := range q {
		if escaped {
			if r != '"' && r != '\\' {
				return nil, fmt.Errorf("%w: bad escape", ErrInvalidSearchQuery)
			}
			cur.WriteRune(r)
			escaped = false
			continue
		}
		if inQuote {
			switch r {
			case '\\':
				escaped = true
			case '"':
				inQuote = false
			default:
				cur.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '"':
			inQuote = true
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if escaped || inQuote {
		return nil, fmt.Errorf("%w: unclosed quote", ErrInvalidSearchQuery)
	}
	flush()
	return tokens, nil
}
