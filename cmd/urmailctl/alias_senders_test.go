package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/aliassenders"
)

func TestAliasSenderPrivateInputAndReviewFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "intent.json")
	raw := `{"tenant_id":1,"actor_id":2,"alias_id":3,"reason":"Explicitly reviewed alias sender maintenance","sender_logins":[]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	intent, err := readAliasSenderIntent(path)
	if err != nil || intent.SenderLogins == nil {
		t.Fatal("explicit revoke file rejected", err)
	}
	for _, bad := range []string{raw + `{}`, strings.Replace(raw, `"tenant_id":1`, `"unknown":1`, 1), strings.Repeat(" ", 65537)} {
		if err = os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err = readAliasSenderIntent(path); err == nil {
			t.Fatal("unbounded/ambiguous private input accepted")
		}
	}
	if err = os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = readAliasSenderIntent(path); err == nil {
		t.Fatal("public input accepted")
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = readAliasSenderIntent(link); err == nil {
		t.Fatal("symlink input accepted")
	}
	review := aliassenders.Review{Version: 1, Intent: intent, Digest: strings.Repeat("a", 64)}
	output := filepath.Join(dir, "review.json")
	if err = writeAliasSenderReview(output, review); err != nil {
		t.Fatal(err)
	}
	if info, e := os.Stat(output); e != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("private output permissions", e)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var decoded aliassenders.Review
	if err = json.Unmarshal(content, &decoded); err != nil || decoded.Digest != review.Digest {
		t.Fatal("private review failed", err)
	}
	if err = writeAliasSenderReview(output, review); err == nil {
		t.Fatal("existing review overwritten")
	}
	if err = writeAliasSenderReview(link, review); err == nil {
		t.Fatal("symlink output followed")
	}
}
