// Package frommilter implements a minimal Postfix milter server that enforces
// the MAIL-04 From-authorization decision (internal/mail/frombind) on
// authenticated submissions. It speaks the milter wire protocol (v6 subset):
// length-prefixed packets, one command byte per packet.
//
// Protocol subset handled, per Postfix smilter(7)/milter protocol docs:
//
//	'O' SMFIC_OPTNEG  -> negotiate version 6, no write actions, skip
//	                     unknown/RCPT/body events (headers and EOH still sent)
//	'D' SMFIC_MACRO   -> key=value pairs for the following stage
//	'C' SMFIC_CONNECT -> continue
//	'H' SMFIC_HELO    -> continue
//	'M' SMFIC_MAIL    -> capture {auth_authen} macro value; continue
//	'R' SMFIC_RCPT    -> skipped via SMFIP_NORCPT, tolerated with continue
//	'L' SMFIC_HEADER  -> collect From headers; continue
//	'N' SMFIC_EOH     -> run the authorization decision; reject or continue
//	'A' SMFIC_ABORT   -> reset session state; continue
//	'T' SMFIC_DATA    -> continue
//	'Q' SMFIC_QUIT    -> close connection
//
// Rejecting is done at EOH with SMFIC_REJECT ('r') so no body is ever
// accepted for an unauthorized From. Decisions are delegated to Handler.
package frommilter

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
)

// Milter protocol command bytes.
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

// Response bytes.
const (
	resContinue = 'c'
	resReject   = 'r'
	resAccept   = 'a'
)

// Protocol flag bits (SMFIP_*): events we ask the MTA NOT to send. Skipping
// RCPT and BODY halves the traffic; header and EOH events must stay enabled
// because the decision needs them.
const (
	flagNoRcpt = 1 << 2
	flagNoBody = 1 << 4
	flagNoEOH  = 1 << 5
	flagNoHDRS = 1 << 6
)

const (
	protoVersion = 6
	maxPacket    = 1 << 21 // 2 MiB: bounded headers per message
)

// Handler resolves the authorization for one message.
type Handler interface {
	// Authorize receives the authenticated login ("" when the session has no
	// SMTP AUTH) and every From header value in order. It reports whether the
	// message must be rejected at end-of-header.
	Authorize(login string, fromHeaders []string) (reject bool, reason string)
}

// Server accepts milter connections and enforces h.
type Server struct {
	Handler Handler
}

// packet reads one length-prefixed packet. The returned slice includes the
// command byte at index 0.
func packet(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length == 0 || length > maxPacket {
		return nil, errors.New("frommilter: packet length out of bounds")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeResponse(w io.Writer, response byte) error {
	buf := []byte{0, 0, 0, 1, response}
	binary.BigEndian.PutUint32(buf[:4], 1)
	_, err := w.Write(buf)
	return err
}

// optNeg replies to SMFIC_OPTNEG: protocol version 6, no write actions, and
// skip flags for events the enforcer does not need.
func optNeg(w io.Writer, payload []byte) error {
	if len(payload) < 12 {
		return errors.New("frommilter: short OPTNEG payload")
	}
	reply := make([]byte, 17)
	reply[4] = cmdOptNeg
	binary.BigEndian.PutUint32(reply[5:9], protoVersion)
	binary.BigEndian.PutUint32(reply[9:13], 0) // no write actions requested
	skip := binary.BigEndian.Uint32(payload[8:12]) | flagNoRcpt | flagNoBody
	binary.BigEndian.PutUint32(reply[13:17], skip)
	binary.BigEndian.PutUint32(reply[:4], 13)
	_, err := w.Write(reply)
	return err
}

// splitNUL decodes an NUL-separated payload into fields (macros, header
// name/value, ...).
func splitNUL(payload []byte) []string {
	parts := strings.Split(string(payload), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// session is one connection's mutable state.
type session struct {
	login       string
	fromHeaders []string
}

// handlePacket advances the session for one command and reports the response
// byte ('c', 'r', 'a') plus whether the connection is finished.
func (s *session) handlePacket(h Handler, pkt []byte, w io.Writer) (response byte, done bool, err error) {
	if len(pkt) == 0 {
		return 0, true, errors.New("frommilter: empty packet")
	}
	cmd, payload := pkt[0], pkt[1:]
	switch cmd {
	case cmdOptNeg:
		return 0, false, optNeg(w, payload)
	case cmdMacro:
		// payload: one stage byte immediately followed by NUL-separated
		// name/value pairs (no separator between the stage byte and the first
		// name). Only the MAIL stage carries {auth_authen}.
		if len(payload) == 0 {
			return resContinue, false, nil
		}
		stage := payload[0]
		fields := splitNUL(payload[1:])
		if stage == 'M' {
			for i := 0; i+1 < len(fields); i += 2 {
				if fields[i] == "{auth_authen}" {
					s.login = fields[i+1]
				}
			}
		}
		return resContinue, false, nil
	case cmdConnect, cmdHelo, cmdData, cmdUnknown:
		return resContinue, false, nil
	case cmdMail:
		return resContinue, false, nil
	case cmdRcpt:
		return resContinue, false, nil
	case cmdHeader:
		fields := splitNUL(payload)
		if len(fields) >= 1 && strings.EqualFold(fields[0], "From") {
			value := ""
			if len(fields) >= 2 {
				value = fields[1]
			}
			s.fromHeaders = append(s.fromHeaders, value)
		}
		return resContinue, false, nil
	case cmdEOH:
		reject, _ := h.Authorize(s.login, s.fromHeaders)
		if reject {
			return resReject, false, nil
		}
		return resContinue, false, nil
	case cmdAbort:
		s.reset()
		return resContinue, false, nil
	case cmdEOB:
		return resContinue, false, nil
	case cmdQuit:
		return resAccept, true, nil
	default:
		return resContinue, false, nil
	}
}

func (s *session) reset() {
	s.login = ""
	s.fromHeaders = nil
}

// serveConn runs one connection to completion.
func serveConn(conn net.Conn, h Handler) error {
	defer func() { _ = conn.Close() }()
	s := &session{}
	for {
		pkt, err := packet(conn)
		if err != nil {
			return err
		}
		response, done, err := s.handlePacket(h, pkt, conn)
		if err != nil {
			return err
		}
		if response != 0 {
			if err := writeResponse(conn, response); err != nil {
				return err
			}
		}
		if done {
			return nil
		}
	}
}

// Serve accepts connections on l until l is closed.
func Serve(l net.Listener, h Handler) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			_ = serveConn(conn, h)
		}()
	}
}
