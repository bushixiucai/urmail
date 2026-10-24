package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreResumeBindsArtifactsTargetAndCompletedDatabaseStage(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-private restore state is exercised in Linux acceptance container")
	}
	path := filepath.Join(t.TempDir(), "marker")
	state, err := createRestoreQuarantine(path)
	if err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(t.TempDir(), "snapshot.dump")
	if err = os.WriteFile(dump, []byte("owned-database-snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	url := "postgres://urmail_migrate:private@127.0.0.1:55432/owned_restore?sslmode=disable"
	fingerprint, err := restoreInputFingerprint(url, []string{dump, "", "", ""})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquireRestoreOperation(path)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := acquireRestoreOperation(path); err == nil {
		_ = duplicate.Close()
		t.Fatal("concurrent restore operation accepted")
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	proof := restoreInputProof{state.ID, fingerprint}
	if err = writeRestoreProof(path, "input.json", proof); err != nil {
		t.Fatal(err)
	}
	_, skip, err := resumeRestoreQuarantine(path, state.ID, fingerprint)
	if err != nil || skip {
		t.Fatalf("unfinished database stage skipped: %v %v", skip, err)
	}
	if err = writeRestoreProof(path, "database-restored.json", proof); err != nil {
		t.Fatal(err)
	}
	_, skip, err = resumeRestoreQuarantine(path, state.ID, fingerprint)
	if err != nil || !skip {
		t.Fatalf("repaired database would be overwritten: %v %v", skip, err)
	}
	if _, _, err = resumeRestoreQuarantine(path, strings.Repeat("0", 32), fingerprint); err == nil {
		t.Fatal("wrong restore identity accepted")
	}
	changed, err := restoreInputFingerprint(strings.Replace(url, "owned_restore", "another_target", 1), []string{dump, "", "", ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = resumeRestoreQuarantine(path, state.ID, changed); err == nil {
		t.Fatal("resume changed target")
	}
	if err = os.WriteFile(dump, []byte("different-snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err = restoreInputFingerprint(url, []string{dump, "", "", ""})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = resumeRestoreQuarantine(path, state.ID, changed); err == nil {
		t.Fatal("resume changed snapshot")
	}
	if err = completeRestoreQuarantine(path, state.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = resumeRestoreQuarantine(path, state.ID, fingerprint); err == nil {
		t.Fatal("completed restore restarted")
	}
}

func TestRestoreOperationLockChild(t *testing.T) {
	if os.Getenv("URMAIL_RESTORE_LOCK_CHILD") != "1" {
		return
	}
	file := os.NewFile(3, "inherited-restore-lock")
	if file == nil {
		t.Fatal("missing inherited lock")
	}
	if _, err := fmt.Fprintln(os.Stdout, "LOCK_READY"); err != nil {
		t.Fatal(err)
	}
	var signal [1]byte
	if _, err := os.Stdin.Read(signal[:]); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestRestoreOperationLockSurvivesParentDescriptorClose(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-private Linux acceptance")
	}
	path := filepath.Join(t.TempDir(), "marker")
	if _, err := createRestoreQuarantine(path); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireRestoreOperation(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestRestoreOperationLockChild$")
	command.Env = append(os.Environ(), "URMAIL_RESTORE_LOCK_CHILD=1")
	command.ExtraFiles = append(command.ExtraFiles, lock)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "LOCK_READY" {
		t.Fatalf("child startup %q %v", line, err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := acquireRestoreOperation(path); err == nil {
		_ = duplicate.Close()
		t.Fatal("parent loss released the child's active restore lock")
	}
	if _, err = input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err = input.Close(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err != nil {
		t.Fatal(err)
	}
	released, err := acquireRestoreOperation(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = released.Close(); err != nil {
		t.Fatal(err)
	}
}
