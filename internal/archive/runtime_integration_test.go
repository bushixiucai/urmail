package archive

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

// TestArchiveSpoolPipeRuntime is a real Postfix pipe child inside the owned
// disposable fixture. It exercises production durable spooling with no product DB.
func TestArchiveSpoolPipeRuntime(t *testing.T) {
	if os.Getenv("URMAIL_ARCHIVE_PIPE_RUNTIME") != "1" {
		t.Skip("requires owned Postfix archive pipe fixture")
	}
	recipient, err := domainMail.DecodeArchiveRecipient(os.Getenv("URMAIL_ARCHIVE_RECIPIENT"))
	if err != nil || recipient.TenantID != 1 || recipient.MailboxID != 2 || recipient.Address != "bob@example.test" {
		t.Fatalf("pipe recipient identity mismatch: %+v %v", recipient, err)
	}
	manifest, err := Spool("/tmp/native-mail-runtime/archive-spool", recipient, os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("durable pipe copy size=%d digest=%s", manifest.Size, manifest.SHA256)
}

// TestArchivePostfixSizeRuntime confirms the accepted v80 SMTP size and actual
// internally-added BCC/Received bytes fit durable spooling. It runs after the
// regular native protocol/API tests in the same network-isolated fixture.
func TestArchivePostfixSizeRuntime(t *testing.T) {
	if os.Getenv("URMAIL_ARCHIVE_RUNTIME") != "1" {
		t.Skip("requires disposable Postfix/Dovecot runtime fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	command := func(binary string, args ...string) {
		t.Helper()
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture %s: %v %s", binary, err, output)
		}
	}
	const runtimeRoot = "/tmp/native-mail-runtime"
	if err := os.Mkdir(runtimeRoot+"/archive-spool", 0o700); err != nil {
		t.Fatal(err)
	}
	command("/usr/sbin/useradd", "--system", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", "urmail-archive-fixture")
	command("/usr/bin/chown", "urmail-archive-fixture:urmail-archive-fixture", runtimeRoot+"/archive-spool")
	recipient := domainMail.EncodeArchiveRecipient(1, 2, "bob@example.test")
	for path, value := range map[string]string{
		runtimeRoot + "/archive-bcc":       "bob@example.test " + recipient + "\n",
		runtimeRoot + "/archive-transport": "archive.invalid urmail-archive:\n",
	} {
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
		command("/usr/sbin/postmap", path)
	}
	master, err := os.OpenFile("/etc/postfix/master.cf", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(master, "\nurmail-archive unix - n n - 1 pipe\n  flags=Rq user=urmail-archive-fixture argv=/usr/bin/env URMAIL_ARCHIVE_PIPE_RUNTIME=1 URMAIL_ARCHIVE_RECIPIENT=${recipient} /tmp/archive-runtime.test -test.run=^TestArchiveSpoolPipeRuntime -test.timeout=2m\n")
	if err != nil {
		_ = master.Close()
		t.Fatal(err)
	}
	if err = master.Close(); err != nil {
		t.Fatal(err)
	}
	command("/usr/sbin/postconf", "-e", "message_size_limit=134217728", "recipient_bcc_maps=hash:"+runtimeRoot+"/archive-bcc", "transport_maps=hash:"+runtimeRoot+"/archive-transport")
	command("/usr/sbin/postfix", "reload")
	cert, err := os.ReadFile(runtimeRoot + "/cert.pem")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("fixture certificate unavailable")
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", "127.0.0.1:587")
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.SetDeadline(time.Now().Add(130 * time.Second)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	client, err := smtp.NewClient(conn, "runtime.example.test")
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.StartTLS(&tls.Config{RootCAs: roots, ServerName: "runtime.example.test", MinVersion: tls.VersionTLS12}); err != nil {
		t.Fatal(err)
	}
	if err = client.Auth(smtp.PlainAuth("", "alice@example.test", "isolated-fixture-password", "runtime.example.test")); err != nil {
		t.Fatal(err)
	}
	if ok, size := client.Extension("SIZE"); !ok || size != "134217728" {
		t.Fatalf("v80 SMTP SIZE not preserved: %t %q", ok, size)
	}
	if err = client.Mail("alice@example.test"); err != nil {
		t.Fatal(err)
	}
	if err = client.Rcpt("bob@example.test"); err != nil {
		t.Fatal(err)
	}
	data, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(data, "From: alice@example.test\r\nTo: bob@example.test\r\nSubject: near-v80-size-boundary\r\nMessage-ID: <archive-large-runtime@example.test>\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// Postfix limits the queue file, including record/envelope overhead.
	// net/smtp expands each LF into CRLF; budgeting 1000 bytes per 997-byte
	// logical line also reserves queue-record length bytes. Keep a further
	// 8 KiB for envelope/header records, rather than mislabeling body bytes
	// as the advertised SIZE/queue-file contract.
	const lines = ((128 << 20) - 8192) / 1000
	const bodySize = lines * 997
	const wireBodySize = lines * 998
	line := make([]byte, 997)
	for i := range line {
		line[i] = 'x'
	}
	line[996] = '\n'
	for i := 0; i < lines; i++ {
		if _, err = data.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err = data.Close(); err != nil {
		t.Fatalf("near-limit SMTP message not accepted: %v", err)
	}
	// Declared SIZE beyond the fixed contract must fail before receiving its body.
	if err = client.Text.PrintfLine("MAIL FROM:<alice@example.test> SIZE=134217729"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = client.Text.ReadResponse(552); err != nil {
		t.Fatalf("oversized SMTP declaration accepted: %v", err)
	}
	var manifest Manifest
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		entries, e := os.ReadDir(runtimeRoot + "/archive-spool")
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			if !validID(entry.Name()) {
				continue
			}
			encoded, e := os.ReadFile(filepath.Join(runtimeRoot, "archive-spool", entry.Name(), "manifest.json"))
			if e == nil && json.Unmarshal(encoded, &manifest) == nil {
				break
			}
		}
		if manifest.ID != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if manifest.ID == "" {
		t.Fatal("accepted SMTP message never formed a durable archive copy")
	}
	if manifest.Size <= bodySize || manifest.Size < 127<<20 || manifest.Size > MaxMessage {
		t.Fatalf("archive internal-header size mismatch: %d", manifest.Size)
	}
	file, err := os.Open(filepath.Join(runtimeRoot, "archive-spool", manifest.ID, "raw.eml"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest := sha256.New()
	copied, err := io.Copy(digest, file)
	if err != nil || copied != manifest.Size || hex.EncodeToString(digest.Sum(nil)) != manifest.SHA256 {
		t.Fatal("real pipe truncated/changed accepted raw")
	}
	t.Log("PASS near-128MiB queue budget real SMTP + BCC pipe + durable SHA256; logical_body=" + strconv.Itoa(bodySize) + " wire_body=" + strconv.Itoa(wireBodySize) + " raw_copy=" + strconv.FormatInt(copied, 10) + "; SIZE=134217728, oversized SIZE rejected")
}
