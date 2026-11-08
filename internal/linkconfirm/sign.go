// Package linkconfirm signs outbound mail links so the reader must
// confirm before leaving the webmail origin. The confirmation page is
// the only place that shows the real target, and it renders the
// hostname character by character so look-alike domains cannot hide
// inside a familiar word.
package linkconfirm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// SignatureTTL is how long a confirmation token stays valid.
	SignatureTTL = 24 * time.Hour
)

var (
	// ErrNoKey means the confirmation feature has no signing key.
	ErrNoKey = errors.New("linkconfirm: no signing key")
	// ErrBadSignature means the token does not match the signature.
	ErrBadSignature = errors.New("linkconfirm: invalid signature")
	// ErrExpired means the token is past its TTL.
	ErrExpired = errors.New("linkconfirm: signature expired")
)

// Payload is the signed description of one outbound link.
type Payload struct {
	MailboxID int64  `json:"m"`
	UID       uint32 `json:"u"`
	Expires   int64  `json:"e"`
	URL       string `json:"r"`
	From      string `json:"f,omitempty"`
	Subject   string `json:"s,omitempty"`
}

// Signer signs and verifies confirmation payloads.
type Signer struct {
	key []byte
}

// NewSigner returns a Signer for key. An empty key disables the feature.
func NewSigner(key []byte) *Signer {
	return &Signer{key: key}
}

// Enabled reports whether the signer can sign payloads.
func (s *Signer) Enabled() bool {
	return s != nil && len(s.key) > 0
}

// Sign encodes p into an opaque token. A zero Expires is filled with the
// default TTL.
func (s *Signer) Sign(p Payload) (token string, err error) {
	if !s.Enabled() {
		return "", ErrNoKey
	}
	if p.Expires == 0 {
		p.Expires = time.Now().Add(SignatureTTL).Unix()
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("linkconfirm: encode payload: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

// Verify checks the signature and the expiry of a token and returns the
// payload it protects.
func (s *Signer) Verify(token string) (Payload, error) {
	if !s.Enabled() {
		return Payload{}, ErrNoKey
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Payload{}, ErrBadSignature
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return Payload{}, ErrBadSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Payload{}, ErrBadSignature
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Payload{}, ErrBadSignature
	}
	if p.URL == "" || p.MailboxID <= 0 {
		return Payload{}, ErrBadSignature
	}
	if p.Expires <= time.Now().Unix() {
		return Payload{}, ErrExpired
	}
	return p, nil
}

// NeedsConfirm reports whether an href should go through the confirmation
// page. mailto: and tel: stay direct; only absolute http(s) links confirm.
func NeedsConfirm(href string) bool {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return true
	default:
		return false
	}
}

// HostLabels splits a host into labels for monospace rendering. Each
// rune is kept as its own string so the confirmation page can render
// them one by one and defeat look-alike domains that rely on blending.
func HostLabels(host string) []string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return nil
	}
	out := make([]string, 0, len(host))
	for _, r := range host {
		out = append(out, string(r))
	}
	return out
}
