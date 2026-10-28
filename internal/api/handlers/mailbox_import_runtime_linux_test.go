package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/bushixiucai/urmail/internal/mailimport"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/webmail"
)

type importRuntimeDialer struct {
	inner   webmail.NetDialer
	mu      sync.Mutex
	loseACK bool
}

func (d *importRuntimeDialer) Dial(ctx context.Context, address, password string) (webmail.Client, error) {
	c, e := d.inner.Dial(ctx, address, password)
	if e != nil {
		return nil, e
	}
	return &importRuntimeClient{Client: c, owner: d}, nil
}

type importRuntimeClient struct {
	webmail.Client
	owner *importRuntimeDialer
}

func (c *importRuntimeClient) FindImportCopies(ctx context.Context, folder string, raw []byte, v uint32) ([]webmail.DraftSaveResult, error) {
	return c.Client.(webmail.ImportCopyFinder).FindImportCopies(ctx, folder, raw, v)
}
func (c *importRuntimeClient) GetRawMessage(ctx context.Context, folder string, uid uint32) ([]byte, uint32, error) {
	return c.Client.(webmail.RawMessageAccessor).GetRawMessage(ctx, folder, uid)
}
func (c *importRuntimeClient) ImportMessage(ctx context.Context, folder string, raw []byte) (webmail.DraftSaveResult, error) {
	result, e := c.Client.(webmail.MessageImporter).ImportMessage(ctx, folder, raw)
	c.owner.mu.Lock()
	lose := c.owner.loseACK
	c.owner.loseACK = false
	c.owner.mu.Unlock()
	if e == nil && lose {
		return webmail.DraftSaveResult{}, errors.New("injected loss after real Dovecot APPEND acceptance")
	}
	return result, e
}

// This opt-in fixture owns a separate Dovecot process and temporary Maildir.
func TestMailboxImportRealDovecotRuntime(t *testing.T) {
	if os.Getenv("URMAIL_IMPORT_DOVECOT_RUNTIME") != "1" {
		t.Skip("owned Dovecot runtime not requested")
	}
	if os.Geteuid() != 0 {
		t.Fatal("requires an owned root container")
	}
	f := newImportHTTPFixture(t)
	base := t.TempDir()
	for _, p := range []string{filepath.Dir(base), base} {
		if e := os.Chmod(p, 0755); e != nil {
			t.Fatal(e)
		}
	}
	home := filepath.Join(base, "homes", "alice@mail-import.test")
	if e := os.MkdirAll(home, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Chown(home, 8, 8); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	config := filepath.Join(base, "dovecot.conf")
	body := fmt.Sprintf(`base_dir = %s/run
log_path = %s/dovecot.log
protocols = imap
listen = 127.0.0.1
ssl = no
disable_plaintext_auth = no
first_valid_uid = 8
mail_location = maildir:%s/homes/%%u/Maildir
namespace inbox {
 inbox = yes
 separator = /
}
passdb {
 driver = static
 args = password=%s
}
userdb {
 driver = static
 args = uid=8 gid=8 home=%s/homes/%%u allow_all_users=yes
}
service imap-login {
 inet_listener imap {
  port = %d
 }
 inet_listener imaps {
  port = 0
 }
}
service stats {
 unix_listener stats-writer {
  mode = 0666
 }
}
`, base, base, base, f.password, base, port)
	if e = os.WriteFile(config, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	daemon := exec.CommandContext(t.Context(), "/usr/sbin/dovecot", "-F", "-c", config) // #nosec G204 -- fixed executable and owned fixture config.
	var logs bytes.Buffer
	daemon.Stdout = &logs
	daemon.Stderr = &logs
	if e = daemon.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = daemon.Process.Signal(syscall.SIGTERM); _ = daemon.Wait() })
	dialer := &importRuntimeDialer{inner: webmail.NetDialer{Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Timeout: 5 * time.Second}}
	var client webmail.Client
	deadline := time.Now().Add(10 * time.Second)
	for {
		client, e = dialer.Dial(t.Context(), "alice@mail-import.test", f.password)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Dovecot readiness: %v %s", e, logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	validity, e := client.FolderUIDValidity(t.Context(), "INBOX")
	_ = client.Close()
	if e != nil {
		t.Fatal(e)
	}
	f.native.Dialer = dialer
	raw := []byte("Message-ID: <real-dovecot-ack-lost@fixture.test>\r\nFrom: sender@fixture.test\r\nTo: alice@mail-import.test\r\nSubject: Runtime MIME attachment\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=runtime-boundary\r\n\r\n--runtime-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOriginal body with From and >From.\r\n--runtime-boundary\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=original.bin\r\n\r\nAAECA//+ECA=\r\n--runtime-boundary--\r\n")
	input := map[string]any{"format": "eml", "filename": "real.eml", "content_base64": base64.StdEncoding.EncodeToString(raw), "folder_enc": webmail.EncodeFolderName("INBOX"), "uid_validity": validity, "duplicate_policy": "skip"}
	status, out := f.call(t, "POST", "/imports/preview", f.token, input, "runtime-real-import-key")
	if status != 201 {
		t.Fatal("real preview", status, out)
	}
	job := importJobMap(t, out)
	dialer.mu.Lock()
	dialer.loseACK = true
	dialer.mu.Unlock()
	status, out = f.call(t, "POST", importJobPath(job, "/confirm"), f.token, importConfirmBody(job, f.password, 1), "")
	if status != 200 || importJobMap(t, out)["status"] != "uncertain" {
		t.Fatal("real accepted unknown", status, out)
	}
	uncertain := importJobMap(t, out)
	status, out = f.call(t, "POST", importJobPath(job, "/reconcile"), f.token, map[string]any{"version": uncertain["version"], "current_password": f.password}, "")
	if status != 200 || importJobMap(t, out)["status"] != "completed" {
		t.Fatal("real exact reconciliation", status, out)
	}
	final := importJobMap(t, out)
	uid := uint32(final["items"].([]any)[0].(map[string]any)["destination_uid"].(float64)) // #nosec G115 -- server checked uint32 UID.
	client, e = dialer.Dial(t.Context(), "alice@mail-import.test", f.password)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = client.Close() }()
	received, v, e := client.(webmail.RawMessageAccessor).GetRawMessage(t.Context(), "INBOX", uid)
	if e != nil || v != validity || !bytes.Equal(received, raw) {
		t.Fatal("real MIME octets changed", v, e)
	}
	matches, e := client.(webmail.ImportCopyFinder).FindImportCopies(t.Context(), "INBOX", raw, validity)
	if e != nil || len(matches) != 1 {
		t.Fatal("real accepted copy duplicated", matches, e)
	}
	status, out = f.call(t, "POST", "/imports/preview", f.token, input, "runtime-real-duplicate-key")
	if status != 201 || importJobMap(t, out)["items"].([]any)[0].(map[string]any)["status"] != "duplicate" {
		t.Fatal("real duplicate preview", status, out)
	}

	exportURL := f.server.URL + "/api/v1/mailbox/folders/" + url.PathEscape(webmail.EncodeFolderName("INBOX")) + "/export.mbox?uid_validity=" + strconv.FormatUint(uint64(validity), 10) + "&uids=" + strconv.FormatUint(uint64(uid), 10)
	request, e := http.NewRequestWithContext(t.Context(), "GET", exportURL, nil)
	if e != nil {
		t.Fatal(e)
	}
	request.Header.Set("Authorization", "Bearer "+f.token)
	response, e := f.server.Client().Do(request)
	if e != nil {
		t.Fatal(e)
	}
	exported, e := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if e != nil || response.StatusCode != 200 {
		t.Fatal("real MBOX export", response.StatusCode, e, string(exported))
	}
	roundtrip, e := mailimport.Parse("mbox", "export.mbox", exported)
	if e != nil || len(roundtrip.Items) != 1 || !bytes.Equal(roundtrip.Items[0].Raw, raw) {
		t.Fatal("MBOX framing changed original MIME attachment", e)
	}
	t.Log("Real PostgreSQL + native HTTP + Dovecot APPEND accepted/lost ACK -> exact SHA/UIDVALIDITY reconciliation, MIME byte equality and duplicate detection PASS")
}
