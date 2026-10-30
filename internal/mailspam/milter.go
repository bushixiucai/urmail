// Milter wire transport for the spam verdict service.
//
// This file implements the Postfix milter protocol (v6 subset) needed to
// observe a message and insert spam verdict headers. The framing follows the
// same length-prefixed, one-command-byte design as internal/mail/frommilter;
// it is deliberately self-contained because this transport needs WRITE
// actions (SMFIR_INSHEADER at end-of-body), RCPT and BODY events, none of
// which the From-authorization enforcer negotiates or handles.
//
// Protocol subset handled:
//
//	'O' SMFIC_OPTNEG   -> negotiate version 6 with SMFIF_ADDHDRS; keep RCPT,
//	                      HEADER and BODY events enabled
//	'D' SMFIC_MACRO    -> accumulate macro key=value pairs (no reply)
//	'C' SMFIC_CONNECT  -> continue
//	'H' SMFIC_HELO     -> continue
//	'M' SMFIC_MAIL     -> reset per-message state; capture envelope from
//	'R' SMFIC_RCPT     -> capture envelope recipients (needed for the
//	                      per-mailbox policy lookup)
//	'T' SMFIC_DATA     -> continue
//	'L' SMFIC_HEADER   -> collect headers (bounded)
//	'N' SMFIC_EOH      -> continue (classification happens at EOM so the
//	                      body sample is available)
//	'B' SMFIC_BODY     -> append to the bounded body sample; continue
//	'E' SMFIC_BODYEOB  -> classify; reply with SMFIR_INSHEADER actions and
//	                      continue, or reject when explicitly configured
//	'A' SMFIC_ABORT    -> reset session state (no reply)
//	'Q' SMFIC_QUIT     -> close
//
// Safety: this milter NEVER discards, quarantines or silently drops mail.
// The only actions it can send are header insertions, continue, and (only
// when DenyActions is explicitly enabled in configuration, default OFF) a
// rejection of messages whose verdict reached the deny threshold.

package mailspam

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mailauth"
)

// Milter protocol command bytes (SMFIC_*).
const (
	cmdAbort   = 'A'
	cmdBody    = 'B'
	cmdConnect = 'C'
	cmdMacro   = 'D'
	cmdEOB     = 'E'
	cmdHelo    = 'H'
	cmdHeader  = 'L'
	cmdMail    = 'M'
	cmdOptNeg  = 'O'
	cmdEOH     = 'N'
	cmdQuit    = 'Q'
	cmdRcpt    = 'R'
	cmdData    = 'T'
	cmdUnknown = 'U'
)

// Milter response bytes (SMFIR_*).
const (
	resContinue  = 'c'
	resReject    = 'r'
	resAccept    = 'a'
	resInsHeader = 'i'
	resChgHeader = 'm'
)

// Negotiated capability bits.
const (
	protoVersion  = 6
	actionAddHdrs = 1 << 0
	actionChgHdrs = 1 << 4 // Strip sender-supplied verdict headers before adding trusted ones.
	// maxPacket bounds one wire packet; headers are small, body chunks are
	// split by the MTA. Same bound as the frommilter transport.
	maxPacket = 1 << 21 // 2 MiB

	// maxHeaders bounds collected headers per message (header storms are a
	// known milter resource risk).
	maxHeaders = 512
	// DefaultMaxBodySampleBytes bounds the body kept for classification;
	// token evidence rarely needs more, and the memory ceiling is then
	// bounded per connection.
	DefaultMaxBodySampleBytes = 64 << 10 // 64 KiB
	// DefaultIdleTimeout bounds one wire read; a stuck client cannot pin a
	// goroutine forever.
	DefaultIdleTimeout = 5 * time.Minute
)

// MilterConfig controls the wire layer only; classification thresholds live
// in the pure engine constants.
type MilterConfig struct {
	// Authentication seals bounded, receive-time DKIM facts. Nil preserves
	// delivery behavior while mail without a verifiable proof remains unknown.
	Authentication *mailauth.Receiver
	// DenyActions enables SMFIR_REJECT for verdicts at DenyScoreThreshold.
	// It is OFF by default: v1 classification + Junk foldering is the
	// product behavior and nothing must be rejected by accident.
	DenyActions bool
	// DenyScoreThreshold is the score at or above which rejection applies
	// when DenyActions is enabled. The default matches the sender-list
	// weight, so only an explicit deny rule would reject.
	DenyScoreThreshold float64
	// MaxBodySampleBytes caps the retained body sample.
	MaxBodySampleBytes int
	// IdleTimeout bounds one wire read.
	IdleTimeout time.Duration
}

// setDefaults fills unset fields.
func (c MilterConfig) withDefaults() MilterConfig {
	if c.MaxBodySampleBytes <= 0 {
		c.MaxBodySampleBytes = DefaultMaxBodySampleBytes
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.DenyScoreThreshold <= 0 {
		c.DenyScoreThreshold = WeightSenderList
	}
	return c
}

// VerdictRequest is everything the wire layer learned about one message.
type VerdictRequest struct {
	SASLUser     string
	EnvelopeFrom string
	Recipients   []string
	Headers      []Header
	BodySample   []byte
}

// VerdictResponse is the classification outcome expressed as milter actions.
// InsertHeaders are inserted at the top of the header block in slice order.
type VerdictResponse struct {
	InsertHeaders [][2]string
	Reject        bool
}

// VerdictHandler classifies one message. Implementations must never panic:
// a milter crash with milter_default_action=accept silently degrades to
// unflagged delivery, which is exactly what the alert surface exists to make
// visible.
type VerdictHandler interface {
	Decide(ctx context.Context, req VerdictRequest) VerdictResponse
}

// VerdictFunc adapts a function to VerdictHandler.
type VerdictFunc func(ctx context.Context, req VerdictRequest) VerdictResponse

// Decide implements VerdictHandler.
func (f VerdictFunc) Decide(ctx context.Context, req VerdictRequest) VerdictResponse {
	return f(ctx, req)
}

// HeaderNameSpam / HeaderNameScore are the two headers this service writes.
const (
	HeaderNameSpam  = "X-UrMail-Spam"
	HeaderNameScore = "X-UrMail-Spam-Score"
)

// VerdictHeaders renders a verdict as the wire-layer header pair. The value
// of X-UrMail-Spam is yes|no|unsure; the score keeps two decimals so the
// header is stable and grep-able.
func VerdictHeaders(v Verdict) [][2]string {
	flag := "no"
	switch v.Class {
	case ClassSpam:
		flag = "yes"
	case ClassUnsure:
		flag = "unsure"
	}
	return [][2]string{
		{HeaderNameSpam, flag},
		{HeaderNameScore, formatScore(v.Score)},
	}
}

func formatScore(score float64) string {
	// Fixed two-decimal formatting is deterministic across architectures and
	// locale-independent.
	return strconv.FormatFloat(score, 'f', 2, 64)
}

// packet reads one length-prefixed packet including its command byte.
func packet(r io.Reader, timeout time.Duration) ([]byte, error) {
	var lenBuf [4]byte
	if err := readFullDeadline(r, lenBuf[:], timeout); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length == 0 || length > maxPacket {
		return nil, errors.New("mailspam: packet length out of bounds")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func readFullDeadline(r io.Reader, buf []byte, timeout time.Duration) error {
	type deadliner interface{ SetReadDeadline(time.Time) error }
	if d, ok := r.(deadliner); ok {
		if err := d.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
	}
	_, err := io.ReadFull(r, buf)
	return err
}

func writeResponse(w io.Writer, response byte) error {
	buf := []byte{0, 0, 0, 1, response}
	binary.BigEndian.PutUint32(buf[:4], 1)
	_, err := w.Write(buf)
	return err
}

// writePayload writes one action packet with a payload.
func writePayload(w io.Writer, response byte, payload []byte) error {
	if len(payload) > maxPacket-1 {
		return errors.New("mailspam: action payload too large")
	}
	buf := make([]byte, 5+len(payload))
	//nolint:gosec // G115: len(payload) <= maxPacket-1 by the guard above.
	binary.BigEndian.PutUint32(buf[:4], uint32(1+len(payload)))
	buf[4] = response
	copy(buf[5:], payload)
	_, err := w.Write(buf)
	return err
}

// optNeg replies to SMFIC_OPTNEG: protocol v6, SMFIF_ADDHDRS, and NO skip
// flags because RCPT and BODY events are required for the per-mailbox policy
// lookup and the content sample.
func optNeg(w io.Writer) error {
	reply := make([]byte, 17)
	reply[4] = cmdOptNeg
	binary.BigEndian.PutUint32(reply[5:9], protoVersion)
	binary.BigEndian.PutUint32(reply[9:13], actionAddHdrs|actionChgHdrs)
	binary.BigEndian.PutUint32(reply[13:17], 0) // keep every event we need
	binary.BigEndian.PutUint32(reply[:4], 13)
	_, err := w.Write(reply)
	return err
}

// splitNUL decodes an NUL-separated payload into fields.
func splitNUL(payload []byte) []string {
	parts := strings.Split(string(payload), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// address cleanup: milter MAIL/RCPT payloads carry the address as
// "<user@example.com>" possibly followed by ESMTP parameters.
func cleanAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if at := strings.IndexByte(raw, '<'); at >= 0 {
		if end := strings.IndexByte(raw[at:], '>'); end > 0 {
			raw = raw[at+1 : at+end]
		} else {
			raw = raw[at+1:]
		}
	}
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, "postmaster") {
		return raw
	}
	return strings.ToLower(raw)
}

// Full-message DKIM capture is an optional enrichment, never a delivery gate.
// Limit concurrent 16 MiB captures and their verification buffers; overload
// delivers normally without a proof instead of retaining unbounded MIME bodies.
var authenticationCaptureSlots = make(chan struct{}, 4)

// session is one connection's mutable state.
type session struct {
	cfg                      MilterConfig
	handler                  VerdictHandler
	macros                   map[string]string
	recipients               []string
	headers                  []Header
	body                     []byte
	authenticationBody       []byte
	authenticationIncomplete bool
	authenticationSlot       bool
	headerBytes              int
	envelopeFrom             string
	verdictHeaders           map[string]uint32
	// dirty marks that a message is in flight (MAIL seen, not yet EOM/ABORT).
	dirty bool
}

func (s *session) resetMessage() {
	if s.authenticationSlot {
		<-authenticationCaptureSlots
		s.authenticationSlot = false
	}
	s.recipients = nil
	s.headers = nil
	s.body = nil
	s.authenticationBody = nil
	s.authenticationIncomplete = false
	s.headerBytes = 0
	s.envelopeFrom = ""
	s.verdictHeaders = nil
	s.dirty = false
}

func (s *session) startAuthenticationCapture() {
	if s.cfg.Authentication == nil {
		return
	}
	select {
	case authenticationCaptureSlots <- struct{}{}:
		s.authenticationSlot = true
	default:
		s.authenticationIncomplete = true
	}
}

// handlePacket advances the session for one command. The returned actions are
// response packets to write (nil means no reply), and done reports connection
// completion.
func (s *session) handlePacket(ctx context.Context, pkt []byte, w io.Writer) (done bool, err error) {
	if len(pkt) == 0 {
		return true, errors.New("mailspam: empty packet")
	}
	cmd, payload := pkt[0], pkt[1:]
	switch cmd {
	case cmdOptNeg:
		return false, optNeg(w)
	case cmdMacro:
		// SMFIC_MACRO packets expect NO reply; answering would desynchronize
		// the response stream.
		if len(payload) == 0 {
			return false, nil
		}
		fields := splitNUL(payload[1:])
		if s.macros == nil {
			s.macros = make(map[string]string, 8)
		}
		for i := 0; i+1 < len(fields); i += 2 {
			s.macros[fields[i]] = fields[i+1]
		}
		return false, nil
	case cmdConnect, cmdHelo, cmdData, cmdUnknown:
		return false, writeResponse(w, resContinue)
	case cmdMail:
		// Macros for the MAIL stage (SASL identity) arrive BEFORE the MAIL
		// command, so they must survive into this event; stale macros were
		// already cleared at the previous EOM/ABORT boundary.
		s.resetMessage()
		s.startAuthenticationCapture()
		s.dirty = true
		if len(payload) > 0 {
			fields := splitNUL(payload)
			if len(fields) > 0 {
				s.envelopeFrom = cleanAddress(fields[0])
			}
		}
		return false, writeResponse(w, resContinue)
	case cmdRcpt:
		fields := splitNUL(payload)
		if len(fields) > 0 {
			if addr := cleanAddress(fields[0]); addr != "" && len(s.recipients) < maxHeaders {
				s.recipients = append(s.recipients, addr)
			}
		}
		return false, writeResponse(w, resContinue)
	case cmdHeader:
		fields := splitNUL(payload)
		if len(fields) >= 1 && isVerdictHeader(fields[0]) {
			if s.verdictHeaders == nil {
				s.verdictHeaders = make(map[string]uint32)
			}
			s.verdictHeaders[canonicalVerdictHeader(fields[0])]++
		}
		if len(fields) >= 1 {
			s.headerBytes += len(fields[0])
			if len(fields) >= 2 {
				s.headerBytes += len(fields[1])
			}
			if len(s.headers) >= maxHeaders || s.headerBytes > mailauth.MaxHeaderBytes {
				s.authenticationIncomplete = true
			}
		}
		if len(fields) >= 1 && len(s.headers) < maxHeaders && s.headerBytes <= mailauth.MaxHeaderBytes {
			name := fields[0]
			value := ""
			if len(fields) >= 2 {
				value = fields[1]
			}
			s.headers = append(s.headers, Header{Name: name, Value: value})
		}
		return false, writeResponse(w, resContinue)
	case cmdEOH:
		return false, writeResponse(w, resContinue)
	case cmdBody:
		if s.dirty && s.authenticationSlot && !s.authenticationIncomplete {
			if len(s.authenticationBody)+len(payload) > mailauth.MaxBodyBytes {
				s.authenticationIncomplete = true
				s.authenticationBody = nil
			} else {
				s.authenticationBody = append(s.authenticationBody, payload...)
			}
		}
		if s.dirty && len(s.body) < s.cfg.MaxBodySampleBytes {
			room := s.cfg.MaxBodySampleBytes - len(s.body)
			if len(payload) < room {
				room = len(payload)
			}
			s.body = append(s.body, payload[:room]...)
		}
		return false, writeResponse(w, resContinue)
	case cmdEOB:
		return false, s.finishMessage(ctx, w)
	case cmdAbort:
		// ABORT expects no reply per the milter protocol.
		s.resetMessage()
		s.macros = nil
		return false, nil
	case cmdQuit:
		return true, nil
	default:
		return false, writeResponse(w, resContinue)
	}
}

// finishMessage classifies at end-of-body and emits the response actions.
func (s *session) finishMessage(ctx context.Context, w io.Writer) error {
	req := VerdictRequest{
		SASLUser:     s.macros["{auth_authen}"],
		EnvelopeFrom: s.envelopeFrom,
		Recipients:   s.recipients,
		Headers:      s.headers,
		BodySample:   s.body,
	}
	verdict := VerdictResponse{}
	if s.handler != nil {
		// A handler panic must not take the whole milter process down; the
		// degrade path (accept unflagged) is Postfix-side, so we surface the
		// panic as a connection error and let the metrics see it.
		verdict = s.handler.Decide(ctx, req)
	}
	if s.cfg.Authentication != nil && s.authenticationSlot && !s.authenticationIncomplete {
		headers := make([]mailauth.Header, 0, len(s.headers))
		for _, header := range s.headers {
			headers = append(headers, mailauth.Header{Name: header.Name, Value: header.Value})
		}
		if token, attestErr := s.cfg.Authentication.Attest(ctx, headers, s.authenticationBody); attestErr == nil && token != "" {
			verdict.InsertHeaders = append(verdict.InsertHeaders, [2]string{mailauth.HeaderName, token})
		}
	}
	if !s.cfg.DenyActions {
		verdict.Reject = false
	}
	// SMFIR_CHGHEADER uses a 32-bit network-order, one-based occurrence index.
	// Delete every incoming verdict occurrence, including ones beyond the
	// bounded classification header sample; clients must not forge delivery tags.
	for _, name := range []string{HeaderNameSpam, HeaderNameScore, mailauth.HeaderName} {
		count := s.verdictHeaders[name]
		for index := count; index > 0; index-- {
			payload := make([]byte, 4, 4+len(name)+2)
			binary.BigEndian.PutUint32(payload, index)
			payload = append(payload, name...)
			payload = append(payload, 0, 0)
			if err := writePayload(w, resChgHeader, payload); err != nil {
				return err
			}
		}
	}
	s.resetMessage()
	s.macros = nil
	if verdict.Reject {
		recordVerdict(Verdict{Class: ClassSpam}, true)
		return writeResponse(w, resReject)
	}
	// Insert at the top of the header block. Inserting in reverse order at
	// index 0 yields the request order top-down in the final message (each
	// later insert at position 0 lands above the previous one).
	for i := len(verdict.InsertHeaders) - 1; i >= 0; i-- {
		name, value := verdict.InsertHeaders[i][0], verdict.InsertHeaders[i][1]
		payload := make([]byte, 0, 4+len(name)+1+len(value)+1)
		var index [4]byte
		binary.BigEndian.PutUint32(index[:], 0)
		payload = append(payload, index[:]...)
		payload = append(payload, name...)
		payload = append(payload, 0)
		payload = append(payload, value...)
		payload = append(payload, 0)
		if err := writePayload(w, resInsHeader, payload); err != nil {
			return err
		}
	}
	return writeResponse(w, resContinue)
}

// serveConn runs one connection to completion.
func serveConn(ctx context.Context, conn net.Conn, cfg MilterConfig, h VerdictHandler) error {
	defer func() { _ = conn.Close() }()
	s := &session{cfg: cfg.withDefaults(), handler: h}
	defer s.resetMessage()
	for {
		pkt, err := packet(conn, cfg.withDefaults().IdleTimeout)
		if err != nil {
			recordSession("error")
			return err
		}
		done, err := s.handlePacket(ctx, pkt, conn)
		if err != nil {
			recordSession("error")
			return err
		}
		if done {
			recordSession("ok")
			return nil
		}
	}
}

// isVerdictHeader identifies private delivery classifications supplied by us.
func isVerdictHeader(name string) bool {
	return strings.EqualFold(name, HeaderNameSpam) || strings.EqualFold(name, HeaderNameScore) || strings.EqualFold(name, mailauth.HeaderName)
}

func canonicalVerdictHeader(name string) string {
	if strings.EqualFold(name, mailauth.HeaderName) {
		return mailauth.HeaderName
	}
	if strings.EqualFold(name, HeaderNameSpam) {
		return HeaderNameSpam
	}
	return HeaderNameScore
}
