package maildnsreport

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxTLSReportBytes bounds the expanded JSON document.
const MaxTLSReportBytes = 4 << 20

// MaxTLSReportWireBytes bounds compressed or plain incoming bytes.
const MaxTLSReportWireBytes = 1 << 20

// MaxTLSReportCount bounds aggregate counts without integer overflow.
const MaxTLSReportCount int64 = 1_000_000_000

// ErrTLSReport reports malformed, mismatched or oversized input.
var ErrTLSReport = errors.New("invalid or oversized TLS report")
var tlsResultType = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// TLSReportSummary deliberately omits source IPs, organization/contact strings,
// and untrusted free-form details. HTTPS transport does not authenticate a
// reporting organization; callers must never use these counts as authority.
type TLSReportSummary struct {
	Digest             string
	ReporterDigest     string
	IDDigest           string
	Start, End         time.Time
	Successful, Failed int64
	Failures           map[string]int64
}

func reportHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// ParseTLSReport accepts RFC 8460 JSON or gzip JSON with independent wire and
// expanded limits. The requested domain is server-resolved, never a tenant ID
// supplied by the reporter. Unknown extension properties are ignored.
func ParseTLSReport(wire []byte, compressed bool, domain string, now time.Time) (TLSReportSummary, error) {
	var out TLSReportSummary
	if len(wire) == 0 || len(wire) > MaxTLSReportWireBytes {
		return out, ErrTLSReport
	}
	raw := wire
	if compressed {
		reader, err := gzip.NewReader(bytes.NewReader(wire))
		if err != nil {
			return out, ErrTLSReport
		}
		raw, err = io.ReadAll(io.LimitReader(reader, MaxTLSReportBytes+1))
		closeErr := reader.Close()
		if err != nil || closeErr != nil || len(raw) > MaxTLSReportBytes {
			return out, ErrTLSReport
		}
	}
	if len(raw) > MaxTLSReportBytes || !utf8.Valid(raw) || !boundedReportJSON(raw) {
		return out, ErrTLSReport
	}
	var in struct {
		Organization string `json:"organization-name"`
		Contact      string `json:"contact-info"`
		ID           string `json:"report-id"`
		Range        struct {
			Start time.Time `json:"start-datetime"`
			End   time.Time `json:"end-datetime"`
		} `json:"date-range"`
		Policies []struct {
			Policy struct {
				Type   string `json:"policy-type"`
				Domain string `json:"policy-domain"`
			} `json:"policy"`
			Summary struct {
				Success *int64 `json:"total-successful-session-count"`
				Failure *int64 `json:"total-failure-session-count"`
			} `json:"summary"`
			Failures []struct {
				Type  string `json:"result-type"`
				Count int64  `json:"failed-session-count"`
			} `json:"failure-details"`
		} `json:"policies"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&in) != nil {
		return out, ErrTLSReport
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return out, ErrTLSReport
	}
	if len(in.Organization) < 1 || len(in.Organization) > 256 || len(strings.TrimSpace(in.Contact)) == 0 || len(in.Contact) > 512 || len(in.ID) < 1 || len(in.ID) > 256 || len(in.Policies) < 1 || len(in.Policies) > 100 {
		return out, ErrTLSReport
	}
	if in.Range.Start.IsZero() || in.Range.End.Before(in.Range.Start) || in.Range.End.Sub(in.Range.Start) > 8*24*time.Hour || in.Range.End.After(now.Add(5*time.Minute)) || in.Range.Start.Before(now.Add(-90*24*time.Hour)) {
		return out, ErrTLSReport
	}
	out = TLSReportSummary{Digest: reportHash(raw), ReporterDigest: reportHash([]byte(strings.ToLower(strings.TrimSpace(in.Organization)))), IDDigest: reportHash([]byte(in.ID)), Start: in.Range.Start.UTC(), End: in.Range.End.UTC(), Failures: map[string]int64{}}
	for _, policy := range in.Policies {
		if strings.TrimSuffix(strings.ToLower(policy.Policy.Domain), ".") != domain || len(policy.Failures) > 200 {
			return TLSReportSummary{}, ErrTLSReport
		}
		switch policy.Policy.Type {
		case "sts", "tlsa", "no-policy-found":
		default:
			return TLSReportSummary{}, ErrTLSReport
		}
		if policy.Summary.Success == nil || policy.Summary.Failure == nil {
			return TLSReportSummary{}, ErrTLSReport
		}
		success, failed := *policy.Summary.Success, *policy.Summary.Failure
		if success < 0 || failed < 0 || success > MaxTLSReportCount-out.Successful || failed > MaxTLSReportCount-out.Failed {
			return TLSReportSummary{}, ErrTLSReport
		}
		out.Successful += success
		out.Failed += failed
		var details int64
		for _, failure := range policy.Failures {
			if !tlsResultType.MatchString(failure.Type) || failure.Count < 0 || failure.Count > failed-details {
				return TLSReportSummary{}, ErrTLSReport
			}
			details += failure.Count
			out.Failures[failure.Type] += failure.Count
		}
	}
	return out, nil
}

// Reject pathological nesting, token floods and duplicate object fields before
// decoding attacker-controlled arrays into typed allocations.
func boundedReportJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	tokens := 0
	var value func(int) bool
	value = func(depth int) bool {
		tokens++
		if depth > 32 || tokens > 100000 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return true
		}
		switch delimiter {
		case '{':
			keys := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return false
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return false
				}
				keys[name] = true
				tokens++
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}
