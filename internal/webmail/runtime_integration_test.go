package webmail_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/webmail"
)

// TestNativeWebmailRuntime runs only inside the isolated fixture started by
// scripts/native-webmail-protocol-acceptance.py. It never uses a product database.
func TestNativeWebmailRuntime(t *testing.T) {
	if os.Getenv("URMAIL_WEBMAIL_RUNTIME") != "1" {
		t.Skip("requires isolated native webmail protocol fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	raw, err := os.ReadFile("/tmp/native-mail-runtime/cert.pem")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		t.Fatal("fixture CA not loaded")
	}
	tlsConfig := &tls.Config{RootCAs: roots, ServerName: "runtime.example.test", MinVersion: tls.VersionTLS12}
	dialer := webmail.NetDialer{Addr: "127.0.0.1:993", TLS: tlsConfig, Timeout: 5 * time.Second}
	const password = "isolated-fixture-password"
	sender, err := dialer.Dial(ctx, "alice@example.test", password)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close() }()
	payload := bytes.Repeat([]byte{0, 1, 127, 128, 255}, 2<<20)
	const subject = "native-runtime-protocol-marker"
	outgoing := webmail.Outgoing{
		From: "alice@example.test", To: []string{"bob@example.test"}, Bcc: []string{"hidden@example.test"}, Subject: subject,
		Text: "native body marker", HTML: "<p>native html marker</p>",
		Attachments: []webmail.OutgoingAttachment{{Filename: "exact.bin", ContentType: "application/octet-stream", Content: payload}},
	}
	draft, err := sender.SaveDraft(ctx, "alice@example.test", outgoing, 0, 0)
	if err != nil || draft.UID == 0 || draft.UIDValidity == 0 {
		t.Fatalf("real APPEND: result=%+v err=%v", draft, err)
	}
	t.Logf("PASS real APPEND UID=%d UIDVALIDITY=%d", draft.UID, draft.UIDValidity)
	opened, err := sender.GetMessage(ctx, draft.Folder, draft.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Bcc) != 1 || opened.Bcc[0].Email != "hidden@example.test" || strings.TrimSpace(opened.HTML) != outgoing.HTML || strings.TrimSpace(opened.Text) != outgoing.Text {
		t.Fatalf("draft body/recipient mismatch: %+v", opened)
	}
	replaced, err := sender.SaveDraft(ctx, "alice@example.test", outgoing, draft.UID, draft.UIDValidity)
	if err != nil || replaced.CleanupPending || replaced.UID == draft.UID {
		t.Fatalf("real replacement: result=%+v err=%v", replaced, err)
	}
	if _, err = sender.GetMessage(ctx, draft.Folder, draft.UID); err == nil {
		t.Fatal("old draft remained after replacement")
	}
	if _, err = sender.SaveDraft(ctx, "alice@example.test", outgoing, replaced.UID, replaced.UIDValidity+1); !errors.Is(err, webmail.ErrDraftChanged) {
		t.Fatalf("stale validity accepted: %v", err)
	}
	result, err := sender.ListMessages(ctx, replaced.Folder, webmail.ListOpts{Query: "subject:" + subject})
	if err != nil || result.Total != 1 {
		t.Fatalf("search after replacement: total=%d err=%v", result.Total, err)
	}
	rebuilt, err := webmail.OutgoingFromDraft(ctx, sender, replaced.Folder, replaced.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt.Attachments) != 1 || sha256.Sum256(rebuilt.Attachments[0].Content) != sha256.Sum256(payload) {
		t.Fatal("rebuilt draft attachment hash mismatch")
	}
	rebuilt.From = "alice@example.test"
	t.Log("PASS real draft replacement, stale UIDVALIDITY refusal, search, Bcc/HTML/attachment roundtrip")
	exerciseRuntimeFolders(ctx, t, sender, replaced)
	if _, err = (webmail.NetSubmitter{Addr: "127.0.0.1:587", Timeout: 10 * time.Second}).Send(ctx, "alice@example.test", password, rebuilt); err != nil {
		t.Fatalf("real SMTP submission: %v", err)
	}
	for _, address := range []string{"bob@example.test", "hidden@example.test"} {
		recipient, err := dialer.Dial(ctx, address, password)
		if err != nil {
			t.Fatal(err)
		}
		verifyRuntimeDelivery(ctx, t, recipient, subject, payload)
		_ = recipient.Close()
	}
	t.Logf("PASS SMTP AUTH→Postfix→Dovecot LMTP→strict fixture-TLS IMAP for To+Bcc, attachment SHA256=%x", sha256.Sum256(payload))
	// Recreate the mailbox through an independent IMAP session, then prove that
	// the stale saved generation cannot replace a new mailbox's messages.
	_ = sender.Close()
	time.Sleep(1100 * time.Millisecond)
	rawConn, err := (&tls.Dialer{Config: tlsConfig}).DialContext(ctx, "tcp", "127.0.0.1:993")
	if err != nil {
		t.Fatal(err)
	}
	_ = rawConn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(rawConn)
	_, err = reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	for i, command := range []string{`LOGIN "alice@example.test" "` + password + `"`, `DELETE "Drafts"`, `CREATE "Drafts"`} {
		tag := fmt.Sprintf("R%d", i)
		if _, err = io.WriteString(rawConn, tag+" "+command+"\r\n"); err != nil {
			t.Fatal(err)
		}
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.HasPrefix(line, tag+" OK") {
				break
			}
			if strings.HasPrefix(line, tag+" ") {
				t.Fatalf("mailbox recreation failed: %s", line)
			}
		}
	}
	_ = rawConn.Close()
	sender, err = dialer.Dial(ctx, "alice@example.test", password)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close() }()
	validity, err := sender.FolderUIDValidity(ctx, "Drafts")
	if err != nil || validity == replaced.UIDValidity {
		t.Fatalf("mailbox recreation validity=%d previous=%d err=%v", validity, replaced.UIDValidity, err)
	}
	if _, err = sender.SaveDraft(ctx, "alice@example.test", outgoing, replaced.UID, replaced.UIDValidity); !errors.Is(err, webmail.ErrDraftChanged) {
		t.Fatalf("recreated mailbox accepted old draft identity: %v", err)
	}
	t.Logf("PASS real Drafts recreation changed UIDVALIDITY %d→%d; stale replacement blocked", replaced.UIDValidity, validity)
}

func verifyRuntimeDelivery(ctx context.Context, t *testing.T, client webmail.Client, subject string, payload []byte) {
	t.Helper()
	var result webmail.ListResult
	var err error
	for attempt := 0; attempt < 100; attempt++ {
		result, err = client.ListMessages(ctx, "INBOX", webmail.ListOpts{Query: "subject:" + subject + " from:alice@example.test text:marker"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Total > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if result.Total != 1 {
		t.Fatalf("expected one delivered message, got %d", result.Total)
	}
	message, err := client.GetMessage(ctx, "INBOX", result.Messages[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Bcc) != 0 || !strings.Contains(message.HTML, "native html marker") || !strings.Contains(message.Text, "native body marker") || len(message.Attachments) != 1 {
		t.Fatalf("delivered MIME mismatch: %+v", message)
	}
	_, _, download, err := client.GetAttachment(ctx, "INBOX", message.UID, message.Attachments[0].PartID)
	if err != nil || sha256.Sum256(download) != sha256.Sum256(payload) {
		t.Fatalf("delivered attachment mismatch: %v", err)
	}
}

func exerciseRuntimeFolders(ctx context.Context, t *testing.T, client webmail.Client, draft webmail.DraftSaveResult) {
	t.Helper()
	manager, ok := client.(webmail.FolderManager)
	if !ok {
		t.Fatal("real IMAP lacks folder management")
	}
	name := "项目 & 资料"
	if err := manager.CreateFolder(ctx, name); err != nil {
		t.Fatal(err)
	}
	folders, err := client.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, folder := range folders {
		if folder.Name == name {
			found = true
		}
	}
	if !found {
		t.Fatal("Unicode created folder not decoded from LIST")
	}
	copyResult, err := manager.TransferMessages(ctx, draft.Folder, []uint32{draft.UID}, name, false)
	if err != nil || len(copyResult.Results) != 1 || copyResult.Results[0].Status != "success" || copyResult.UIDValidity == 0 {
		t.Fatalf("real COPYUID result %+v %v", copyResult, err)
	}
	if _, err = client.GetMessage(ctx, draft.Folder, draft.UID); err != nil {
		t.Fatal("copy removed original")
	}
	uid := copyResult.Results[0].DestinationUID
	if _, err = client.GetMessage(ctx, name, uid); err != nil {
		t.Fatal(err)
	}
	renamed := "已改名"
	if err = manager.RenameFolder(ctx, name, renamed); err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetMessage(ctx, renamed, uid); err != nil {
		t.Fatal("rename lost message")
	}
	if err = manager.CreateFolder(ctx, "Recycle-test"); err != nil {
		t.Fatal(err)
	}
	moved, err := manager.TransferMessages(ctx, renamed, []uint32{uid}, "Recycle-test", true)
	if err != nil || moved.Results[0].Status != "success" {
		t.Fatalf("real MOVEUID %+v %v", moved, err)
	}
	if err = manager.DeleteFolder(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	restored, err := manager.TransferMessages(ctx, "Recycle-test", []uint32{moved.Results[0].DestinationUID}, "INBOX", true)
	if err != nil || restored.Results[0].Status != "success" {
		t.Fatalf("real restore %+v %v", restored, err)
	}
	if err = client.DeleteMessages(ctx, "INBOX", []uint32{restored.Results[0].DestinationUID}, true); err != nil {
		t.Fatal(err)
	}
	if err = manager.DeleteFolder(ctx, "Recycle-test"); err != nil {
		t.Fatal(err)
	}
	t.Log("PASS real Unicode folder create/rename/delete, UIDPLUS copy, recoverable move/restore and retained original")
}
