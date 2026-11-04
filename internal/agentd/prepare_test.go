package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflinePrepareNeverRestartsOrClaimsRuntimeReady(t *testing.T) {
	m := testManager(t)
	m.prepareOnly = true
	m.status.Applied = "previous-runtime-generation"
	base := m.run
	var commands []string
	m.run = func(ctx context.Context, path string, args ...string) error {
		commands = append(commands, path+" "+strings.Join(args, " "))
		if path == "/usr/bin/systemctl" {
			return errors.New("offline preparation must never invoke systemctl")
		}
		return base(ctx, path, args...)
	}
	m.readinessProbe = func(context.Context) error {
		t.Error("offline preparation ran a runtime probe")
		return errors.New("offline probe")
	}
	generation, _, err := m.Apply(emptyApply())
	if err != nil {
		t.Fatal(err)
	}
	if generation == "" || m.status.Desired != generation || m.status.Ready || m.status.Step != "prepared" || m.status.Applied != "previous-runtime-generation" {
		t.Fatalf("prepared status incorrectly implies runtime application: %+v", m.status)
	}
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); !os.IsNotExist(err) {
		t.Fatalf("successful prepare left journal: %v", err)
	}
	if len(m.manifestFiles) == 0 {
		t.Fatal("prepared generation lacks integrity records")
	}
	for _, c := range commands {
		if strings.Contains(c, "systemctl") {
			t.Errorf("runtime command during prepare: %s", c)
		}
	}
}

func TestOfflinePrepareFailureRemainsNotReady(t *testing.T) {
	m := testManager(t)
	m.prepareOnly = true
	base := m.run
	m.run = func(ctx context.Context, path string, args ...string) error {
		if path == "/usr/bin/systemctl" {
			t.Error("prepare rollback restarted a service")
			return errors.New("forbidden runtime command")
		}
		if path == "/usr/sbin/nginx" {
			return errors.New("injected invalid configuration")
		}
		return base(ctx, path, args...)
	}
	if _, _, err := m.Apply(emptyApply()); err == nil {
		t.Fatal("invalid configuration preparation succeeded")
	}
	if m.status.Ready {
		t.Fatal("failed prepare claimed ready")
	}
}
