package mail

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
)

// DKIMKeyPair holds the generated private and public key material.
type DKIMKeyPair struct {
	Selector   string // e.g. "mail"
	PrivateKey string // PEM formatted RSA private key
	PublicKey  string // Base64 DER formatted public key for DNS TXT
	DNSTXT     string // Full DNS TXT value e.g. "v=DKIM1; k=rsa; p=MIGfMA0GCS..."
}

// GenerateDKIMKeypair generates a 2048-bit RSA keypair for DKIM signing.
func GenerateDKIMKeypair(selector string) (*DKIMKeyPair, error) {
	if selector == "" {
		selector = "mail"
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}

	// Marshal private key to PEM
	privBytes := x509.MarshalPKCS1PrivateKey(key)
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privBytes,
	})

	// Marshal public key to DER/Base64
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pubBytes)

	return &DKIMKeyPair{
		Selector:   selector,
		PrivateKey: string(privPEM),
		PublicKey:  pubB64,
		DNSTXT:     fmt.Sprintf("v=DKIM1; k=rsa; p=%s", pubB64),
	}, nil
}
