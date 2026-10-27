package backupauto

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
)

// EncryptionAlgorithmAge is the only supported archive cipher: age with an
// X25519 recipient. The payload is ChaCha20-Poly1305, so ciphertext integrity
// is authenticated end to end; a truncated or tampered object fails Decrypted.
const EncryptionAlgorithmAge = "age"

// ParseRecipient parses an age X25519 recipient public key.
func ParseRecipient(publicKey string) (age.Recipient, error) {
	r, err := age.ParseX25519Recipient(publicKey)
	if err != nil {
		return nil, fmt.Errorf("parse age recipient: %w", err)
	}
	return r, nil
}

// EncryptTo encrypts plaintext from in into out for the age recipient.
func EncryptTo(out io.Writer, in io.Reader, recipient age.Recipient) error {
	w, err := age.Encrypt(out, recipient)
	if err != nil {
		return fmt.Errorf("open age writer: %w", err)
	}
	if _, err := io.Copy(w, in); err != nil {
		_ = w.Close()
		return fmt.Errorf("encrypt: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finish age stream: %w", err)
	}
	return nil
}

// DecryptFrom decrypts an age stream with the supplied identities.
func DecryptFrom(out io.Writer, in io.Reader, identities ...age.Identity) error {
	if len(identities) == 0 {
		return fmt.Errorf("decrypt requires at least one age identity")
	}
	r, err := age.Decrypt(in, identities...)
	if err != nil {
		return fmt.Errorf("open age reader: %w", err)
	}
	if _, err := io.Copy(out, r); err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	return nil
}

// LoadIdentities reads age identities from a 0600 private key file.
func LoadIdentities(path string) ([]age.Identity, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-selected private key file.
	if err != nil {
		return nil, fmt.Errorf("read age identity: %w", err)
	}
	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse age identity: %w", err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("age identity file is empty")
	}
	return ids, nil
}
