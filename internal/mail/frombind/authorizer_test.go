package frombind

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMapFileAuthorizerFailsClosedAndAllows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sender_login.map")
	content := "# Generated\nalice@a.test\talice@a.test\nloop@a.test\talice@a.test\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	a := MapFileAuthorizer{Path: path}

	// Own address passes.
	if reject, _ := a.Authorize("alice@a.test", []string{"Alice <alice@a.test>"}); reject {
		t.Fatal("own address rejected")
	}
	// Foreign address is rejected.
	if reject, _ := a.Authorize("alice@a.test", []string{"CEO <ceo@b.test>"}); !reject {
		t.Fatal("foreign From accepted")
	}
	// The map file disappears: fail closed for authenticated sessions.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if reject, _ := a.Authorize("alice@a.test", []string{"Alice <alice@a.test>"}); !reject {
		t.Fatal("authenticated submission allowed without the map file")
	}
	// Unauthenticated inbound still continues without any map.
	if reject, _ := a.Authorize("", []string{"Outsider <x@elsewhere.test>"}); reject {
		t.Fatal("unauthenticated inbound rejected")
	}
}
