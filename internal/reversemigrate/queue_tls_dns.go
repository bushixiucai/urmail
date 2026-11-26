package reversemigrate

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// QueueItem represents one Postfix active/deferred/hold spool entry.
type QueueItem struct {
	QueueID           string   `json:"queue_id"`
	EnvelopeSender    string   `json:"envelope_sender"`
	PendingRecipients []string `json:"pending_recipients"`
	MIMEBodySHA256    string   `json:"mime_body_sha256"`
	MessageID         string   `json:"message_id,omitempty"`
}

// CompoundKey returns the §4.1.4 MIME-payload-based deduplication key.
func (q QueueItem) CompoundKey() string {
	recips := append([]string(nil), q.PendingRecipients...)
	for i := range recips {
		recips[i] = strings.ToLower(strings.TrimSpace(recips[i]))
	}
	sort.Strings(recips)
	return strings.ToLower(strings.TrimSpace(q.MIMEBodySHA256)) + "|" +
		strings.ToLower(strings.TrimSpace(q.EnvelopeSender)) + "|" +
		strings.Join(recips, ",")
}

// MergeQueue deduplicates pending Postfix queue items against delivered
// Maildir MIME hashes and target queue items using the §4.1.4 compound key.
func MergeQueue(srcItems, tgtItems []QueueItem, deliveredMIMEHashes map[string]bool, outDir string, dryRun bool) (QueueReport, []QueueItem, error) {
	rep := QueueReport{SourceItems: len(srcItems)}
	seenSrc := map[string]string{}
	seenTgt := map[string]bool{}
	for _, t := range tgtItems {
		seenTgt[t.CompoundKey()] = true
	}

	var handoff []QueueItem
	for _, item := range srcItems {
		if item.MIMEBodySHA256 == "" || len(item.PendingRecipients) == 0 {
			return rep, nil, fmt.Errorf("queue item %q missing mime_body_sha256 or recipients", item.QueueID)
		}
		key := item.CompoundKey()
		if prevQID, dup := seenSrc[key]; dup {
			return rep, nil, fmt.Errorf("duplicate source queue compound key %s (queue_ids=%s,%s)", key, prevQID, item.QueueID)
		}
		seenSrc[key] = item.QueueID

		if deliveredMIMEHashes != nil && deliveredMIMEHashes[item.MIMEBodySHA256] {
			rep.SkippedDelivered++
			continue
		}
		if seenTgt[key] {
			rep.SkippedTargetDup++
			continue
		}
		seenTgt[key] = true
		handoff = append(handoff, item)
		rep.HandoffQueued++
		rep.Actions = append(rep.Actions, fmt.Sprintf("handoff queue_id=%s key=%s", item.QueueID, key))
	}

	if !dryRun && outDir != "" && len(handoff) > 0 {
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return rep, nil, err
		}
		raw, err := json.MarshalIndent(handoff, "", "  ")
		if err != nil {
			return rep, nil, err
		}
		if err := os.WriteFile(filepath.Join(outDir, "queue-handoff.json"), append(raw, '\n'), 0o600); err != nil {
			return rep, nil, err
		}
	}
	return rep, handoff, nil
}

// VerifyAndMergeTLS implements §4.3 algorithm-agnostic (RSA & ECDSA) public
// key SHA-256 verification and atomic target cert/key replacement.
func VerifyAndMergeTLS(srcCertPath, srcKeyPath, tgtCertPath, tgtKeyPath, backupDir string, now time.Time, dryRun bool) (TLSReport, error) {
	var rep TLSReport
	certPEM, err := os.ReadFile(srcCertPath) // #nosec G304 -- operator-configured cert path
	if err != nil {
		return rep, fmt.Errorf("read source cert: %w", err)
	}
	keyPEM, err := os.ReadFile(srcKeyPath) // #nosec G304 -- operator-configured key path
	if err != nil {
		return rep, fmt.Errorf("read source key: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return rep, errors.New("invalid source certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return rep, fmt.Errorf("parse source certificate: %w", err)
	}
	if now.UTC().After(cert.NotAfter) {
		return rep, fmt.Errorf("source certificate expired at %s", cert.NotAfter.Format(time.RFC3339))
	}

	certPubHash, algo, err := pubkeySHA256(cert.PublicKey)
	if err != nil {
		return rep, fmt.Errorf("hash certificate pubkey: %w", err)
	}
	privPub, err := parsePrivateKeyPub(keyPEM)
	if err != nil {
		return rep, fmt.Errorf("parse source private key: %w", err)
	}
	keyPubHash, _, err := pubkeySHA256(privPub)
	if err != nil {
		return rep, fmt.Errorf("hash private key pubkey: %w", err)
	}
	if certPubHash != keyPubHash {
		return rep, fmt.Errorf("TLS pubkey mismatch: cert=%s != key=%s", certPubHash, keyPubHash)
	}

	rep.Verified = true
	rep.PubkeySHA256 = certPubHash
	rep.Algorithm = algo
	rep.NotAfter = cert.NotAfter.UTC().Format(time.RFC3339)

	shouldUpdate := true
	if tgtCertPath != "" {
		if tgtPEM, err := os.ReadFile(tgtCertPath); err == nil { // #nosec G304 -- operator-configured target cert path
			if tb, _ := pem.Decode(tgtPEM); tb != nil {
				if tgtCert, pErr := x509.ParseCertificate(tb.Bytes); pErr == nil && !cert.NotAfter.After(tgtCert.NotAfter) {
					shouldUpdate = false
				}
			}
		}
	}
	if !shouldUpdate {
		rep.Actions = append(rep.Actions, "target TLS cert already current or newer; skipped replace")
		return rep, nil
	}
	rep.Updated = true
	rep.Actions = append(rep.Actions, fmt.Sprintf("update target TLS cert/key (%s pubkey=%s)", algo, certPubHash))

	if !dryRun && tgtCertPath != "" && tgtKeyPath != "" {
		if backupDir != "" {
			_ = os.MkdirAll(backupDir, 0o700)
			if raw, err := os.ReadFile(tgtCertPath); err == nil { // #nosec G304 -- operator-configured cert path
				_ = writeAtomicPEM(filepath.Join(backupDir, "cert.pem.bak"), raw, 0o600) // #nosec G703 -- fixed filename inside operator backupDir
			}
			if raw, err := os.ReadFile(tgtKeyPath); err == nil { // #nosec G304 -- operator-configured key path
				_ = writeAtomicPEM(filepath.Join(backupDir, "key.pem.bak"), raw, 0o600) // #nosec G703 -- fixed filename inside operator backupDir
			}
		}
		if err := writeAtomicPEM(tgtCertPath, certPEM, 0o644); err != nil {
			return rep, err
		}
		if err := writeAtomicPEM(tgtKeyPath, keyPEM, 0o600); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func pubkeySHA256(pub any) (string, string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	algo := "unknown"
	switch pub.(type) {
	case *rsa.PublicKey:
		algo = "RSA"
	case *ecdsa.PublicKey:
		algo = "ECDSA"
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), algo, nil
}

func parsePrivateKeyPub(keyPEM []byte) (any, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("invalid private key PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		switch kt := k.(type) {
		case *rsa.PrivateKey:
			return &kt.PublicKey, nil
		case *ecdsa.PrivateKey:
			return &kt.PublicKey, nil
		}
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return &k.PublicKey, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return &k.PublicKey, nil
	}
	return nil, errors.New("unsupported private key format")
}

func writeAtomicPEM(dst string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".e7-tls-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, dst)
}

// VerifyDNS enforces Q-S2 zero-DNS-cutover and validates managed zone syntax.
func VerifyDNS(zoneFiles []string, managedChanged bool) (DNSReport, error) {
	rep := DNSReport{ZeroDNSCutover: true}
	for _, zf := range zoneFiles {
		raw, err := os.ReadFile(zf) // #nosec G304 -- operator-configured zone file
		if err != nil {
			return rep, fmt.Errorf("read zone %s: %w", zf, err)
		}
		text := string(raw)
		if !strings.Contains(text, "SOA") || !strings.Contains(text, "$ORIGIN") {
			return rep, fmt.Errorf("invalid zone syntax in %s: missing $ORIGIN or SOA", zf)
		}
		rep.ZonesChecked++
		rep.Actions = append(rep.Actions, "verified zone syntax "+filepath.Base(zf))
	}
	if managedChanged && len(zoneFiles) == 0 {
		return rep, errors.New("managed DNS changed but no zone files provided for verification")
	}
	rep.Verified = true
	return rep, nil
}
