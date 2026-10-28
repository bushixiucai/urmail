package main

import (
	"context"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestManagedContainerBackupSourceAndEndpointBinding(t *testing.T) {
	source := "postgres://urmail_migrate:private-fixture-password@127.0.0.1:55433/urmail?sslmode=disable"
	plan, e := containerBackupSourceURL(source, "urmail-managed-pg")
	if e != nil || strings.Contains(plan, "private-fixture-password") {
		t.Fatalf("managed plan rejected or disclosed credentials: %v", e)
	}
	parsed, e := url.Parse(plan)
	if e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("a", 64)
	valid := `{"id":"` + id + `","running":true,"ports":{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"55433"}]}}`
	if got, e := validatedBackupContainer(parsed, []byte(valid)); e != nil || got != id {
		t.Fatalf("loopback endpoint rejected: %v", e)
	}
	for name, raw := range map[string]string{
		"stopped":         strings.Replace(valid, `"running":true`, `"running":false`, 1),
		"foreign port":    strings.Replace(valid, "55433", "55434", 1),
		"public binding":  strings.Replace(valid, "127.0.0.1", "0.0.0.0", 1),
		"foreign address": strings.Replace(valid, "127.0.0.1", "10.0.0.1", 1),
		"mutable name":    strings.Replace(valid, id, "urmail-managed-pg", 1),
		"missing port":    `{"id":"` + id + `","running":true,"ports":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := validatedBackupContainer(parsed, []byte(raw)); e == nil {
				t.Fatal("foreign/mutable endpoint accepted")
			}
		})
	}
	for name, connection := range map[string]string{
		"remote":             strings.Replace(source, "127.0.0.1", "db.external.test", 1),
		"wrong role":         strings.Replace(source, "urmail_migrate", "urmail_app", 1),
		"query override":     source + "&options=-csearch_path=foreign",
		"invalid escape":     source + "&options=%zz",
		"socket override":    source + "&host=%2Ftmp",
		"database traversal": strings.Replace(source, "/urmail?", "/../other?", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := containerBackupSourceURL(connection, "urmail-managed-pg"); e == nil {
				t.Fatal("unmanaged source accepted")
			}
		})
	}
	if _, e := containerBackupSourceURL(source, "--privileged"); e == nil {
		t.Fatal("option-shaped container accepted")
	}
	for _, query := range []string{"host_port=0", "host_port=55433&host_port=55434", "host_port=55433&command=exec", "host_port=55433&other=%zz"} {
		planCopy := *parsed
		planCopy.RawQuery = query
		if _, e := validatedBackupContainer(&planCopy, []byte(valid)); e == nil {
			t.Fatal("invalid plan accepted")
		}
	}
}

// Explicit opt-in: only reads the configured managed database into a private
// disposable archive and runs pg_restore --list; it never stops or restores it.
func TestManagedContainerBackupReadOnlyRuntime(t *testing.T) {
	container := os.Getenv("URMAIL_TEST_MANAGED_CONTAINER")
	if container == "" {
		t.Skip("explicit owned managed-container runtime required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("managed-container runtime requires root")
	}
	cfg, e := config.Load()
	if e != nil {
		t.Fatal("cannot load managed configuration")
	}
	source, e := containerBackupSourceURL(cfg.DatabaseURLMigrate, container)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command, e := containerBackupDatabaseCommand(ctx, source)
	if e != nil {
		t.Fatal(e)
	}
	file, e := os.OpenFile(filepath.Join(t.TempDir(), "managed.dump"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = file.Close() }()
	command.Stdout = file
	command.Stderr = io.Discard
	if e = command.Run(); e != nil {
		t.Fatal("read-only managed pg_dump failed")
	}
	if e = file.Sync(); e != nil {
		t.Fatal(e)
	}
	info, e := file.Stat()
	if e != nil || info.Size() < 5 {
		t.Fatal("managed dump is empty")
	}
	if _, e = file.Seek(0, 0); e != nil {
		t.Fatal(e)
	}
	magic := make([]byte, 5)
	if _, e = io.ReadFull(file, magic); e != nil || string(magic) != "PGDMP" {
		t.Fatal("managed dump is not a custom PostgreSQL archive")
	}
	if _, e = file.Seek(0, 0); e != nil {
		t.Fatal(e)
	}
	id := command.Args[4]
	if !backupContainerID.MatchString(id) {
		t.Fatal("dump did not pin immutable container")
	}
	list := exec.CommandContext(ctx, "/usr/bin/docker", "exec", "-i", "--user", "postgres", id, "pg_restore", "--list") // #nosec G204 -- fixed command and validated immutable ID; archive listing only.
	list.Stdin = file
	list.Stdout = io.Discard
	list.Stderr = io.Discard
	if e = list.Run(); e != nil {
		t.Fatal("managed archive list failed")
	}
	t.Logf("read-only managed pg_dump/pg_restore-list PASS; private archive bytes=%d", info.Size())
}
