// Package imageproxy fetches remote mail images on the server's behalf so
// that a third-party host never learns the reader's IP address, user agent
// or online state. Every request is authorized by a short-lived HMAC
// signature that binds the mailbox, the message and the target URL, and
// every target is screened against SSRF before a socket is opened.
package imageproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// SignatureTTL is how long a signed image URL stays valid. It is long
	// enough to render a message that sits in a background tab and short
	// enough that a leaked URL stops working quickly.
	SignatureTTL = 6 * time.Hour
)

// Errors reported by Signer.Verify.
var (
	// ErrNoKey means the proxy has no signing key configured; the feature
	// stays disabled and remote images remain blocked.
	ErrNoKey = errors.New("imageproxy: no signing key")
	// ErrBadSignature means the token does not match the signature or is
	// malformed.
	ErrBadSignature = errors.New("imageproxy: invalid signature")
	// ErrExpired means the signature is well formed but past its TTL.
	ErrExpired = errors.New("imageproxy: signature expired")
)

// Payload is the signed description of one proxied image.
type Payload struct {
	// MailboxID owns the message the image came from.
	MailboxID int64 `json:"m"`
	// UID is the message UID inside that mailbox.
	UID uint32 `json:"u"`
	// Expires is the Unix second the signature stops working.
	Expires int64 `json:"e"`
	// URL is the absolute remote image URL to fetch.
	URL string `json:"r"`
}

// Signer signs and verifies image proxy payloads.
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

// Sign encodes p into an opaque token and its detached signature. A zero
// Expires is filled with the default TTL.
func (s *Signer) Sign(p Payload) (token, signature string, err error) {
	if !s.Enabled() {
		return "", "", ErrNoKey
	}
	if p.Expires == 0 {
		p.Expires = time.Now().Add(SignatureTTL).Unix()
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", "", fmt.Errorf("imageproxy: encode payload: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, s.sign(token), nil
}

// Verify checks the signature and the expiry of a token pair and returns
// the payload it protects. Malformed tokens and bad signatures are
// reported identically so the endpoint does not leak which part failed.
func (s *Signer) Verify(token, signature string) (Payload, error) {
	if !s.Enabled() {
		return Payload{}, ErrNoKey
	}
	if token == "" || signature == "" {
		return Payload{}, ErrBadSignature
	}
	if !hmac.Equal([]byte(s.sign(token)), []byte(signature)) {
		return Payload{}, ErrBadSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
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

func (s *Signer) sign(token string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
