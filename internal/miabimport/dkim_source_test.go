package miabimport

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/miadbinventory"
)

func pureDKIMCoreSource(t *testing.T) *miadbinventory.ReviewedSource {
	t.Helper()
	dir := t.TempDir()
	input := miadbinventory.Options{ConfigPath: filepath.Join(dir, "source.conf"), SQLitePath: filepath.Join(dir, "users.sqlite"), MappingPath: filepath.Join(dir, "mapping.json")}
	if err := os.WriteFile(input.ConfigPath, []byte("STORAGE_ROOT=/home/user-data\nPRIMARY_HOSTNAME=box.legacy.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := exec.CommandContext(t.Context(), "/usr/bin/python3", "-I", "-B", "-c", syntheticSource, input.SQLitePath).Run(); err != nil {
		t.Fatal("create synthetic source")
	}
	names := make([]map[string]string, 0, 7)
	for _, domain := range []string{"d0.legacy.test", "d1.legacy.test", "d2.legacy.test", "d3.legacy.test", "d4.legacy.test", "d5.legacy.test", "box.legacy.test"} {
		names = append(names, map[string]string{"domain": domain, "tenant_ref": "legacy-primary"})
	}
	mapping := map[string]any{"version": 1, "domains": names}
	save := func() {
		t.Helper()
		raw, _ := json.Marshal(mapping)
		if err := os.WriteFile(input.MappingPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	report, err := miadbinventory.Inspect(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	mapping["version"] = 2
	mapping["reviewed_snapshot_sha256"] = report.SnapshotSHA256
	mapping["legacy_privileges"] = "preserve_metadata_no_authority"
	mapping["allow_same_tenant_cross_domain_aliases"] = true
	mapping["allow_equivalent_alias_dedup"] = true
	save()
	source, err := miadbinventory.LoadReviewedSource(t.Context(), miadbinventory.ReviewedSourceOptions{Options: input, ExpectedSnapshotSHA256: report.SnapshotSHA256})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func dkFixture(t *testing.T, source *miadbinventory.ReviewedSource, bits int) (DKIMSourceOptions, []byte) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve a legitimate CRLF encoding rather than silently re-encoding PKCS1.
	raw := []byte(strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), "\n", "\r\n"))
	var table, sign strings.Builder
	for _, domain := range source.Domains() {
		fmt.Fprintf(&table, "%s %s:mail:/home/user-data/mail/dkim/mail.private\n", domain.FQDN, domain.FQDN)
		fmt.Fprintf(&sign, "*@%s %s\n", domain.FQDN, domain.FQDN)
	}
	paths := map[string][]byte{legacyDKIMKey: raw, legacyDKIMKeyTable: []byte(table.String()), legacyDKIMSigningTable: []byte(sign.String())}
	for name, data := range paths {
		path := filepath.Join(root, name)
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	hash := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	return DKIMSourceOptions{CloneRoot: root, ExpectedPrivateSHA256: hash(raw), ExpectedKeyTableSHA256: hash(paths[legacyDKIMKeyTable]), ExpectedSigningTableSHA256: hash(paths[legacyDKIMSigningTable]), ExpectedSnapshotSHA256: source.Fingerprint()}, raw
}

func TestReviewedDKIMPreservesExactSourceBytesAndRedacts(t *testing.T) {
	source := pureDKIMCoreSource(t)
	options, raw := dkFixture(t, source, 2048)
	reviewed, err := LoadReviewedDKIM(t.Context(), options, source)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.privatePEM != string(raw) || reviewed.DomainCount() != 7 || len(reviewed.PublicFingerprint()) != 64 || len(reviewed.PrivateProofSHA256()) != 64 {
		t.Fatal("source key/routing proof changed")
	}
	encoded, err := json.Marshal(reviewed)
	if err != nil {
		t.Fatal(err)
	}
	for _, debug := range []string{fmt.Sprintf("%v", reviewed), fmt.Sprintf("%+v", *reviewed), fmt.Sprintf("%#v", reviewed), string(encoded)} {
		if strings.Contains(debug, "PRIVATE KEY") || strings.Contains(debug, "legacy.test") || strings.Contains(debug, "mail.private") {
			t.Fatal("private DKIM leaked through diagnostics")
		}
	}
	parsed, err := parseLegacyRSA(raw)
	if err != nil {
		t.Fatal(err)
	}
	// A network-free RSA signing/verification proof of this exact preserved key.
	// Actual wire-format DKIM/milter acceptance remains a separate native gate.
	bodyHash := sha256.Sum256([]byte("From: owner@d0.legacy.test\r\nSubject: preserved-key fixture\r\n\r\nLocal only.\r\n"))
	signature, err := rsa.SignPKCS1v15(rand.Reader, parsed, crypto.SHA256, bodyHash[:])
	if err != nil {
		t.Fatal("source signing failed")
	}
	if rsa.VerifyPKCS1v15(&parsed.PublicKey, crypto.SHA256, bodyHash[:], signature) != nil {
		t.Fatal("source signature invalid")
	}
	changed := sha256.Sum256([]byte("tampered fixture"))
	if rsa.VerifyPKCS1v15(&parsed.PublicKey, crypto.SHA256, changed[:], signature) == nil {
		t.Fatal("tampered signature accepted")
	}
	if reviewed.ValidateUnchanged(t.Context()) != nil {
		t.Fatal("unchanged source refused")
	}
}

func TestReviewedDKIMRejectsDriftAndUnreviewedRouting(t *testing.T) {
	source := pureDKIMCoreSource(t)
	for _, problem := range []string{"key-drift", "table-drift", "signing-drift", "missing-domain", "extra-domain", "wrong-key-path", "selector-injection", "signing-cross-domain", "key-symlink", "ancestor-symlink", "key-hardlink", "insecure-root", "1024-bit", "oversized-key", "oversized-table", "key-directory", "public-key", "trailing-pem", "prefixed-junk", "wrong-review", "wrong-hash"} {
		t.Run(problem, func(t *testing.T) {
			bits := 2048
			if problem == "1024-bit" {
				bits = 1024
			}
			options, raw := dkFixture(t, source, bits)
			keyPath := filepath.Join(options.CloneRoot, legacyDKIMKey)
			tablePath := filepath.Join(options.CloneRoot, legacyDKIMKeyTable)
			signPath := filepath.Join(options.CloneRoot, legacyDKIMSigningTable)
			rebind := func(path string, target *string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(data)
				*target = hex.EncodeToString(digest[:])
			}
			rewrite := func(path, old, replacement string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, []byte(strings.Replace(string(data), old, replacement, 1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch problem {
			case "key-drift":
				if err := os.WriteFile(keyPath, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "table-drift":
				rewrite(tablePath, ":mail:", ":rotated:")
			case "signing-drift":
				rewrite(signPath, "\n", "\n# drift\n")
			case "missing-domain":
				data, _ := os.ReadFile(tablePath)
				lines := strings.Split(string(data), "\n")
				if err := os.WriteFile(tablePath, []byte(strings.Join(lines[1:], "\n")), 0600); err != nil {
					t.Fatal(err)
				}
				rebind(tablePath, &options.ExpectedKeyTableSHA256)
			case "extra-domain":
				data, _ := os.ReadFile(tablePath)
				data = append(data, []byte("outside.test outside.test:mail:/home/user-data/mail/dkim/mail.private\n")...)
				if err := os.WriteFile(tablePath, data, 0600); err != nil {
					t.Fatal(err)
				}
				rebind(tablePath, &options.ExpectedKeyTableSHA256)
			case "wrong-key-path":
				rewrite(tablePath, "/home/user-data/mail/dkim/mail.private", "/etc/other-private")
				rebind(tablePath, &options.ExpectedKeyTableSHA256)
			case "selector-injection":
				rewrite(tablePath, ":mail:", ":mail;evil:")
				rebind(tablePath, &options.ExpectedKeyTableSHA256)
			case "signing-cross-domain":
				rewrite(signPath, " d0.legacy.test\n", " d1.legacy.test\n")
				rebind(signPath, &options.ExpectedSigningTableSHA256)
			case "key-symlink":
				target := keyPath + ".target"
				if err := os.Rename(keyPath, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("mail.private.target", keyPath); err != nil {
					t.Fatal(err)
				}
			case "ancestor-symlink":
				dir := filepath.Dir(keyPath)
				if err := os.Rename(dir, dir+"-target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("dkim-target", dir); err != nil {
					t.Fatal(err)
				}
			case "key-hardlink":
				if err := os.Link(keyPath, keyPath+".linked"); err != nil {
					t.Fatal(err)
				}
			case "insecure-root":
				if err := os.Chmod(options.CloneRoot, 0755); err != nil {
					t.Fatal(err)
				}
			case "oversized-key":
				if err := os.WriteFile(keyPath, make([]byte, 32769), 0600); err != nil {
					t.Fatal(err)
				}
				rebind(keyPath, &options.ExpectedPrivateSHA256)
			case "oversized-table":
				if err := os.WriteFile(tablePath, []byte("#"+strings.Repeat("x", 65536)), 0600); err != nil {
					t.Fatal(err)
				}
				rebind(tablePath, &options.ExpectedKeyTableSHA256)
			case "key-directory":
				if err := os.Remove(keyPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(keyPath, 0700); err != nil {
					t.Fatal(err)
				}
			case "public-key":
				parsed, _ := parseLegacyRSA(raw)
				data, _ := x509.MarshalPKIXPublicKey(&parsed.PublicKey)
				if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: data}), 0600); err != nil {
					t.Fatal(err)
				}
				rebind(keyPath, &options.ExpectedPrivateSHA256)
			case "trailing-pem":
				if err := os.WriteFile(keyPath, append(raw, raw...), 0600); err != nil {
					t.Fatal(err)
				}
				rebind(keyPath, &options.ExpectedPrivateSHA256)
			case "prefixed-junk":
				if err := os.WriteFile(keyPath, append([]byte("unexpected prefix\n"), raw...), 0600); err != nil {
					t.Fatal(err)
				}
				rebind(keyPath, &options.ExpectedPrivateSHA256)
			case "wrong-review":
				options.ExpectedSnapshotSHA256 = strings.Repeat("a", 64)
			case "wrong-hash":
				options.ExpectedPrivateSHA256 = strings.Repeat("0", 64)
			}
			if _, err := LoadReviewedDKIM(t.Context(), options, source); err == nil {
				t.Fatal("unsupported or changed source accepted")
			}
		})
	}
	options, _ := dkFixture(t, source, 2048)
	reviewed, err := LoadReviewedDKIM(t.Context(), options, source)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(options.CloneRoot, legacyDKIMKey)
	file, err := os.OpenFile(keyPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.ValidateUnchanged(t.Context()) == nil {
		t.Fatal("changed private source remained valid")
	}
}

func TestReviewedDKIMPKCS8KeepsEncodingAndPublicIdentity(t *testing.T) {
	source := pureDKIMCoreSource(t)
	options, raw := dkFixture(t, source, 2048)
	original, err := LoadReviewedDKIM(t.Context(), options, source)
	if err != nil {
		t.Fatal(err)
	}
	key, err := parseLegacyRSA(raw)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err = os.WriteFile(filepath.Join(options.CloneRoot, legacyDKIMKey), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	options.ExpectedPrivateSHA256 = hex.EncodeToString(sum[:])
	converted, err := LoadReviewedDKIM(t.Context(), options, source)
	if err != nil {
		t.Fatal(err)
	}
	if converted.privatePEM != string(encoded) || converted.PublicFingerprint() != original.PublicFingerprint() || converted.PrivateProofSHA256() == original.PrivateProofSHA256() {
		t.Fatal("source encoding and public identity conflated")
	}
	if original.ValidateUnchanged(t.Context()) == nil {
		t.Fatal("old encoding proof remained valid")
	}
}
