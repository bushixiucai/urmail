//go:build linux

package archive

import (
	"bytes"
	"errors"
	"os"
	"syscall"
	"testing"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

func TestArchiveRealENOSPCRefusesDurableAcceptance(t *testing.T) {
	if os.Getenv("URMAIL_ARCHIVE_FAULT_TEST") != "1" {
		t.Skip("requires an isolated tiny tmpfs container")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(SpoolRoot, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Type != 0x01021994 || stat.Blocks*uint64(stat.Bsize) > 2<<20 {
		t.Fatal("fault test requires a tiny isolated tmpfs, never a normal data filesystem")
	}
	_, err := Spool(SpoolRoot, domainMail.ArchiveRecipient{TenantID: 1, MailboxID: 1, Address: "fixture@example.test"}, bytes.NewReader(bytes.Repeat([]byte("x"), 2<<20)))
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("expected real ENOSPC, got %v", err)
	}
	t.Log("real tmpfs ENOSPC confirmed: input was not acknowledged as durable")
}
