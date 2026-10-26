// Package secrets encrypts small application secrets (TOTP seeds) with a key
// derived from the installer-generated master key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

const prefix = "enc:v1:"

// Box is an AES-256-GCM wrapper around a 32-byte key.
type Box struct {
	aead cipher.AEAD
}

// Load derives the encryption key from the master key at path.
// Accepts either an RSA private key PEM (SHA-256 of PKCS#1 DER) or a raw
// 32-byte AES key (installer / cutover hosts sometimes ship the latter).
func Load(path string) (*Box, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // The master-key path is trusted operator configuration.
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(raw) == 32 {
		return New(raw)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("master key is not PEM")
	}
	var der []byte
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		der = x509.MarshalPKCS1PrivateKey(key)
	} else if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("master key is not RSA")
		}
		der = x509.MarshalPKCS1PrivateKey(key)
	} else {
		return nil, fmt.Errorf("parse master key: %w", err)
	}
	sum := sha256.Sum256(der)
	return New(sum[:])
}

// New builds a Box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// EncryptString returns a versioned, base64-encoded ciphertext.
func (b *Box) EncryptString(plain string) (string, error) {
	if b == nil {
		return plain, nil
	}
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plain)+b.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ciphertext := b.aead.Seal(nil, nonce, []byte(plain), nil)
	nonce = append(nonce, ciphertext...)
	return prefix + base64.RawStdEncoding.EncodeToString(nonce), nil
}

// DecryptString accepts both encrypted values and legacy plaintext. The
// plaintext fallback is required while existing rows are migrated.
func (b *Box) DecryptString(value string) (string, error) {
	if b == nil || !strings.HasPrefix(value, prefix) {
		return value, nil
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", fmt.Errorf("decode secret: %w", err)
	}
	if len(payload) < b.aead.NonceSize() {
		return "", fmt.Errorf("secret is truncated")
	}
	nonce, ciphertext := payload[:b.aead.NonceSize()], payload[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plain), nil
}
