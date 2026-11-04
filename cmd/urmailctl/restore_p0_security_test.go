package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/config"
)

func TestP0TargetDirectoryForSlotRejectsUnknownSlot(t *testing.T) {
	cfg := &config.Config{
		StorageRoot:   "/var/mail",
		MasterKeyPath: "/etc/urmail/master.key",
	}

	for _, badSlot := range []string{
		"etc/cron.d",
		"cron/evil",
		"root/ssh",
		"../../etc/shadow",
		"custom/unknown/slot",
	} {
		target, kind := targetDirectoryForSlot(badSlot, cfg, "")
		if target != "" || kind != "" {
			t.Fatalf("expected unknown slot %q to return empty target and kind, got target=%q kind=%q", badSlot, target, kind)
		}
	}
}

func TestP0KindMismatchRejectedBeforeMutation(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "target")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(tempDir, "kind_mismatch.tar.gz")
	tarFile, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(tarFile)
	tw := tar.NewWriter(gz)

	// In tar, write as a directory
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "etc/urmail/master.key",
		Mode:     0o755,
	}); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = tarFile.Close()

	artName := filepath.Base(tarPath)
	// Untrusted manifest claims config/master-key is "dir"
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
				Kind:        "dir", // Mismatch with trusted slot kind "file"!
			},
		},
	}

	cfg := &config.Config{
		MasterKeyPath: "/etc/urmail/master.key",
	}

	if err := validateRestoreArchiveWithManifest(tarPath, artName, manifest, cfg, nil); err == nil {
		t.Fatalf("expected validation failure for Kind mismatch on slot %s", backupauto.SlotConfigMasterKey)
	}

	if err := extractRestoreArchive(tarPath, artName, manifest, cfg, nil, targetDir); err == nil {
		t.Fatalf("expected extraction failure for Kind mismatch on slot %s", backupauto.SlotConfigMasterKey)
	}

	// Assert target directory remains completely empty and no mutation occurred
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected targetDir to remain empty before mutations, found %d entries", len(entries))
	}
}

func TestP0FileSlotDescendantNeverFallsBackToParentOperatorDir(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "target")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(tempDir, "nested_descendant.tar.gz")
	tarFile, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(tarFile)
	tw := tar.NewWriter(gz)

	// An adversarial member that is a child of the master.key file slot,
	// e.g. "etc/urmail/master.key/evil_payload"
	payload := []byte("malicious payload")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "etc/urmail/master.key/evil_payload",
		Size:     int64(len(payload)),
		Mode:     0o644,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = tarFile.Close()

	artName := filepath.Base(tarPath)
	// Both config/operator ("etc/urmail", dir) and config/master-key ("etc/urmail/master.key", file) are defined.
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

	// Validation must reject this descendant without falling back to config/operator dir!
	if err := validateRestoreArchiveWithManifest(tarPath, artName, manifest, cfg, nil); err == nil {
		t.Fatalf("expected validation failure: file slot descendant must be rejected and not fall back to parent operator dir")
	}

	if err := extractRestoreArchive(tarPath, artName, manifest, cfg, nil, targetDir); err == nil {
		t.Fatalf("expected extraction failure: file slot descendant must be rejected and not fall back to parent operator dir")
	}

	// Verify targetDir remains untouched and no evil_payload is created
	evilPath := filepath.Join(targetDir, "etc/urmail/master.key/evil_payload")
	if _, err := os.Stat(evilPath); err == nil {
		t.Fatalf("evil payload was written to %s", evilPath)
	}
}

func TestP0UnknownSlotNameInManifestRejected(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "target")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(tempDir, "cron_slot.tar.gz")
	tarFile, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(tarFile)
	tw := tar.NewWriter(gz)

	cronContent := []byte("* * * * * root /bin/evil\n")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "etc/cron.d/evil",
		Size:     int64(len(cronContent)),
		Mode:     0o644,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(cronContent); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = tarFile.Close()

	artName := filepath.Base(tarPath)
	// Malicious slot with unknown slotName "etc/cron.d"
	manifest := &backupauto.Manifest{
		Version:       backupauto.ManifestVersion3,
		LayoutVersion: backupauto.LayoutVersion1,
		Scope:         backupauto.ScopeComplete,
		Consistent:    true,
		Slots: map[string]backupauto.Slot{
			"etc/cron.d": {
				Artifact:    artName,
				ArchivePath: "etc/cron.d",
				TargetPath:  "/etc/cron.d",
				Kind:        "dir",
			},
		},
	}

	cfg := &config.Config{
		StorageRoot:   "/var/mail",
		MasterKeyPath: "/etc/urmail/master.key",
	}

	if err := validateRestoreArchiveWithManifest(tarPath, artName, manifest, cfg, nil); err == nil {
		t.Fatalf("expected validation failure for unknown manifest slotName etc/cron.d")
	}

	if err := extractRestoreArchive(tarPath, artName, manifest, cfg, nil, targetDir); err == nil {
		t.Fatalf("expected extraction failure for unknown manifest slotName etc/cron.d")
	}

	evilCronPath := filepath.Join(targetDir, "etc/cron.d/evil")
	if _, err := os.Stat(evilCronPath); err == nil {
		t.Fatalf("evil cron file was written to %s", evilCronPath)
	}
}
