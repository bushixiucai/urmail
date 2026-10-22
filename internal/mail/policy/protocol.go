package policy

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Request holds attributes passed from Postfix in an access policy request.
type Request struct {
	ClientAddress string // client_address
	QueueID       string // queue_id
	ClientName    string // client_name
	ReverseClient string // reverse_client_name
	Sender        string // sender
	Recipient     string // recipient
	SASLUsername  string // sasl_username
	SASLMethod    string // sasl_method
	HeloName      string // helo_name
	Size          int64  // size
	Attrs         map[string]string
}

// Action represents a policy decision.
type Action string

const (
	ActionDunno  Action = "DUNNO"
	ActionReject Action = "REJECT"
	ActionDefer  Action = "DEFER_IF_PERMIT"
)

// Decision contains the action and an optional human-readable reason.
type Decision struct {
	Action Action
	Reason string
}

func (d Decision) String() string {
	if d.Reason != "" && d.Action != ActionDunno {
		return fmt.Sprintf("action=%s %s", d.Action, d.Reason)
	}
	return fmt.Sprintf("action=%s", d.Action)
}

// ReadRequest parses a Postfix policy request from a reader.
// Returns (nil, nil) if EOF is reached before a new request starts.
func ReadRequest(r *bufio.Reader) (*Request, error) {
	attrs := make(map[string]string)
	hasAttrs := false

	for {
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			// Empty line marks end of request
			if !hasAttrs && err == io.EOF {
				return nil, nil
			}
			break
		}
		key, val, ok := strings.Cut(trimmed, "=")
		if ok {
			attrs[key] = val
			hasAttrs = true
		}
		if err == io.EOF {
			break
		}
	}

	if !hasAttrs {
		return nil, nil
	}

	req := &Request{
		ClientAddress: attrs["client_address"],
		QueueID:       attrs["queue_id"],
		ClientName:    attrs["client_name"],
		ReverseClient: attrs["reverse_client_name"],
		Sender:        attrs["sender"],
		Recipient:     attrs["recipient"],
		SASLUsername:  attrs["sasl_username"],
		SASLMethod:    attrs["sasl_method"],
		HeloName:      attrs["helo_name"],
		Attrs:         attrs,
	}
	return req, nil
}

// WriteDecision outputs the decision to a writer in Postfix policy format.
func WriteDecision(w io.Writer, d Decision) error {
	_, err := fmt.Fprintf(w, "%s\n\n", d.String())
	return err
}
