package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAutomaticBackupWriterBoundaryAndReceiptValidation(t *testing.T) {
	current := backupWriterUnits()
	for _, unit := range current {
		if strings.Contains(unit, "backup-auto") {
			t.Fatal("backup maintenance would stop its own cgroup", unit)
		}
	}
	restoreHas := map[string]bool{}
	for _, unit := range restoreWriterUnits {
		restoreHas[unit] = true
	}
	for _, unit := range []string{"urmail-backup-auto.timer", "urmail-backup-auto.service"} {
		if !restoreHas[unit] {
			t.Fatal("restore leaves a backup writer running", unit)
		}
	}
	receipt := backupMaintenanceReceipt{Version: 1, CreatedAt: time.Now(), Units: []string{"urmaild-agent.service", "urmaild.service"}}
	if err := validateBackupMaintenanceReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	for _, units := range [][]string{{"urmail-backup-auto.service"}, {"other.service"}, {"urmaild.service", "urmaild.service"}} {
		receipt.Units = units
		if validateBackupMaintenanceReceipt(receipt) == nil {
			t.Fatal("unowned or duplicate unit accepted", units)
		}
	}
}
func TestBackupGenerationStampRejectsPathAndMalformedInputBeforeMaintenance(t *testing.T) {
	for _, stamp := range []string{"../other", "20261028T120000Z/owned", "20261328T120000Z", "20261028", ""} {
		err := runBackup(context.Background(), []string{"--include-mail", "--maintenance", "--stamp", stamp})
		if err == nil || !strings.Contains(err.Error(), "stamp") {
			t.Fatal("invalid stamp reached maintenance", stamp, err)
		}
	}
	if err := runBackup(context.Background(), []string{"--stamp"}); err == nil {
		t.Fatal("missing stamp accepted")
	}
}
func TestAutomaticBackupEntrypointsRejectArguments(t *testing.T) {
	if runBackupAuto(t.Context(), []string{"--out-dir", "/tmp"}) == nil {
		t.Fatal("automatic job accepts caller paths")
	}
	if runBackupAutoResume(t.Context(), []string{"other.service"}) == nil {
		t.Fatal("automatic resume accepts caller units")
	}
}
