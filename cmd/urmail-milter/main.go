// Command urmail-milter is the outbound DKIM signing milter service
// (compose urmail-signer profile, command ["urmail-milter", "sign"]).
//
// It listens on the endpoint Postfix's rendered smtpd_milters chain
// references (default inet 0.0.0.0:12301, published to the host loopback
// by the compose service so the host-network Postfix reaches it), signs
// authenticated local submissions whose From-domain has a provisioned key
// in the OpenDKIM-compatible key directory, and passes everything else
// through unsigned (the chain's milter_default_action=accept stays the
// degrade posture).
//
// Environment:
//
//	URMAIL_MILTER_LISTEN   listen address (default 0.0.0.0:12301)
//	URMAIL_DKIM_KEY_DIR    OpenDKIM-compatible key directory
//	                       (<dir>/<domain>/<selector>.private; required)
//	URMAIL_DKIM_SELECTOR   selector, default "mail" (matches the existing
//	                       mail._domainkey DNS records — no DNS change)
package main

import (
	"context"
	"fmt"
	"net/mail"
	"os"
	"strings"

	"github.com/bushixiucai/urmail/internal/dkimsign"
	"github.com/bushixiucai/urmail/internal/mailspam"
)

type signHandler struct {
	store *dkimsign.Store
}

// Decide signs authenticated local submissions: it extracts the From
// domain, looks up the provisioned key, and returns the DKIM-Signature
// header for insertion at the top of the header block. Unkeyed domains and
// foreign senders pass through unsigned.
func (h *signHandler) Decide(_ context.Context, req mailspam.VerdictRequest) mailspam.VerdictResponse {
	from := ""
	for _, header := range req.Headers {
		if strings.EqualFold(header.Name, "From") {
			from = header.Value
			break
		}
	}
	address, err := mail.ParseAddress(strings.TrimPrefix(from, " "))
	if err != nil {
		return mailspam.VerdictResponse{}
	}
	at := strings.LastIndex(address.Address, "@")
	if at <= 0 {
		return mailspam.VerdictResponse{}
	}
	domain := strings.ToLower(address.Address[at+1:])
	key, ok, err := h.store.KeyFor(domain)
	if err != nil || !ok {
		return mailspam.VerdictResponse{}
	}
	message := append(append([]byte{}, headersBytes(req.Headers)...), req.FullBody...)
	if !strings.HasSuffix(string(message), "\r\n") {
		message = append(message, '\r', '\n')
	}
	value, err := dkimsign.SignHeader(message, key, domain, h.store.Selector())
	if err != nil {
		// Signing failure degrades to unsigned delivery: never block mail
		// on the signer's own errors (the chain default is accept).
		return mailspam.VerdictResponse{}
	}
	return mailspam.VerdictResponse{InsertHeaders: [][2]string{{"DKIM-Signature", value}}}
}

// headersBytes reassembles the milter header block in received order.
func headersBytes(headers []mailspam.Header) []byte {
	var out []byte
	for _, h := range headers {
		out = append(out, h.Name...)
		out = append(out, ':')
		if !strings.HasPrefix(h.Value, " ") {
			out = append(out, ' ')
		}
		out = append(out, h.Value...)
		out = append(out, '\r', '\n')
	}
	out = append(out, '\r', '\n')
	return out
}

func main() {
	listen := os.Getenv("URMAIL_MILTER_LISTEN")
	if listen == "" {
		listen = "0.0.0.0:12301"
	}
	selector := os.Getenv("URMAIL_DKIM_SELECTOR")
	if selector == "" {
		selector = "mail"
	}
	keyDir := os.Getenv("URMAIL_DKIM_KEY_DIR")
	if keyDir == "" {
		fmt.Fprintln(os.Stderr, "urmail-milter: URMAIL_DKIM_KEY_DIR is required")
		os.Exit(1)
	}
	store := dkimsign.NewStore(keyDir, selector)
	if domains, err := store.SignDomains(); err == nil {
		fmt.Printf("urmail-milter: signing domains %v (selector %q)\n", domains, selector)
	} else {
		fmt.Fprintf(os.Stderr, "urmail-milter: key dir %s unreadable: %v\n", keyDir, err)
	}
	server := &mailspam.Server{
		Addr:    listen,
		Handler: &signHandler{store: store},
		Config:  mailspam.MilterConfig{KeepFullBody: true},
	}
	ctx := context.Background()
	if err := server.ListenAndServe(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "urmail-milter: %v\n", err)
		os.Exit(1)
	}
}
