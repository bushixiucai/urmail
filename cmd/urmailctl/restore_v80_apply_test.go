package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// v80VerifiedBundleForApply builds the standard isolated legacy fixture, turns
// it into a verified private bundle and returns the artifact paths plus the
// untouched source tree.
func v80VerifiedBundleForApply(t *testing.T) (manifestPath, archivePath, source string) {
	t.Helper()
	source = v80Fixture(t)
	inventory, err := collectV80Inventory(source)
	if err != nil || len(inventory.Blockers) != 0 {
		t.Fatalf("invalid synthetic profile: blockers=%v err=%v", inventory.Blockers, err)
	}
	manifestPath, err = createV80Bundle(context.Background(), source, v80ApplyTestDirectory(t), inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored v80Manifest
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	archivePath = filepath.Join(filepath.Dir(manifestPath), stored.Artifact.Name)
	if _, err = verifyV80Bundle(context.Background(), manifestPath, archivePath); err != nil {
		t.Fatal(err)
	}
	return manifestPath, archivePath, source
}

// v80TreeDigest fingerprints a whole tree (names, modes, link text, content)
// so tests can prove that a run left it untouched.
func v80TreeDigest(t *testing.T, rootPath string) string {
	t.Helper()
	digest := sha256.New()
	err := filepath.WalkDir(rootPath, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		_, _ = digest.Write([]byte(name))
		_, _ = digest.Write([]byte(info.Mode().String()))
		if info.Mode().IsRegular() {
			raw, readErr := os.ReadFile(name)
			if readErr != nil {
				return readErr
			}
			_, _ = digest.Write(raw)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(name)
			if linkErr != nil {
				return linkErr
			}
			_, _ = digest.Write([]byte(target))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func TestV80ApplyRestoresVerifiedFilesIntoNewPrivateStage(t *testing.T) {
	// The standard fixture, customized with mode, nanosecond mtime and (as
	// root) numeric ownership variation on one persistent device-state file.
	source := v80Fixture(t)
	statePath := filepath.Join(source, "var/lib/z-push/private-device-state")
	if err := os.Chmod(statePath, 0o640); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2021, 6, 7, 8, 9, 10, 123456789, time.UTC)
	if err := os.Chtimes(statePath, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(statePath, 12345, 23456); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := collectV80Inventory(source)
	if err != nil || len(inventory.Blockers) != 0 {
		t.Fatalf("invalid synthetic profile: blockers=%v err=%v", inventory.Blockers, err)
	}
	manifestPath, err := createV80Bundle(context.Background(), source, v80ApplyTestDirectory(t), inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest v80Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(filepath.Dir(manifestPath), manifest.Artifact.Name)

	sourceDigest := v80TreeDigest(t, source)
	stage := filepath.Join(v80ApplyTestDirectory(t), "stage-new")
	receipt, err := applyV80RestoreFiles(context.Background(), manifestPath, archivePath, stage)
	if err != nil {
		t.Fatal(err)
	}
	if v80TreeDigest(t, source) != sourceDigest {
		t.Fatal("apply modified the source tree")
	}
	if !receipt.FilesRestoredOnly || receipt.ProductionActivation || receipt.CutoverReady {
		t.Fatal("receipt must report files-restored-only without any activation claim")
	}
	for _, flag := range []bool{receipt.ServicesStarted, receipt.PHPExecuted, receipt.CredentialsActivated, receipt.DatabaseConnected, receipt.HoldsReleased, receipt.QueuesReplayed} {
		if flag {
			t.Fatal("receipt claimed a production side effect")
		}
	}
	if len(receipt.RequiredFollowUp) != len(v80FollowUp) {
		t.Fatal("receipt lost the required follow-up gates")
	}
	joined := strings.Join(receipt.RequiredFollowUp, ",")
	for _, gate := range []string{"source_dsn_and_secret_rebind", "roundcube_session_invalidation", "device_state_and_external_replay_reconciliation"} {
		if !strings.Contains(joined, gate) {
			t.Fatal("pending PostgreSQL rebind, session and consumer gates are not explicit")
		}
	}
	if strings.Contains(joined, "nextcloud") {
		t.Fatal("retired Nextcloud runtime gates must not remain required follow-up")
	}
	if stageInfo, statErr := os.Lstat(stage); statErr != nil || !stageInfo.IsDir() || stageInfo.Mode().Perm() != 0o700 || receipt.StageMode != 0o700 {
		t.Fatal("staging root is not a private 0700 directory")
	}
	if receipt.ArchiveSHA256 == "" || receipt.ManifestSHA256 == "" || receipt.LayoutSHA256 != manifest.LayoutSHA256 {
		t.Fatal("receipt did not bind the verified artifact identities")
	}
	if archiveDigest, digestErr := fileSHA256(archivePath); digestErr != nil || archiveDigest != receipt.ArchiveSHA256 {
		t.Fatal("receipt archive digest does not match the artifact")
	}
	if _, statErr := os.Lstat(filepath.Join(stage, v80StageMarkerName)); !os.IsNotExist(statErr) {
		t.Fatal("successful apply must not leave an incomplete marker")
	}
	// Preserved data, metadata, numeric ownership and nanosecond mtime.
	stateInfo, statErr := os.Lstat(filepath.Join(stage, "var/lib/z-push/private-device-state"))
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !stateInfo.Mode().IsRegular() || stateInfo.Mode().Perm() != 0o640 || !stateInfo.ModTime().Equal(stamp) {
		t.Fatal("restored file mode or nanosecond mtime was lost")
	}
	if stat, ok := stateInfo.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != manifestMemberUID(manifest, "var/lib/z-push/private-device-state") || int(stat.Gid) != manifestMemberGID(manifest, "var/lib/z-push/private-device-state") {
		t.Fatal("restored file numeric ownership does not match the verified manifest")
	}
	message, err := os.ReadFile(filepath.Join(stage, "home/user-data/mail/mailboxes/source.test/private-user/cur/message"))
	if err != nil || !strings.Contains(string(message), "secret-never-public") {
		t.Fatal("mailbox bytes were not restored verbatim into the private stage")
	}
	// Symlinks are preserved as text, never materialized or followed.
	for name, target := range map[string]string{
		"usr/local/lib/owncloud/config/config.php":            "/home/user-data/owncloud/config.php",
		"etc/dovecot/private/dovecot.key":                     "/etc/ssl/private/ssl-cert-snakeoil.key",
		"home/user-data/mail/sieve/source.test/account.sieve": "account/active.sieve",
	} {
		linkInfo, linkErr := os.Lstat(filepath.Join(stage, name))
		if linkErr != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("restored %s is not a preserved symlink", name)
		}
		if got, readErr := os.Readlink(filepath.Join(stage, name)); readErr != nil || got != target {
			t.Fatalf("symlink text for %s was rewritten", name)
		}
	}
	// Excluded nonpersistent runtime links are not restored at all.
	if _, statErr = os.Lstat(filepath.Join(stage, "usr/local/lib/roundcubemail/temp")); !os.IsNotExist(statErr) {
		t.Fatal("excluded runtime link was materialized in the stage")
	}
	// The receipt copy inside the stage matches the returned receipt.
	storedReceipt, err := os.ReadFile(filepath.Join(stage, v80StageReceiptName))
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Lstat(filepath.Join(stage, v80StageReceiptName)); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("stage receipt is not private")
	}
	var onDisk v80RestoreReceipt
	if err = json.Unmarshal(storedReceipt, &onDisk); err != nil || onDisk.ProductionActivation || !onDisk.FilesRestoredOnly {
		t.Fatal("on-disk receipt is missing or overstates readiness")
	}
	if onDisk.CompletedAt.IsZero() || onDisk.MemberCount != receipt.MemberCount {
		t.Fatal("on-disk receipt does not match the returned receipt")
	}
}

func manifestMemberUID(manifest v80Manifest, name string) int {
	for _, member := range manifest.Members {
		if member.Name == name {
			return member.UID
		}
	}
	return -1
}

func manifestMemberGID(manifest v80Manifest, name string) int {
	for _, member := range manifest.Members {
		if member.Name == name {
			return member.GID
		}
	}
	return -1
}

func TestV80ApplyRejectsAdversarialDestinations(t *testing.T) {
	manifestPath, archivePath, _ := v80VerifiedBundleForApply(t)
	ctx := context.Background()
	for _, destination := range []string{"/", "/etc", "/home", "/etc/opendkim/stage", "/etc/mailinabox.conf", "/home/user-data"} {
		if _, err := applyV80RestoreFiles(ctx, manifestPath, archivePath, destination); err == nil {
			t.Fatalf("production destination %s was accepted", destination)
		}
	}
	t.Run("destination-is-symlink", func(t *testing.T) {
		tmp := v80ApplyTestDirectory(t)
		real := filepath.Join(tmp, "real")
		if err := os.MkdirAll(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(tmp, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := applyV80RestoreFiles(ctx, manifestPath, archivePath, link); err == nil {
			t.Fatal("symlinked destination was accepted")
		}
		entries, readErr := os.ReadDir(real)
		if readErr != nil || len(entries) != 0 {
			t.Fatal("rejected symlink destination received staged files through its target")
		}
	})
	t.Run("parent-is-symlink", func(t *testing.T) {
		tmp := v80ApplyTestDirectory(t)
		real := filepath.Join(tmp, "real")
		if err := os.MkdirAll(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(tmp, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		stage := filepath.Join(link, "stage")
		if _, err := applyV80RestoreFiles(ctx, manifestPath, archivePath, stage); err == nil {
			t.Fatal("symlinked parent ancestry was accepted")
		}
		entries, readErr := os.ReadDir(real)
		if readErr != nil || len(entries) != 0 {
			t.Fatal("staged files escaped through a symlinked parent")
		}
	})
	t.Run("destination-not-empty", func(t *testing.T) {
		tmp := v80ApplyTestDirectory(t)
		stage := filepath.Join(tmp, "stage")
		if err := os.MkdirAll(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "leftover"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := applyV80RestoreFiles(ctx, manifestPath, archivePath, stage); err == nil {
			t.Fatal("non-empty destination was accepted")
		}
	})
	t.Run("incomplete-marker-refuses-reuse", func(t *testing.T) {
		tmp := v80ApplyTestDirectory(t)
		stage := filepath.Join(tmp, "stage")
		if err := os.MkdirAll(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, v80StageMarkerName), []byte(`{"reason":"prior"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := applyV80RestoreFiles(ctx, manifestPath, archivePath, stage); err == nil {
			t.Fatal("a marked incomplete stage was reused")
		}
	})
	t.Run("completed-receipt-refuses-reuse", func(t *testing.T) {
		tmp := v80ApplyTestDirectory(t)
		stage := filepath.Join(tmp, "stage")
		if err := os.MkdirAll(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, v80StageReceiptName), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := applyV80RestoreFiles(ctx, manifestPath, archivePath, stage); err == nil {
			t.Fatal("a completed stage was overwritten")
		}
	})
}

func TestV80ApplyMutatedArchiveFailsBeforeStageCreationAndKeepsSourceUntouched(t *testing.T) {
	for _, scenario := range []string{"path-traversal", "symlink-escape", "owner-metadata", "changed-content"} {
		t.Run(scenario, func(t *testing.T) {
			source := v80Fixture(t)
			inventory, err := collectV80Inventory(source)
			if err != nil || len(inventory.Blockers) != 0 {
				t.Fatalf("invalid synthetic profile: blockers=%v err=%v", inventory.Blockers, err)
			}
			manifestPath, err := createV80Bundle(context.Background(), source, v80ApplyTestDirectory(t), inventory, false)
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			archivePath := mutateV80Archive(t, manifestPath, func(header *tar.Header, body []byte) (*tar.Header, []byte) {
				if changed {
					return header, body
				}
				switch scenario {
				case "path-traversal":
					header.Name = "../../etc/passwd"
					changed = true
				case "symlink-escape":
					if header.Typeflag == tar.TypeSymlink {
						header.Linkname = "/etc/passwd"
						changed = true
					}
				case "owner-metadata":
					header.Uid++
					changed = true
				case "changed-content":
					if len(body) > 0 {
						body[0] ^= 1
						changed = true
					}
				}
				return header, body
			})
			if !changed {
				t.Fatal("attack fixture did not change a member")
			}
			sourceDigest := v80TreeDigest(t, source)
			stage := filepath.Join(v80ApplyTestDirectory(t), "stage")
			if _, err = applyV80RestoreFiles(context.Background(), manifestPath, archivePath, stage); err == nil {
				t.Fatal("mutated archive passed the apply gate")
			}
			if _, statErr := os.Lstat(stage); !os.IsNotExist(statErr) {
				t.Fatal("rejected bundle still created a staging root")
			}
			if v80TreeDigest(t, source) != sourceDigest {
				t.Fatal("apply attempt modified the source tree")
			}
		})
	}
	t.Run("whole-artifact-corruption", func(t *testing.T) {
		source := v80Fixture(t)
		inventory, err := collectV80Inventory(source)
		if err != nil {
			t.Fatal(err)
		}
		manifestPath, err := createV80Bundle(context.Background(), source, v80ApplyTestDirectory(t), inventory, false)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var manifest v80Manifest
		if err = json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(filepath.Dir(manifestPath), manifest.Artifact.Name)
		bundle, err := os.ReadFile(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		bundle[len(bundle)-1] ^= 1
		if err = os.WriteFile(archivePath, bundle, 0o600); err != nil {
			t.Fatal(err)
		}
		sourceDigest := v80TreeDigest(t, source)
		stage := filepath.Join(v80ApplyTestDirectory(t), "stage")
		if _, err = applyV80RestoreFiles(context.Background(), manifestPath, archivePath, stage); err == nil {
			t.Fatal("corrupted bundle passed the apply gate")
		}
		if _, statErr := os.Lstat(stage); !os.IsNotExist(statErr) {
			t.Fatal("rejected bundle still created a staging root")
		}
		if v80TreeDigest(t, source) != sourceDigest {
			t.Fatal("apply attempt modified the source tree")
		}
	})
}

func TestV80ApplyRefusesReuseAfterCompletion(t *testing.T) {
	manifestPath, archivePath, _ := v80VerifiedBundleForApply(t)
	stage := filepath.Join(v80ApplyTestDirectory(t), "stage")
	first, err := applyV80RestoreFiles(context.Background(), manifestPath, archivePath, stage)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(stage, v80StageReceiptName)
	storedBefore, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = applyV80RestoreFiles(context.Background(), manifestPath, archivePath, stage); err == nil {
		t.Fatal("completed stage was reused")
	}
	storedAfter, err := os.ReadFile(receiptPath)
	if err != nil || !bytes.Equal(storedBefore, storedAfter) {
		t.Fatal("refused reuse modified the completed stage receipt")
	}
	if _, statErr := os.Lstat(filepath.Join(stage, v80StageMarkerName)); !os.IsNotExist(statErr) {
		t.Fatal("refused reuse marked a completed stage as incomplete")
	}
	if first.MemberCount == 0 {
		t.Fatal("receipt lost its member count")
	}
}

func TestV80RestoreApplyEntryRequiresRootOperatorAndExplicitArguments(t *testing.T) {
	if os.Geteuid() != 0 {
		var output bytes.Buffer
		args := []string{"--profile", v80Profile, "--manifest", "m", "--archive", "a", "--stage", "s"}
		if err := runV80RestoreApply(context.Background(), args, &output); err == nil || output.Len() != 0 {
			t.Fatal("non-root operator must be refused")
		}
		t.Skip("root entry exercised in the isolated Linux root rehearsal")
	}
	manifestPath, archivePath, _ := v80VerifiedBundleForApply(t)
	stage := filepath.Join(v80ApplyTestDirectory(t), "stage")
	for _, args := range [][]string{
		{"--profile", "unknown", "--manifest", manifestPath, "--archive", archivePath, "--stage", stage},
		{"--profile", v80Profile, "--manifest", manifestPath, "--archive", archivePath},
		{"--profile", v80Profile, "--archive", archivePath, "--stage", stage},
	} {
		var output bytes.Buffer
		if err := runV80RestoreApply(context.Background(), args, &output); err == nil {
			t.Fatal("invalid apply arguments were accepted")
		}
	}
	var output bytes.Buffer
	args := []string{"--profile", v80Profile, "--manifest", manifestPath, "--archive", archivePath, "--stage", stage}
	if err := runV80RestoreApply(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	var receipt v80RestoreReceipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.FilesRestoredOnly || receipt.ProductionActivation || receipt.CutoverReady {
		t.Fatal("apply entry overstates restoration readiness")
	}
	if _, statErr := os.Lstat(filepath.Join(stage, "etc/mailinabox.conf")); statErr != nil {
		t.Fatal("apply entry did not restore the verified files")
	}
}

func v80ApplyTestDirectory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
