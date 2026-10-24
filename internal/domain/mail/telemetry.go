package mail

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Postfix delivery event statuses.
//
// These seven values are exactly the statuses accepted by the
// delivery_events_status_check constraint in
// migrations/0027_onboarding_and_telemetry.up.sql:
//
//	CHECK (status IN ('queued', 'sent', 'deferred', 'bounced', 'expired', 'removed', 'unknown'))
//
// The parser never emits anything outside this set. A Postfix status word that
// is not part of the accepted set (for example "warning" or "undeliverable")
// is normalised to DeliveryStatusUnknown, because emitting it would make the
// CHECK reject the insert and silently lose the delivery event.
const (
	DeliveryStatusQueued   = "queued"
	DeliveryStatusSent     = "sent"
	DeliveryStatusDeferred = "deferred"
	DeliveryStatusBounced  = "bounced"
	DeliveryStatusExpired  = "expired"
	DeliveryStatusRemoved  = "removed"
	DeliveryStatusUnknown  = "unknown"
)

// Delivery domain errors.
var (
	ErrNotPostfixLine = errors.New("line is not a recognizable postfix delivery event")
)

// AcceptedDeliveryStatuses returns the delivery statuses accepted by the
// delivery_events table (the delivery_events_status_check constraint of
// migrations/0027_onboarding_and_telemetry.up.sql). The returned slice is a
// copy, so callers may sort or modify it freely.
func AcceptedDeliveryStatuses() []string {
	return []string{
		DeliveryStatusBounced,
		DeliveryStatusDeferred,
		DeliveryStatusExpired,
		DeliveryStatusQueued,
		DeliveryStatusRemoved,
		DeliveryStatusSent,
		DeliveryStatusUnknown,
	}
}

// IsAcceptedDeliveryStatus reports whether status may be stored in the
// delivery_events table.
func IsAcceptedDeliveryStatus(status string) bool {
	for _, s := range AcceptedDeliveryStatuses() {
		if s == status {
			return true
		}
	}
	return false
}

// ParsedDeliveryEvent represents a normalized mail delivery milestone extracted from Postfix syslog.
//
// Correlation fields (QueueID, MessageID, Recipient, Sender) are filled from
// whichever part of the log line carries them, so a line that mentions several
// of them yields an event that carries all of them. QueueID is the stable
// correlation key: Postfix repeats the same queue ID on the qmgr, cleanup and
// smtp lines of one message, so a consumer can join a message id with its
// recipients through it.
type ParsedDeliveryEvent struct {
	SMTPClientRecord     bool      `json:"smtp_client_record,omitempty"`
	AuthenticatedMailbox string    `json:"authenticated_mailbox,omitempty"`
	QueueID              string    `json:"queue_id"`
	Timestamp            time.Time `json:"timestamp"`
	Process              string    `json:"process,omitempty"` // syslog tag: postfix, postfix-out, postfix-in, ...
	Component            string    `json:"component"`         // qmgr, smtp, cleanup, bounce, ...
	Sender               string    `json:"sender,omitempty"`
	SenderDomain         string    `json:"sender_domain,omitempty"`
	Recipient            string    `json:"recipient,omitempty"`
	RecipientDomain      string    `json:"recipient_domain,omitempty"`
	Relay                string    `json:"relay,omitempty"`
	Status               string    `json:"status"` // queued, sent, deferred, bounced, expired, removed, unknown
	DSN                  string    `json:"dsn,omitempty"`
	DelaySeconds         float64   `json:"delay_seconds,omitempty"`
	ResponseDetail       string    `json:"response_detail,omitempty"`
	MessageID            string    `json:"message_id,omitempty"`
}

// ParserOptions configures the Postfix log parser.
//
// The parser stays a pure function: options carry no I/O, no clock reads other
// than the optional reference clock and no database handles.
type ParserOptions struct {
	// Location is the time zone used to interpret log timestamps that carry
	// no explicit UTC offset. Postfix writes RFC3164 syslog timestamps in
	// the local time of the host it runs on and the classic syslog format
	// carries no offset at all, so such a timestamp is interpreted in this
	// zone. A nil Location means time.Local, which is the zone of the
	// machine the parser runs on - for the intended deployment (parser and
	// Postfix on the same host) that is exactly the zone Postfix logged in.
	Location *time.Location

	// Now is the reference clock used to infer the year of RFC3164
	// timestamps, which carry no year. It is also the reference for the
	// year-rollover heuristic. A zero Now means time.Now().
	//
	// Supplying Now makes the parse fully deterministic, which is what
	// tests and replay tooling should do.
	Now time.Time
}

// location returns the effective time zone of the options.
func (o ParserOptions) location() *time.Location {
	if o.Location == nil {
		return time.Local
	}
	return o.Location
}

// reference returns the effective reference clock of the options.
func (o ParserOptions) reference() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

var (
	// Postfix syslog tag. A plain deployment logs "postfix/smtp[1234]:";
	// a deployment that enables multi_instance_wrapper(5) logs
	// "postfix-out/smtp[1234]:" or "postfix-in/smtp[1234]:", where the
	// instance name sits in front of the service name. The queue ID
	// charset follows Postfix: uppercase-style short IDs, base-52 long IDs
	// (upper+lower+digit) and legacy forms containing '!'.
	postfixPrefixRe = regexp.MustCompile(`\b(postfix(?:[-_][A-Za-z0-9]+)*)/([A-Za-z0-9_-]+)(?:\[\d+\])?: ([A-Za-z0-9!]+): (.*)`)

	// Flat multi-instance tag, e.g. "postfix-smtp[1234]: 4S123: ...".
	// The service name is recovered from the tag when it is a known Postfix
	// daemon name.
	postfixFlatPrefixRe = regexp.MustCompile(`\b(postfix-[A-Za-z0-9_]+)(?:\[\d+\])?: ([A-Za-z0-9!]+): (.*)`)

	// Syslog timestamp prefix, e.g. "Oct 23 14:00:01 " (day may be space-padded).
	syslogTimeRe = regexp.MustCompile(`^([A-Z][a-z]{2})\s+(\d{1,2})\s+(\d{2}):(\d{2}):(\d{2})(?:\s|$)`)

	// RFC5424 / journald ISO8601 timestamp, e.g. "2026-10-23T14:00:12.123+08:00".
	// The UTC offset is optional; "Z" and "+08:00" style offsets are both
	// accepted, with or without the colon.
	iso8601TimeRe = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})[Tt ](\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(Z|z|[+-]\d{2}:?\d{2})?`)

	// Month lookup for syslog timestamps.
	syslogMonths = map[string]time.Month{
		"Jan": time.January, "Feb": time.February, "Mar": time.March,
		"Apr": time.April, "May": time.May, "Jun": time.June,
		"Jul": time.July, "Aug": time.August, "Sep": time.September,
		"Oct": time.October, "Nov": time.November, "Dec": time.December,
	}

	// knownPostfixServices lists the Postfix daemons that may appear in a
	// syslog tag. It is used to recover the service name from a flat
	// multi-instance tag such as "postfix-smtp[1234]:".
	knownPostfixServices = map[string]bool{
		"anvil": true, "bounce": true, "cleanup": true, "discard": true,
		"error": true, "flush": true, "lmtp": true, "local": true,
		"pickup": true, "pipe": true, "proxymap": true, "qmgr": true,
		"rewrite": true, "smtp": true, "smtpd": true, "tlsmgr": true,
		"trivial-rewrite": true, "verify": true, "virtual": true,
	}

	// Recipient matching, e.g. "to=<user@example.com>". The word boundary
	// keeps "orig_to=<...>" from being mistaken for the real recipient.
	recipientRe = regexp.MustCompile(`\bto=<([^>]*)>`)

	// Sender matching, e.g. "from=<sender@example.com>, size=4521, nrcpt=1".
	senderRe = regexp.MustCompile(`\bfrom=<([^>]*)>`)

	// Message-ID matching, e.g. "message-id=<msg@example.com>".
	messageIDRe = regexp.MustCompile(`\bmessage-id=<([^>]*)>`)

	// Relay matching, e.g. "relay=mx.example.org[1.2.3.4]:25".
	relayRe = regexp.MustCompile(`\brelay=([^,\s]*)`)

	// Delay matching. The value is captured loosely (a comma separated
	// number is kept as one token) so that a non-numeric delay such as
	// "delay=1,234" or "delay=abc" does not make the whole line unparseable;
	// it is simply left unset.
	delayRe = regexp.MustCompile(`\bdelay=([0-9][0-9,]*(?:\.[0-9]+)?)`)

	// DSN matching, e.g. "dsn=2.0.0".
	dsnRe = regexp.MustCompile(`\bdsn=([0-9]+(?:\.[0-9]+)*)`)

	// Status matching, e.g. "status=sent (250 2.0.0 Ok)".
	statusRe = regexp.MustCompile(`\bstatus=([a-zA-Z]+)`)

	// Queue removal marker, e.g. "4S123456789: removed".
	removedRe = regexp.MustCompile(`\bremoved\b`)

	// Queue expiry marker, e.g. "4S123456789: message expired".
	expiredRe = regexp.MustCompile(`\bexpired\b`)

	// Bounce notification markers, e.g. "sender non-delivery notification: ...".
	bounceNotificationRe = regexp.MustCompile(`(?i)\bnon-delivery notification\b|\bdelivery[- ]status report\b`)

	// Authentication is accepted only from the canonical SMTP client record.
	authenticatedClientRe = regexp.MustCompile(`^client=[^,\s]+, sasl_method=[A-Za-z0-9_-]+, sasl_username=([^,\s]+)(?:, [A-Za-z_]+=[^,\s]+)*$`)
)

// syntheticEpoch anchors the deterministic fallback timestamps that are used
// when a log line carries no parseable timestamp.
//
// Fallback timestamps are deliberately NOT wall-clock times: the current clock
// would make every re-read of the same line produce a different value, and
// occurred_at is part of the delivery_events dedupe key, so a log rotation
// would count the same delivery event twice. The fallback is instead derived
// from the SHA-256 of the raw line, so re-reading the same line always yields
// the same instant and therefore the same dedupe key.
//
// The derived value lives in the fixed window [2000-01-01, 2001-01-01) UTC.
// It is a synthetic marker, not a real event time.
var syntheticEpoch = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// syntheticWindowSeconds is the size of the window the deterministic fallback
// timestamps are spread over.
const syntheticWindowSeconds = 365 * 24 * 60 * 60

// ExtractDomain parses the domain part after the @ symbol in an email address.
func ExtractDomain(address string) string {
	parts := strings.Split(address, "@")
	if len(parts) == 2 {
		return strings.ToLower(strings.TrimSpace(parts[1]))
	}
	return ""
}

// ParsePostfixLogLine parses a single raw syslog line from Postfix into a
// ParsedDeliveryEvent.
//
// It uses the host local time zone and the current clock as reference, see
// ParsePostfixLogLineWithOptions for the exact semantics.
func ParsePostfixLogLine(line string) (*ParsedDeliveryEvent, error) {
	return ParsePostfixLogLineWithOptions(line, ParserOptions{})
}

// ParsePostfixLogLineWithOptions parses a single raw syslog line from Postfix
// into a ParsedDeliveryEvent.
//
// Recognized line shapes:
//
//	Oct 23 14:00:00 mail postfix/qmgr[1234]: 4S123456789: from=<a@b>, size=1, nrcpt=1 (queue active)
//	2026-10-23T14:00:00+08:00 mail postfix-out/smtp[1234]: 4S123456789: to=<a@b>, relay=mx[1.2.3.4]:25, delay=1.4, dsn=2.0.0, status=sent (250 Ok)
//	Oct 23 14:00:00 mail postfix/bounce[1234]: 4S123456789: sender non-delivery notification: 5A1B2C3D4E
//
// Timestamps:
//
//   - RFC5424 / journald ISO8601 timestamps ("2026-10-23T14:00:12+08:00")
//     carry a full date and an optional UTC offset; the offset is applied when
//     present, otherwise the configured location is used.
//   - RFC3164 syslog timestamps ("Oct 23 14:00:12") carry neither a year nor
//     an offset. They are interpreted in the configured location (Postfix logs
//     local time) and the year is inferred from the reference clock: the
//     candidate year closest to the reference wins, which resolves the
//     December/January rollover in both directions.
//   - When no timestamp can be parsed the event time is derived
//     deterministically from the raw line (see fallbackTimestamp) so that
//     re-reading the same line yields the same event time and the same dedupe
//     key.
//
// The function is pure: it performs no I/O and keeps no state.
func ParsePostfixLogLineWithOptions(line string, opts ParserOptions) (*ParsedDeliveryEvent, error) {
	prefix, ok := splitPostfixPrefix(line)
	if !ok {
		return nil, ErrNotPostfixLine
	}

	loc := opts.location()
	ref := opts.reference()

	event := &ParsedDeliveryEvent{
		QueueID:   prefix.queueID,
		Process:   prefix.process,
		Component: prefix.component,
		Status:    DeliveryStatusUnknown,
	}

	if prefix.component == "smtpd" && prefix.queueID != "NOQUEUE" {
		event.SMTPClientRecord = strings.HasPrefix(prefix.rest, "client=")
		if match := authenticatedClientRe.FindStringSubmatch(prefix.rest); len(match) == 2 {
			event.AuthenticatedMailbox = strings.ToLower(match[1])
		}
	}
	if ts, ok := parseLineTimestamp(line, prefix.tagStart, ref, loc); ok {
		event.Timestamp = ts
	} else {
		event.Timestamp = fallbackTimestamp(line)
	}

	// Correlation fields are collected from wherever the line carries them,
	// not from a single "branch" of the line, so that one event can hold a
	// message id and a recipient at the same time.
	if m := recipientRe.FindStringSubmatch(prefix.rest); m != nil {
		event.Recipient = strings.ToLower(strings.TrimSpace(m[1]))
		event.RecipientDomain = ExtractDomain(event.Recipient)
	}
	hasSender := false
	if m := senderRe.FindStringSubmatch(prefix.rest); m != nil {
		hasSender = true
		event.Sender = strings.ToLower(strings.TrimSpace(m[1]))
		event.SenderDomain = ExtractDomain(event.Sender)
	}
	if m := messageIDRe.FindStringSubmatch(prefix.rest); m != nil {
		event.MessageID = strings.TrimSpace(m[1])
	}
	if m := relayRe.FindStringSubmatch(prefix.rest); m != nil {
		event.Relay = strings.TrimSpace(m[1])
	}
	if m := dsnRe.FindStringSubmatch(prefix.rest); m != nil {
		event.DSN = m[1]
	}
	// A delay that cannot be parsed is left unset; the event itself is kept,
	// because the recipient and the status are still known.
	if m := delayRe.FindStringSubmatch(prefix.rest); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			event.DelaySeconds = v
		}
	}

	rawStatus, detail := extractStatus(prefix.rest)
	if detail != "" {
		event.ResponseDetail = detail
	}

	status, ok := resolveStatus(prefix, event, rawStatus, hasSender)
	if !ok {
		// The line belongs to Postfix but carries no delivery milestone.
		return nil, ErrNotPostfixLine
	}
	event.Status = status

	return event, nil
}

// postfixPrefix holds the parts of a Postfix syslog tag.
type postfixPrefix struct {
	process   string
	component string
	queueID   string
	rest      string
	tagStart  int
}

// splitPostfixPrefix splits a log line into the Postfix syslog tag and the
// remainder of the line. It accepts the plain "postfix/<service>" tag as well
// as the multi-instance tags used by multi_instance_wrapper(5).
func splitPostfixPrefix(line string) (postfixPrefix, bool) {
	if m := postfixPrefixRe.FindStringSubmatchIndex(line); m != nil {
		return postfixPrefix{
			process:   line[m[2]:m[3]],
			component: strings.ToLower(line[m[4]:m[5]]),
			queueID:   line[m[6]:m[7]],
			rest:      line[m[8]:m[9]],
			tagStart:  m[0],
		}, true
	}
	if m := postfixFlatPrefixRe.FindStringSubmatchIndex(line); m != nil {
		process := line[m[2]:m[3]]
		component := ""
		if name := strings.TrimPrefix(process, "postfix-"); knownPostfixServices[name] {
			component = name
		}
		return postfixPrefix{
			process:   process,
			component: component,
			queueID:   line[m[4]:m[5]],
			rest:      line[m[6]:m[7]],
			tagStart:  m[0],
		}, true
	}
	return postfixPrefix{}, false
}

// extractStatus returns the Postfix status word of a delivery status record
// and the optional detail that follows it, e.g. "sent" and
// "250 2.0.0 Ok: queued as ABCD1234".
func extractStatus(rest string) (string, string) {
	m := statusRe.FindStringSubmatchIndex(rest)
	if m == nil {
		return "", ""
	}
	raw := strings.ToLower(rest[m[2]:m[3]])
	after := rest[m[1]:]
	if !strings.HasPrefix(after, " (") {
		return raw, ""
	}
	end := strings.LastIndex(after, ")")
	if end < 2 {
		return raw, ""
	}
	return raw, after[2:end]
}

// resolveStatus maps a parsed Postfix line onto one of the delivery statuses
// accepted by the delivery_events table. The bool is false when the line
// carries no delivery milestone at all and must not be stored.
func resolveStatus(prefix postfixPrefix, event *ParsedDeliveryEvent, rawStatus string, hasSender bool) (string, bool) {
	// Authentication metadata is parsed only from the canonical client record.
	// A local-part containing "status=" cannot turn that record into an outcome.
	if event.SMTPClientRecord {
		return DeliveryStatusUnknown, true
	}
	// 1. An explicit Postfix status word always wins. Unknown words are
	//    normalised to "unknown" instead of being passed through, because
	//    the delivery_events CHECK rejects anything else.
	if rawStatus != "" {
		return mapDeliveryStatus(rawStatus), true
	}

	// 2. The bounce and error daemons report a permanent delivery failure.
	switch prefix.component {
	case "bounce", "error":
		return DeliveryStatusBounced, true
	}
	if bounceNotificationRe.MatchString(prefix.rest) {
		return DeliveryStatusBounced, true
	}

	// 3. The queue manager accepted the message for delivery. A null
	//    sender ("from=<>") is a valid Postfix line - it is the bounce of
	//    a bounce - so the presence of the field, not its content, decides.
	if prefix.component == "qmgr" && hasSender {
		return DeliveryStatusQueued, true
	}
	if prefix.component == "pickup" && event.MessageID != "" {
		return DeliveryStatusQueued, true
	}

	// 4. The queue entry expired or was removed.
	if expiredRe.MatchString(prefix.rest) {
		return DeliveryStatusExpired, true
	}
	if removedRe.MatchString(prefix.rest) {
		return DeliveryStatusRemoved, true
	}

	// 5. Anything that carries correlation data is still an event, even if
	//    the status is unknown (for example a cleanup line that only carries
	//    the message id).
	if event.Recipient != "" || hasSender || event.MessageID != "" {
		return DeliveryStatusUnknown, true
	}

	return "", false
}

// mapDeliveryStatus normalises a Postfix status word onto one of the
// statuses accepted by the delivery_events table.
func mapDeliveryStatus(raw string) string {
	switch strings.ToLower(raw) {
	case DeliveryStatusSent:
		return DeliveryStatusSent
	case DeliveryStatusDeferred:
		return DeliveryStatusDeferred
	case DeliveryStatusBounced:
		return DeliveryStatusBounced
	case DeliveryStatusExpired:
		return DeliveryStatusExpired
	case DeliveryStatusRemoved:
		return DeliveryStatusRemoved
	case DeliveryStatusQueued:
		return DeliveryStatusQueued
	default:
		// Postfix status words outside the accepted set (for example
		// "warning" or "undeliverable") would be rejected by the
		// delivery_events CHECK, so they are normalised to "unknown".
		return DeliveryStatusUnknown
	}
}

// parseLineTimestamp extracts the event time of a log line.
//
// Only the part of the line in front of the Postfix tag is inspected for an
// ISO8601 timestamp, so a timestamp-looking string inside the message body
// cannot be mistaken for the event time.
func parseLineTimestamp(line string, tagStart int, ref time.Time, loc *time.Location) (time.Time, bool) {
	head := line
	if tagStart > 0 && tagStart <= len(line) {
		head = line[:tagStart]
	}
	if ts, ok := parseISOTimestamp(head, loc); ok {
		return ts, true
	}
	return parseSyslogTimestamp(line, ref, loc)
}

// parseISOTimestamp parses an RFC5424 / journald ISO8601 timestamp such as
// "2026-10-23T14:00:12.123456+08:00". When the timestamp carries a UTC offset
// it is honored, otherwise the timestamp is interpreted in loc.
func parseISOTimestamp(s string, loc *time.Location) (time.Time, bool) {
	m := iso8601TimeRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	year, err := strconv.Atoi(m[1])
	if err != nil {
		return time.Time{}, false
	}
	month, err := strconv.Atoi(m[2])
	if err != nil {
		return time.Time{}, false
	}
	day, err := strconv.Atoi(m[3])
	if err != nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[4])
	if err != nil {
		return time.Time{}, false
	}
	minute, err := strconv.Atoi(m[5])
	if err != nil {
		return time.Time{}, false
	}
	second, err := strconv.Atoi(m[6])
	if err != nil {
		return time.Time{}, false
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 60 {
		return time.Time{}, false
	}

	nanosecond := 0
	if m[7] != "" {
		frac := m[7]
		if len(frac) > 9 {
			frac = frac[:9]
		}
		for len(frac) < 9 {
			frac += "0"
		}
		nanosecond, err = strconv.Atoi(frac)
		if err != nil {
			return time.Time{}, false
		}
	}

	zone := loc
	if m[8] != "" {
		offset := m[8]
		if offset == "Z" || offset == "z" {
			zone = time.UTC
		} else {
			sign := 1
			if offset[0] == '-' {
				sign = -1
			}
			digits := strings.ReplaceAll(offset[1:], ":", "")
			if len(digits) != 4 {
				return time.Time{}, false
			}
			hours, err1 := strconv.Atoi(digits[:2])
			minutes, err2 := strconv.Atoi(digits[2:])
			if err1 != nil || err2 != nil {
				return time.Time{}, false
			}
			zone = time.FixedZone("", sign*(hours*3600+minutes*60))
		}
	}

	return time.Date(year, time.Month(month), day, hour, minute, second, nanosecond, zone), true
}

// parseSyslogTimestamp extracts an RFC3164 syslog timestamp ("Oct 23
// 14:00:01") and converts it to a time in loc.
//
// The classic syslog format carries neither a year nor an offset. The year is
// inferred from the reference clock: of the three candidate years around the
// reference, the one that puts the timestamp closest to the reference wins.
// That resolves the December/January rollover in both directions - a
// "Dec 31" line read in January belongs to the previous year, a "Jan 1" line
// read in December belongs to the next year.
func parseSyslogTimestamp(line string, ref time.Time, loc *time.Location) (time.Time, bool) {
	m := syslogTimeRe.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, false
	}
	month, ok := syslogMonths[m[1]]
	if !ok {
		return time.Time{}, false
	}
	day, err := strconv.Atoi(m[2])
	if err != nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[3])
	if err != nil {
		return time.Time{}, false
	}
	minute, err := strconv.Atoi(m[4])
	if err != nil {
		return time.Time{}, false
	}
	second, err := strconv.Atoi(m[5])
	if err != nil {
		return time.Time{}, false
	}
	if day < 1 || day > 31 {
		return time.Time{}, false
	}

	local := ref.In(loc)
	best := time.Time{}
	bestDelta := time.Duration(math.MaxInt64)
	for _, year := range []int{local.Year() - 1, local.Year(), local.Year() + 1} {
		candidate := time.Date(year, month, day, hour, minute, second, 0, loc)
		// Reject impossible dates such as "Feb 30" instead of silently
		// rolling them over into the next month.
		if candidate.Month() != month || candidate.Day() != day {
			continue
		}
		delta := candidate.Sub(local)
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			bestDelta = delta
			best = candidate
		}
	}
	if best.IsZero() {
		return time.Time{}, false
	}
	return best, true
}

// fallbackTimestamp derives a stable timestamp from the raw log line.
//
// The current clock must not be used here: occurred_at is part of the
// delivery_events dedupe key, so a clock based fallback would make a replayed
// line look like a new event after a log rotation and the same delivery would
// be counted twice. The value is derived from the SHA-256 of the raw line
// instead, so the same line always yields the same instant - and therefore
// the same dedupe key - no matter when it is read.
//
// The result is a synthetic marker inside the fixed window
// [2000-01-01, 2001-01-01) UTC, not a wall-clock time.
func fallbackTimestamp(line string) time.Time {
	sum := sha256.Sum256([]byte(line))
	offset := binary.BigEndian.Uint64(sum[:8]) % syntheticWindowSeconds
	return syntheticEpoch.Add(time.Duration(offset) * time.Second)
}
