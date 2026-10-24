package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportMIADBDryRunRequiresExplicitMappingAndNeverOpensPlatform(t *testing.T) {
	t.Setenv("URMAIL_DATABASE_URL", "invalid-must-never-connect")
	directory := t.TempDir()
	config := filepath.Join(directory, "mailinabox.conf")
	sqlite := filepath.Join(directory, "users.sqlite")
	if err := os.WriteFile(config, []byte("STORAGE_ROOT=/home/user-data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `import sqlite3,sys
c=sqlite3.connect(sys.argv[1]);c.executescript("CREATE TABLE users(id INTEGER PRIMARY KEY AUTOINCREMENT,email TEXT NOT NULL UNIQUE,password TEXT NOT NULL,extra,privileges TEXT NOT NULL DEFAULT '',quota TEXT NOT NULL DEFAULT '0');CREATE TABLE aliases(id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT NOT NULL UNIQUE,destination TEXT NOT NULL,permitted_senders TEXT);CREATE TABLE auto_aliases(id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT NOT NULL UNIQUE,destination TEXT NOT NULL,permitted_senders TEXT);CREATE TABLE mfa(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id INTEGER NOT NULL,type TEXT NOT NULL,secret TEXT NOT NULL,mru_token TEXT,label TEXT,FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE);")
c.execute('INSERT INTO users(email,password)VALUES(?,?)',('private@cli.fixture.test','{SHA512-CRYPT}$6$privatesalt$'+'A'*86));c.commit();c.close()`
	if err := exec.CommandContext(context.Background(), "/usr/bin/python3", "-I", "-c", script, sqlite).Run(); err != nil {
		t.Fatal("private SQLite fixture failed")
	}
	args := make([]string, 0, 7)
	args = append(args, "--dry-run", "--config", config, "--sqlite", sqlite)
	var output bytes.Buffer
	if err := importMIADBDryRun(context.Background(), args, &output); err == nil {
		t.Fatal("missing explicit mapping did not block")
	}
	if !strings.Contains(output.String(), "MISSING_EXPLICIT_TENANT_MAPPING") || strings.Contains(output.String(), "private@") || strings.Contains(output.String(), "SHA512") {
		t.Fatal("CLI lost blocker or leaked a secret")
	}
	mapping := filepath.Join(directory, "mapping.json")
	if err := os.WriteFile(mapping, []byte(`{"version":1,"domains":[{"domain":"cli.fixture.test","tenant_ref":"single-tenant"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := importMIADBDryRun(context.Background(), append(args, "--mapping", mapping), &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Status  string `json:"status"`
		Changed bool   `json:"changed"`
		Apply   bool   `json:"apply_available"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Status != "dry_run_complete" || result.Changed || result.Apply {
		t.Fatalf("unexpected CLI result %+v %v", result, err)
	}
	for _, bad := range [][]string{{"--config", config, "--sqlite", sqlite}, {"--apply", "--config", config, "--sqlite", sqlite}, {"--dry-run", "--config", config}, {"--dry-run", "--config", config, "--sqlite", sqlite, "trailing"}} {
		output.Reset()
		if err := importMIADBDryRun(context.Background(), bad, &output); err == nil || output.Len() != 0 {
			t.Fatal("unsupported apply or implicit source was accepted")
		}
	}
}
