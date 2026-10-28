package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

type completeScopeRunner struct{ dump, config, mail string }

func (r completeScopeRunner) RunBackup(ctx context.Context, stamp string) ([]backupauto.LocalArtifact, string, bool, error) {
	entries, scope, consistent, err := (journeyRunner{dumpPath: r.dump}).RunBackup(ctx, stamp)
	if err != nil {
		return nil, "", false, err
	}
	for _, item := range []struct{ file, kind, name string }{
		{r.config, backupauto.KindConfigTar, "urmail-config-" + stamp + ".tar.gz"},
		{r.mail, backupauto.KindMailTar, "urmail-mail-" + stamp + ".tar.gz"},
	} {
		hash, size, e := plainFileHash(item.file)
		if e != nil {
			return nil, "", false, e
		}
		entries = append(entries, backupauto.LocalArtifact{Path: item.file, Name: item.name, Kind: item.kind, Size: size, SHA256: hash})
	}
	return entries, scope, consistent, nil
}

// This verifies a complete generation with all three actual age
// envelopes and byte-faithful Maildir/config/key extraction. Transport is a
// directory; application of files to a booted machine is a separate drill.
func TestEncryptedCompleteScopeDatabaseMailConfigAndKeys(t *testing.T) {
	journeyRequireBinaries(t)
	ops009RequireBinaries(t)
	source := testdb.New(t)
	journeyMigrate(t, source)
	journeySeed(t, source)
	spool := t.TempDir()
	dump := filepath.Join(spool, "full.dump")
	if out, err := exec.CommandContext(t.Context(), journeyPGBin.dump, "--format=custom", "--file", dump, "--dbname", source).CombinedOutput(); err != nil {
		t.Fatalf("dump: %v %s", err, out)
	}
	configTar := filepath.Join(spool, "urmail-config-full.tar.gz")
	mailTar := filepath.Join(spool, "urmail-mail-full.tar.gz")
	configFacts := map[string]string{
		"etc/urmail/urmail.env":                                    "URMAIL_SESSION_ENCRYPTION_KEY=original-session-key\nURMAIL_NODE_FQDN=mail.restore.test\n",
		"etc/opendkim/keys/restore.test/mail.private":              "private-DKIM-key-preserved\n",
		"var/lib/urmail-agent/certs/mail.restore.test/privkey.pem": "private-TLS-key-preserved\n",
	}
	mailFacts := map[string]string{
		"var/mail/mail/mailboxes/journey.example.test/alice/new/one":   "From: old@example.test\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\n\r\nbody-original\r\n--b\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\nAQIDBA==\r\n--b--\r\n",
		"var/mail/mail/mailboxes/journey.example.test/bob/cur/two:2,S": "From: bob@example.test\r\n\r\nprivate-second-mailbox\r\n",
	}
	buildTarGz(t, configTar, configFacts)
	buildTarGz(t, mailTar, mailFacts)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(spool, "private-identity.agekey")
	if err = os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	offsite := t.TempDir()
	pool, err := db.OpenPlatform(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	worker := backupauto.Worker{Pool: pool, Runner: completeScopeRunner{dump, configTar, mailTar}, Store: dirOffsite{offsite}, Recipient: identity.Recipient().String(), OffsiteURI: "dir://" + offsite}
	if err = worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	objects, err := os.ReadDir(offsite)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := ""
	for _, e := range objects {
		if filepath.Ext(e.Name()) == ".json" {
			manifestPath = filepath.Join(offsite, e.Name())
		}
	}
	manifest, err := backupauto.LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Artifacts) != 3 {
		t.Fatalf("complete manifest omits artifacts: %d", len(manifest.Artifacts))
	}
	output := t.TempDir()
	var decrypted []string
	var decryptedDump string
	root := t.TempDir()
	for _, artifact := range manifest.Artifacts {
		destination := filepath.Join(output, artifact.Name)
		if err = decryptBackupArtifact(manifestPath, artifact.Name, destination, identityPath); err != nil {
			t.Fatal(err)
		}
		decrypted = append(decrypted, destination)
		if artifact.Kind == backupauto.KindPgDump {
			decryptedDump = destination
			continue
		}
		if err = validateRestoreArchive(destination); err != nil {
			t.Fatal(err)
		}
		if out, e := exec.CommandContext(t.Context(), "tar", "-xzf", destination, "-C", root).CombinedOutput(); e != nil {
			t.Fatalf("extract: %v %s", e, out)
		}
	}
	// verifyBackupManifest is the production all-artifact scope gate.
	copiedManifest := filepath.Join(output, "manifest.json")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(copiedManifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = verifyBackupManifest(copiedManifest, decrypted, true); err != nil {
		t.Fatal(err)
	}
	for _, facts := range []map[string]string{configFacts, mailFacts} {
		for name, want := range facts {
			actual, e := os.ReadFile(filepath.Join(root, name))
			if e != nil || string(actual) != want {
				t.Fatalf("byte-faithful file %s: %v", name, e)
			}
		}
	}
	target := testdb.New(t)
	journeyMigrate(t, target)
	journeyBeginQuarantine(t, target)
	ops009RestorePipeline(t, target, decryptedDump, "complete-scope-recovery")
	journeyAssertFacts(t, target)
	if !ops009Release(t, target) {
		t.Fatal("complete scope target not releasable")
	}
}
