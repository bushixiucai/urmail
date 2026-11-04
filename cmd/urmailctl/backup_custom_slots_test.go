package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
)

func TestCustomStorageRootAndMasterKeyBackupRestore(t *testing.T) {
	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "source")
	backupDir := filepath.Join(tempDir, "backup")
	targetDir := filepath.Join(tempDir, "new_host_root")

	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	customStorage := "/mnt/maildata"
	customKeyPath := "/opt/custom/master.key"

	mailMsgContent := "From: alice@example.com\r\nSubject: Custom storage test\r\n\r\nSecret mail body in custom storage\r\n"
	mailMsgRel := "mail/mailboxes/example.com/alice/cur/10001:2,S"
	mailArchivePath := strings.TrimPrefix(filepath.Join(customStorage, mailMsgRel), "/")

	masterKeyContent := "custom-master-key-32-byte-hex-data\n"
	masterKeyArchivePath := strings.TrimPrefix(customKeyPath, "/")

	configContent := "URMAIL_NODE_FQDN=mail.example.com\n"
	configArchivePath := "etc/urmail/urmail.env"

	stamp := time.Now().UTC().Format("20060102T150405Z")
	mailTarName := "urmail-mail-" + stamp + ".tar.gz"
	configTarName := "urmail-config-" + stamp + ".tar.gz"
	dumpName := "urmail-" + stamp + ".dump"

	mailTarPath := filepath.Join(backupDir, mailTarName)
	configTarPath := filepath.Join(backupDir, configTarName)
	dumpPath := filepath.Join(backupDir, dumpName)

	if err := os.WriteFile(dumpPath, []byte("PG_DUMP_CUSTOM_DATA"), 0o600); err != nil {
		t.Fatal(err)
	}

	buildTarGz(t, mailTarPath, map[string]string{
		mailArchivePath: mailMsgContent,
	})
	buildTarGz(t, configTarPath, map[string]string{
		masterKeyArchivePath: masterKeyContent,
		configArchivePath:    configContent,
	})

	mailSum, mailSize, err := plainFileHash(mailTarPath)
	if err != nil {
		t.Fatal(err)
	}
	configSum, configSize, err := plainFileHash(configTarPath)
	if err != nil {
		t.Fatal(err)
	}
	dumpSum, dumpSize, err := plainFileHash(dumpPath)
	if err != nil {
		t.Fatal(err)
	}

	manifest := &backupauto.Manifest{
		Version:       backupauto.ManifestVersion3,
		LayoutVersion: backupauto.LayoutVersion1,
		Scope:         backupauto.ScopeComplete,
		Consistent:    true,
		CreatedAt:     time.Now().UTC(),
		Artifacts: []backupauto.Artifact{
			{Name: dumpName, SHA256: dumpSum, Size: dumpSize, Mode: 0o600, Kind: backupauto.KindPgDump},
			{Name: configTarName, SHA256: configSum, Size: configSize, Mode: 0o600, Kind: backupauto.KindConfigTar},
			{Name: mailTarName, SHA256: mailSum, Size: mailSize, Mode: 0o600, Kind: backupauto.KindMailTar},
		},
		Slots: map[string]backupauto.Slot{
			backupauto.SlotMailStorage: {
				Artifact:    mailTarName,
				ArchivePath: strings.TrimPrefix(filepath.Join(customStorage, "mail"), "/"),
				TargetPath:  filepath.Join(customStorage, "mail"),
				Kind:        "dir",
			},
			backupauto.SlotConfigMasterKey: {
				Artifact:    configTarName,
				ArchivePath: masterKeyArchivePath,
				TargetPath:  customKeyPath,
				Kind:        "file",
			},
			backupauto.SlotConfigOperator: {
				Artifact:    configTarName,
				ArchivePath: "etc/urmail",
				TargetPath:  "/etc/urmail",
				Kind:        "dir",
			},
		},
	}

	manifestPath := filepath.Join(backupDir, backupauto.ManifestName(stamp))
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	newHostCfg := &config.Config{
		StorageRoot:   "/srv/mail",
		MasterKeyPath: "/etc/urmail/master.key",
	}

	if err := validateRestoreArchiveWithManifest(mailTarPath, mailTarName, manifest, newHostCfg, nil); err != nil {
		t.Fatalf("custom StorageRoot archive unexpectedly blocked by validator: %v", err)
	}
	if err := validateRestoreArchiveWithManifest(configTarPath, configTarName, manifest, newHostCfg, nil); err != nil {
		t.Fatalf("custom MasterKeyPath archive unexpectedly blocked by validator: %v", err)
	}

	if err := extractRestoreArchive(mailTarPath, mailTarName, manifest, newHostCfg, nil, targetDir); err != nil {
		t.Fatalf("extract mail archive failed: %v", err)
	}
	if err := extractRestoreArchive(configTarPath, configTarName, manifest, newHostCfg, nil, targetDir); err != nil {
		t.Fatalf("extract config archive failed: %v", err)
	}

	expectedMailPath := filepath.Join(targetDir, "srv/mail/mail/mailboxes/example.com/alice/cur/10001:2,S")
	restoredMailBytes, err := os.ReadFile(expectedMailPath)
	if err != nil {
		t.Fatalf("restored mail not found at new StorageRoot location %s: %v", expectedMailPath, err)
	}
	if string(restoredMailBytes) != mailMsgContent {
		t.Fatalf("restored mail content mismatch: got %q, want %q", string(restoredMailBytes), mailMsgContent)
	}
	mailHash := sha256.Sum256(restoredMailBytes)
	origHash := sha256.Sum256([]byte(mailMsgContent))
	if mailHash != origHash {
		t.Fatalf("restored mail SHA256 mismatch")
	}

	expectedKeyPath := filepath.Join(targetDir, "etc/urmail/master.key")
	restoredKeyBytes, err := os.ReadFile(expectedKeyPath)
	if err != nil {
		t.Fatalf("restored master key not found at %s: %v", expectedKeyPath, err)
	}
	if string(restoredKeyBytes) != masterKeyContent {
		t.Fatalf("restored master key content mismatch: got %q, want %q", string(restoredKeyBytes), masterKeyContent)
	}

	expectedEnvPath := filepath.Join(targetDir, "etc/urmail/urmail.env")
	restoredEnvBytes, err := os.ReadFile(expectedEnvPath)
	if err != nil {
		t.Fatalf("restored operator config not found at %s: %v", expectedEnvPath, err)
	}
	if string(restoredEnvBytes) != configContent {
		t.Fatalf("restored operator config mismatch: got %q, want %q", string(restoredEnvBytes), configContent)
	}
	_ = sourceDir
}

func TestV3ManifestOffsiteEncryptDecryptRoundTrip(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "offsite")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := newFileStore(t, storeDir)

	stamp := "20261103T120000Z"
	mailContent := "custom storage data"
	mailTarName := "urmail-mail-" + stamp + ".tar.gz"
	mailTarPath := filepath.Join(dir, mailTarName)
	buildTarGz(t, mailTarPath, map[string]string{
		"data/mail/message": mailContent,
	})

	mailSum, mailSize, err := plainFileHash(mailTarPath)
	if err != nil {
		t.Fatal(err)
	}

	entry, err := backupauto.EncryptAndUpload(context.Background(), store, backupauto.LocalArtifact{
		Name: mailTarName, Path: mailTarPath, Kind: backupauto.KindMailTar,
		Size: mailSize, SHA256: mailSum,
	}, identity.Recipient().String())
	if err != nil {
		t.Fatal(err)
	}

	v3Manifest := &backupauto.Manifest{
		Version:       backupauto.ManifestVersion3,
		LayoutVersion: backupauto.LayoutVersion1,
		Scope:         backupauto.ScopeComplete,
		Consistent:    true,
		CreatedAt:     time.Now().UTC(),
		Artifacts:     []backupauto.Artifact{entry},
		Encryption:    &backupauto.EncryptionInfo{Algorithm: backupauto.EncryptionAlgorithmAge, Recipient: identity.Recipient().String()},
		Slots: map[string]backupauto.Slot{
			backupauto.SlotMailStorage: {
				Artifact:    mailTarName,
				ArchivePath: "data/mail",
				TargetPath:  "/data/mail",
				Kind:        "dir",
			},
		},
	}

	manifestRaw, err := json.MarshalIndent(v3Manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(storeDir, backupauto.ManifestName(stamp))
	if err := os.WriteFile(manifestPath, manifestRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	identityPath := filepath.Join(dir, "identity.agekey")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	decryptedTarPath := filepath.Join(dir, "decrypted-"+mailTarName)
	if err := decryptBackupArtifact(manifestPath, mailTarName, decryptedTarPath, identityPath); err != nil {
		t.Fatalf("decrypt v3 backup artifact: %v", err)
	}

	decryptedSum, _, err := plainFileHash(decryptedTarPath)
	if err != nil {
		t.Fatal(err)
	}
	if decryptedSum != mailSum {
		t.Fatalf("decrypted artifact SHA256 mismatch")
	}

	loaded, err := backupauto.LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 3 || loaded.LayoutVersion != 1 {
		t.Fatalf("manifest version lost after offsite round trip: %d, layout: %d", loaded.Version, loaded.LayoutVersion)
	}
	if slot, ok := loaded.Slots[backupauto.SlotMailStorage]; !ok || slot.ArchivePath != "data/mail" {
		t.Fatalf("manifest slot lost after offsite round trip: %+v", slot)
	}
}

func TestRestoreSecurityRejectsEscapingLinksAndAbsolutePaths(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "root")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		memberPath string
		target     string
		isSymlink  bool
	}{
		{"relative_traversal", "var/mail/mail/../../../etc/shadow", "", false},
		{"internal_dotdot", "var/mail/mail/sub/../../../../etc/passwd", "", false},
		{"escaping_symlink", "var/mail/mail/evil_link", "../../outside", true},
		{"absolute_symlink", "var/mail/mail/abs_link", "/etc/passwd", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tarPath := filepath.Join(tempDir, tc.name+".tar.gz")
			f, err := os.Create(tarPath)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)

			hdr := &tar.Header{
				Name: tc.memberPath,
				Mode: 0o600,
			}
			if tc.isSymlink {
				hdr.Typeflag = tar.TypeSymlink
				hdr.Linkname = tc.target
			} else {
				hdr.Typeflag = tar.TypeReg
				hdr.Size = int64(len("evil"))
			}
			_ = tw.WriteHeader(hdr)
			if !tc.isSymlink {
				_, _ = tw.Write([]byte("evil"))
			}
			_ = tw.Close()
			_ = gz.Close()
			_ = f.Close()

			manifest := &backupauto.Manifest{
				Version:       backupauto.ManifestVersion3,
				LayoutVersion: backupauto.LayoutVersion1,
				Scope:         backupauto.ScopeComplete,
				Consistent:    true,
				Slots: map[string]backupauto.Slot{
					backupauto.SlotMailStorage: {
						Artifact:    filepath.Base(tarPath),
						ArchivePath: "var/mail/mail",
						TargetPath:  "/var/mail/mail",
						Kind:        "dir",
					},
				},
			}

			valErr := validateRestoreArchiveWithManifest(tarPath, filepath.Base(tarPath), manifest, &config.Config{StorageRoot: "/var/mail"}, nil)
			if valErr == nil {
				extractErr := extractRestoreArchive(tarPath, filepath.Base(tarPath), manifest, &config.Config{StorageRoot: "/var/mail"}, nil, targetDir)
				if extractErr == nil {
					t.Fatalf("malicious archive member %s was NOT rejected", tc.memberPath)
				}
			}
		})
	}
}

func TestLegacyV1V2BackupRestoreCompatibility(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "target")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(tempDir, "legacy.tar.gz")
	buildTarGz(t, tarPath, map[string]string{
		"var/mail/mail/mailboxes/legacy.test/user/cur/1": "legacy message body\n",
	})

	sum, size, err := plainFileHash(tarPath)
	if err != nil {
		t.Fatal(err)
	}

	v1Manifest := &backupauto.Manifest{
		Version:    1,
		Scope:      backupauto.ScopeComplete,
		Consistent: true,
		Artifacts: []backupauto.Artifact{
			{Name: "legacy.tar.gz", SHA256: sum, Size: size, Mode: 0o600, Kind: backupauto.KindMailTar},
		},
	}

	cfg := &config.Config{StorageRoot: "/var/mail"}
	if err := validateRestoreArchiveWithManifest(tarPath, filepath.Base(tarPath), v1Manifest, cfg, nil); err != nil {
		t.Fatalf("v1 archive rejected: %v", err)
	}
	if err := extractRestoreArchive(tarPath, filepath.Base(tarPath), v1Manifest, cfg, nil, targetDir); err != nil {
		t.Fatalf("v1 extract failed: %v", err)
	}

	restoredPath := filepath.Join(targetDir, "var/mail/mail/mailboxes/legacy.test/user/cur/1")
	content, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatalf("restored v1 file missing at %s: %v", restoredPath, err)
	}
	if string(content) != "legacy message body\n" {
		t.Fatalf("restored v1 content mismatch: %q", string(content))
	}

	nonStdTar := filepath.Join(tempDir, "nonstd-legacy.tar.gz")
	buildTarGz(t, nonStdTar, map[string]string{
		"custom/storage/mail/box/cur/2": "non-standard legacy body\n",
	})
	legacyMap := map[string]string{
		"mail/storage": "custom/storage/mail",
	}
	if err := validateRestoreArchiveWithManifest(nonStdTar, filepath.Base(nonStdTar), v1Manifest, cfg, legacyMap); err != nil {
		t.Fatalf("non-standard legacy with slot map rejected: %v", err)
	}
	if err := extractRestoreArchive(nonStdTar, filepath.Base(nonStdTar), v1Manifest, cfg, legacyMap, targetDir); err != nil {
		t.Fatalf("non-standard legacy extract failed: %v", err)
	}
	restoredNonStd := filepath.Join(targetDir, "var/mail/mail/box/cur/2")
	nonStdContent, err := os.ReadFile(restoredNonStd)
	if err != nil {
		t.Fatalf("restored non-standard legacy file missing at %s: %v", restoredNonStd, err)
	}
	if string(nonStdContent) != "non-standard legacy body\n" {
		t.Fatalf("content mismatch: %q", string(nonStdContent))
	}
}

func TestProductionTarArchiveRoundTripWithOwnerModeAndLinks(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "restore_target")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	preKeyDir := filepath.Join(targetDir, "etc/urmail")
	if err := os.MkdirAll(preKeyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	preKeyPath := filepath.Join(preKeyDir, "master.key")
	if err := os.WriteFile(preKeyPath, []byte("old-0644-key-contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	preKeyStat, err := os.Stat(preKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if preKeyStat.Mode().Perm() != 0o644 {
		t.Fatalf("expected pre-existing key to have 0644, got %o", preKeyStat.Mode().Perm())
	}

	tarPath := filepath.Join(tempDir, "prod-config.tar.gz")
	tarFile, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(tarFile)
	tw := tar.NewWriter(gz)

	fixedTime := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	keyData := []byte("secret-master-key-0600")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "etc/urmail/master.key",
		Size:     int64(len(keyData)),
		Mode:     0o600,
		ModTime:  fixedTime,
		Uid:      1000,
		Gid:      1000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(keyData); err != nil {
		t.Fatal(err)
	}

	aData := []byte("regular file a content")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "etc/urmail/a",
		Size:     int64(len(aData)),
		Mode:     0o640,
		ModTime:  fixedTime,
		Uid:      1000,
		Gid:      1000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(aData); err != nil {
		t.Fatal(err)
	}

	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeLink,
		Name:     "etc/urmail/b",
		Linkname: "etc/urmail/a",
		Mode:     0o640,
		ModTime:  fixedTime,
	}); err != nil {
		t.Fatal(err)
	}

	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "etc/urmail/c",
		Linkname: "a",
		ModTime:  fixedTime,
	}); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tarFile.Close(); err != nil {
		t.Fatal(err)
	}

	artName := filepath.Base(tarPath)
	manifest := &backupauto.Manifest{
		Version:       backupauto.ManifestVersion3,
		LayoutVersion: backupauto.LayoutVersion1,
		Scope:         backupauto.ScopeComplete,
		Consistent:    true,
		Slots: map[string]backupauto.Slot{
			backupauto.SlotConfigOperator: {
				Artifact:    artName,
				ArchivePath: "etc/urmail",
				TargetPath:  "/etc/urmail",
				Kind:        "dir",
			},
			backupauto.SlotConfigMasterKey: {
				Artifact:    artName,
				ArchivePath: "etc/urmail/master.key",
				TargetPath:  "/etc/urmail/master.key",
				Kind:        "file",
			},
		},
	}

	cfg := &config.Config{
		StorageRoot:   "/var/mail",
		MasterKeyPath: "/etc/urmail/master.key",
	}

	if err := validateRestoreArchiveWithManifest(tarPath, artName, manifest, cfg, nil); err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if err := extractRestoreArchive(tarPath, artName, manifest, cfg, nil, targetDir); err != nil {
		t.Fatalf("extract failed: %v", err)
	}

	newKeyStat, err := os.Stat(preKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if newKeyStat.Mode().Perm() != 0o600 {
		t.Fatalf("expected restored key to have 0600 mode, got %o", newKeyStat.Mode().Perm())
	}
	keyRead, err := os.ReadFile(preKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(keyRead) != string(keyData) {
		t.Fatalf("restored key content mismatch: got %q, want %q", string(keyRead), string(keyData))
	}

	pathA := filepath.Join(targetDir, "etc/urmail/a")
	pathB := filepath.Join(targetDir, "etc/urmail/b")
	bRead, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("read hardlink b failed: %v", err)
	}
	if string(bRead) != string(aData) {
		t.Fatalf("hardlink content mismatch: got %q, want %q", string(bRead), string(aData))
	}
	statA, err := os.Stat(pathA)
	if err != nil {
		t.Fatal(err)
	}
	statB, err := os.Stat(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(statA, statB) {
		t.Fatalf("expected %s and %s to be hard links to the same file", pathA, pathB)
	}

	pathC := filepath.Join(targetDir, "etc/urmail/c")
	cRead, err := os.ReadFile(pathC)
	if err != nil {
		t.Fatalf("read symlink c failed: %v", err)
	}
	if string(cRead) != string(aData) {
		t.Fatalf("symlink target content mismatch: got %q, want %q", string(cRead), string(aData))
	}
}

func TestV2ManifestRestoreCompatibility(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "target")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(tempDir, "v2-backup.tar.gz")
	buildTarGz(t, tarPath, map[string]string{
		"var/mail/mail/mailboxes/v2.test/user/cur/1": "v2 message body\n",
	})

	sum, size, err := plainFileHash(tarPath)
	if err != nil {
		t.Fatal(err)
	}

	v2Manifest := &backupauto.Manifest{
		Version:    backupauto.ManifestVersion2,
		Scope:      backupauto.ScopeComplete,
		Consistent: true,
		CreatedAt:  time.Now().UTC(),
		Artifacts: []backupauto.Artifact{
			{
				Name:            filepath.Base(tarPath),
				SHA256:          sum,
				Size:            size,
				Mode:            0o600,
				EncryptedName:   filepath.Base(tarPath) + ".age",
				EncryptedSHA256: "fake-sha",
				EncryptedSize:   size + 100,
				Algorithm:       backupauto.EncryptionAlgorithmAge,
				Kind:            backupauto.KindMailTar,
			},
		},
		Encryption: &backupauto.EncryptionInfo{
			Algorithm: backupauto.EncryptionAlgorithmAge,
			Recipient: "age1fake",
		},
	}

	cfg := &config.Config{StorageRoot: "/var/mail"}
	artName := filepath.Base(tarPath)
	if err := validateRestoreArchiveWithManifest(tarPath, artName, v2Manifest, cfg, nil); err != nil {
		t.Fatalf("v2 archive rejected: %v", err)
	}
	if err := extractRestoreArchive(tarPath, artName, v2Manifest, cfg, nil, targetDir); err != nil {
		t.Fatalf("v2 extract failed: %v", err)
	}

	restoredPath := filepath.Join(targetDir, "var/mail/mail/mailboxes/v2.test/user/cur/1")
	content, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatalf("restored v2 file missing at %s: %v", restoredPath, err)
	}
	if string(content) != "v2 message body\n" {
		t.Fatalf("restored v2 content mismatch: %q", string(content))
	}
}

func TestFileSlotRejectsDescendantAndNonReg(t *testing.T) {
	tempDir := t.TempDir()

	for _, tc := range []struct {
		name       string
		memberName string
		typeflag   byte
	}{
		{"descendant_in_file_slot", "etc/urmail/master.key/sub", tar.TypeReg},
		{"dir_in_file_slot", "etc/urmail/master.key", tar.TypeDir},
		{"symlink_in_file_slot", "etc/urmail/master.key", tar.TypeSymlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tarPath := filepath.Join(tempDir, tc.name+".tar.gz")
			tarFile, err := os.Create(tarPath)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(tarFile)
			tw := tar.NewWriter(gz)

			hdr := &tar.Header{
				Typeflag: tc.typeflag,
				Name:     tc.memberName,
				Mode:     0o600,
			}
			if tc.typeflag == tar.TypeReg {
				hdr.Size = 4
			}
			if tc.typeflag == tar.TypeSymlink {
				hdr.Linkname = "target"
			}
			_ = tw.WriteHeader(hdr)
			if tc.typeflag == tar.TypeReg {
				_, _ = tw.Write([]byte("data"))
			}
			_ = tw.Close()
			_ = gz.Close()
			_ = tarFile.Close()

			artName := filepath.Base(tarPath)
			manifest := &backupauto.Manifest{
				Version:       backupauto.ManifestVersion3,
				LayoutVersion: backupauto.LayoutVersion1,
				Scope:         backupauto.ScopeComplete,
				Consistent:    true,
				Slots: map[string]backupauto.Slot{
					backupauto.SlotConfigMasterKey: {
						Artifact:    artName,
						ArchivePath: "etc/urmail/master.key",
						TargetPath:  "/etc/urmail/master.key",
						Kind:        "file",
					},
				},
			}

			cfg := &config.Config{MasterKeyPath: "/etc/urmail/master.key"}
			if err := validateRestoreArchiveWithManifest(tarPath, artName, manifest, cfg, nil); err == nil {
				t.Fatalf("expected validation failure for %s", tc.name)
			}
		})
	}
}

func TestManifestV3NoFallbackToLegacyRoots(t *testing.T) {
	tempDir := t.TempDir()
	tarPath := filepath.Join(tempDir, "unmatched.tar.gz")
	buildTarGz(t, tarPath, map[string]string{
		"etc/postfix/urmail/custom.cf": "custom postfix cf\n",
	})

	artName := filepath.Base(tarPath)
	manifest := &backupauto.Manifest{
		Version:       backupauto.ManifestVersion3,
		LayoutVersion: backupauto.LayoutVersion1,
		Scope:         backupauto.ScopeComplete,
		Consistent:    true,
		Slots: map[string]backupauto.Slot{
			backupauto.SlotConfigMasterKey: {
				Artifact:    artName,
				ArchivePath: "etc/urmail/master.key",
				TargetPath:  "/etc/urmail/master.key",
				Kind:        "file",
			},
		},
	}

	cfg := &config.Config{MasterKeyPath: "/etc/urmail/master.key"}
	err := validateRestoreArchiveWithManifest(tarPath, artName, manifest, cfg, nil)
	if err == nil {
		t.Fatalf("expected Manifest v3 to reject unmatched member without falling back to legacy default roots")
	}
}
func TestOpenRootSymlinkBehavior(t *testing.T) {
	tmpDir := t.TempDir()
	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := root.Mkdir("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("sub", "link-to-sub"); err != nil {
		t.Fatal(err)
	}

	// Document Go stdlib behavior: OpenRoot follows internal relative symlinks without error.
	// This confirms that callers must perform an inode identity check (SameFile)
	// against Lstat to detect a concurrent symlink replacement.
	r2, err := root.OpenRoot("link-to-sub")
	if err != nil {
		t.Fatalf("unexpected failure: Go os.Root.OpenRoot is documented to follow relative symlinks: %v", err)
	}
	defer func() { _ = r2.Close() }()
}
