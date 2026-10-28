package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	recall "github.com/bushixiucai/urmail/internal/domain/recall"
	"github.com/bushixiucai/urmail/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestReviewedRecallDovecotRuntime is explicitly opt-in and creates its own
// Dovecot instance, Maildir and PostgreSQL database. It never reads or changes
// the installed node's Dovecot configuration, mailboxes or service lifecycle.
func TestReviewedRecallDovecotRuntime(t *testing.T) {
	if os.Getenv("URMAIL_RECALL_DOVECOT_RUNTIME") != "1" {
		t.Skip("isolated Dovecot runtime was not requested")
	}
	if os.Geteuid() != 0 {
		t.Fatal("run this fixture only in an owned root container")
	}
	f := newRecallReviewFixture(t)
	ctx := t.Context()
	base := t.TempDir()
	if err := os.Chmod(filepath.Dir(base), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "homes", f.address)
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(home, 8, 8); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(base, "dovecot.conf")
	body := fmt.Sprintf(`base_dir = %s/run
log_path = %s/dovecot.log
protocols = imap
listen = 127.0.0.1
ssl = no
first_valid_uid = 8
mail_location = maildir:%s/homes/%%u/Maildir
namespace inbox {
 inbox = yes
 separator = /
}
passdb {
 driver = static
 args = password=isolated-runtime-password
}
userdb {
 driver = static
 args = uid=8 gid=8 home=%s/homes/%%u allow_all_users=yes
}
service imap-login {
 inet_listener imap {
  port = 0
 }
 inet_listener imaps {
  port = 0
 }
}
service auth {
 unix_listener auth-userdb {
  mode = 0600
  user = root
 }
}
service stats {
 unix_listener stats-writer {
  mode = 0666
 }
}
`, base, base, base, base)
	if err := os.WriteFile(config, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	daemon := exec.CommandContext(ctx, "/usr/sbin/dovecot", "-F", "-c", config) // #nosec G204 -- fixed executable and owned temporary config.
	var daemonLog bytes.Buffer
	daemon.Stdout = &daemonLog
	daemon.Stderr = &daemonLog
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Signal(syscall.SIGTERM); _ = daemon.Wait() })
	command := func(input []byte, args ...string) ([]byte, error) {
		fixed := append([]string{"-c", config}, args...)
		cmd := exec.CommandContext(ctx, "/usr/bin/doveadm", fixed...) // #nosec G204 -- fixed doveadm operations in an explicit isolated fixture.
		cmd.Stdin = bytes.NewReader(input)
		result, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("isolated doveadm: %w: %s", err, result)
		}
		return result, nil
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := command(nil, "mailbox", "create", "-u", f.address, "INBOX"); err == nil {
			break
		} else if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(base, "dovecot.log"))
			t.Fatalf("Dovecot did not become ready: %v %s %s", err, daemonLog.String(), log)
		}
		time.Sleep(100 * time.Millisecond)
	}
	raw := []byte("Message-ID: " + f.messageID + "\r\nFrom: sender@example.test\r\nTo: " + f.address + "\r\nSubject: synthetic fixed-copy recall\r\n\r\nOriginal runtime payload\r\n")
	if _, err := command(raw, "save", "-u", f.address); err != nil {
		t.Fatal(err)
	}
	if _, err := command(nil, "mailbox", "create", "-u", f.address, "Sent"); err != nil {
		t.Fatal(err)
	}
	if _, err := command(nil, "copy", "-u", f.address, "Sent", "mailbox", "INBOX", "all"); err != nil {
		t.Fatal(err)
	}
	f.manager.output = func(_ context.Context, args ...string) ([]byte, error) { return command(nil, args...) }
	failureGUID := ""
	f.manager.run = func(_ context.Context, path string, args ...string) error {
		if path != "/usr/bin/doveadm" {
			t.Fatal("unexpected executable")
		}
		if failureGUID != "" && len(args) > 4 && args[4] == failureGUID {
			return fmt.Errorf("injected failure before the selected Sent mutation")
		}
		_, err := command(nil, args...)
		return err
	}
	review := func(choose func(recall.Copy) bool) store.RecallRequests {
		params, _ := json.Marshal(RecallPreviewParams{TenantID: f.tenant, EntryID: f.entry, ActorID: f.actor})
		out, _, err := f.manager.PreviewRecall(params)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot recall.Snapshot
		if err = json.Unmarshal([]byte(out), &snapshot); err != nil {
			t.Fatal(err)
		}
		selected := []string{}
		for _, localCopy := range snapshot.Copies {
			if !recall.MessageGUIDPattern.MatchString(localCopy.MessageGUID) {
				t.Fatal("runtime message GUID rejected")
			}
			if localCopy.Folder == "Sent" {
				failureGUID = localCopy.GUID
			}
			if choose(localCopy) {
				selected = append(selected, localCopy.Key())
			}
		}
		digest, _ := snapshot.Digest()
		tx, err := f.app.UserTx(ctx, f.tenant, f.actor)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		q := store.New(tx)
		row, err := q.StoreRecallReviewPreview(ctx, store.StoreRecallReviewPreviewParams{TenantID: f.tenant, ActorID: f.actor, EntryID: f.entry, Token: uuid.NewString(), Digest: digest, Snapshot: []byte(out)})
		if err != nil {
			t.Fatal(err)
		}
		keys, _ := json.Marshal(selected)
		confirmed, err := q.ConfirmRecallReview(ctx, store.ConfirmRecallReviewParams{TenantID: f.tenant, ActorID: pgtype.Int8{Int64: f.actor, Valid: true}, RecallID: row.ID, Token: row.PreviewToken, Digest: row.PreviewDigest, Selected: keys})
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return store.RecallRequests(confirmed)
	}
	first := review(func(recall.Copy) bool { return true })
	partial := f.execute(t, first)
	if partial.Status != "failed" || partial.Reason.String != "local_partial_result" {
		t.Fatalf("partial facts %s %s", partial.Status, partial.Reason.String)
	}
	if _, err := command(raw, "save", "-u", f.address); err != nil {
		t.Fatal(err)
	}
	retry := review(func(localCopy recall.Copy) bool { return localCopy.Folder == "Sent" })
	failureGUID = ""
	done := f.execute(t, retry)
	if done.Status != "local_recalled" {
		t.Fatalf("retry %s %s", done.Status, done.Reason.String)
	}
	copies, err := f.manager.recallExactCopies(ctx, f.address, f.messageID)
	if err != nil || len(copies) != 1 || copies[0].Folder != "INBOX" {
		t.Fatalf("new arrival was removed: %+v %v", copies, err)
	}
	if after := f.execute(t, retry); after.Status != "local_recalled" {
		t.Fatal("completed intent changed")
	}
	// Verify the production conjunct against the actual parser: the same current
	// mailbox GUID/UID with a different message GUID must not expunge anything.
	localCopy := copies[0]
	if _, err = command(nil, "expunge", "-u", f.address, "mailbox-guid", localCopy.GUID, "uid", fmt.Sprint(localCopy.UID), "guid", "unrelated-message-identity"); err != nil {
		t.Fatal(err)
	}
	after, err := f.manager.recallExactCopies(ctx, f.address, f.messageID)
	if err != nil || len(after) != 1 || after[0].MessageGUID != localCopy.MessageGUID {
		t.Fatal("atomic message GUID guard removed another identity")
	}
	t.Logf("Real Dovecot: exact GUID/UID copies, partial failure, selected retry, later arrival and completed replay PASS; message GUID length=%d, type=%s", len(localCopy.MessageGUID), map[bool]string{true: "Maildir opaque name", false: "opaque identifier"}[strings.Contains(localCopy.MessageGUID, ".")])
}
