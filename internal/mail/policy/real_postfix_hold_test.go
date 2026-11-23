package policy_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/policy"
)

// realPostfixNetwork drives two real Postfix instances: a sender with its own
// queue and a receiving MX running the product policy server. The test proves
// the upgrade hold answers SMTP with the agreed 4xx, that the sender retains
// the message, and that after release the same sample is delivered exactly
// once. A connection failure is recorded as a distinct outcome, never as a
// 4xx reply. It is an isolated local fixture: no external mail, DNS, or host
// Postfix is touched, and it skips unless URMAIL_POSTFIX_TEST_IMAGE names a
// local image that contains Postfix.
func TestRealPostfixHoldDeferAndQueueReplay(t *testing.T) {
	image := os.Getenv("URMAIL_POSTFIX_TEST_IMAGE")
	if image == "" {
		t.Skip("URMAIL_POSTFIX_TEST_IMAGE is not configured")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is not available")
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	tag := hex.EncodeToString(suffix[:])
	network, mxName, txName := "p3hold-"+tag, "p3mx-"+tag, "p3tx-"+tag
	evidenceDir := os.Getenv("URMAIL_P3_EVIDENCE_DIR")
	dir := t.TempDir()
	holdPath := filepath.Join(dir, "upgrade.hold")
	socketDir := filepath.Join(dir, "policy")
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDir, "policy.sock")

	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	dockerQuiet := func(args ...string) {
		t.Helper()
		_ = exec.Command("docker", args...).Run()
	}
	dockerQuiet("network", "create", network)
	for _, name := range []string{mxName, txName} {
		dockerQuiet("rm", "-f", name)
	}
	t.Cleanup(func() {
		for _, name := range []string{mxName, txName} {
			dockerQuiet("rm", "-f", name)
		}
		dockerQuiet("network", "rm", network)
	})
	mxIP := func() string {
		t.Helper()
		out := docker("inspect", mxName, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
		return strings.TrimSpace(out)
	}
	container := func(name string, script string) string {
		t.Helper()
		return docker("exec", name, "bash", "-lc", script)
	}
	startPostfix := func(name, config string) {
		t.Helper()
		container(name, "postconf -e "+config+" maillog_file=/var/log/postfix.log; postconf -M smtp/inet=\"smtp inet n - n - - smtpd\"; postconf -F smtp/unix/chroot=n; postfix start")
	}
	waitPort := func(addr string, deadline time.Duration) bool {
		end := time.Now().Add(deadline)
		for time.Now().Before(end) {
			conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}
	waitFile := func(pattern string, deadline time.Duration) bool {
		end := time.Now().Add(deadline)
		for time.Now().Before(end) {
			matches, _ := filepath.Glob(pattern)
			if len(matches) > 0 {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}
	writeHold := func() {
		t.Helper()
		if err := os.WriteFile(holdPath, []byte(`{"kind":"test-upgrade-hold"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clearHold := func() {
		t.Helper()
		if err := os.Remove(holdPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	svc := &policy.Service{Hold: policy.HoldGate{Path: holdPath}}
	server := policy.NewServer(socketPath, svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	t.Cleanup(func() { _ = server.Close() })
	if !waitFile(socketPath, 3*time.Second) {
		t.Fatal("policy socket did not appear")
	}
	// The smtpd worker runs as the container's postfix user; this isolated
	// fixture makes the product socket reachable across the bind mount.
	if err := os.Chmod(socketPath, 0o666); err != nil {
		t.Fatal(err)
	}
	writeHold()

	docker("run", "-d", "--name", mxName, "--network", network,
		"-v", socketDir+":/run/urmail-policy", image, "sleep", "infinity")
	docker("run", "-d", "--name", txName, "--network", network, image, "sleep", "infinity")

	container(mxName, "useradd -m -s /bin/bash sink || true")
	startPostfix(mxName, "myhostname=mx.example.test mydestination=example.test,localhost inet_interfaces=all inet_protocols=ipv4 mynetworks=127.0.0.0/8,172.16.0.0/12 smtpd_recipient_restrictions=\"check_policy_service unix:/run/urmail-policy/policy.sock\" smtpd_relay_restrictions=\"permit_mynetworks,reject_unauth_destination\" local_recipient_maps= home_mailbox=Maildir/ luser_relay=sink@localhost")
	startPostfix(txName, "myhostname=tx.example.test relayhost=["+mxName+"]:25 inet_interfaces=loopback-only inet_protocols=ipv4 minimal_backoff_time=2s maximal_backoff_time=2s queue_run_delay=2s")
	container(txName, "echo tx.example.test > /etc/mailname; postfix reload")
	if ip := mxIP(); ip == "" || !waitPort(ip+":25", 20*time.Second) {
		t.Fatal("MX smtpd did not start listening")
	}

	inject := func(id string) {
		t.Helper()
		container(txName, fmt.Sprintf(`printf "From: a@example.test\r\nTo: alice@example.test\r\nMessage-ID: <%s@example.test>\r\nSubject: p3 sample\r\n\r\nhold window sample\r\n" | sendmail -f a@example.test alice@example.test`, id))
	}
	queueLine := regexp.MustCompile(`^([0-9A-Fa-f]{6,})[[:space:]]+[0-9]+[[:space:]]`)
	queueIDs := func() []string {
		out := container(txName, "postqueue -p")
		var ids []string
		for _, line := range strings.Split(out, "\n") {
			if m := queueLine.FindStringSubmatch(line); m != nil {
				ids = append(ids, m[1])
			}
		}
		return ids
	}
	waitFor := func(what string, deadline time.Duration, cond func() bool) {
		t.Helper()
		end := time.Now().Add(deadline)
		for time.Now().Before(end) {
			if cond() {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s", what)
	}
	txLog := func() string { return container(txName, "cat /var/log/postfix.log") }
	mxLog := func() string { return container(mxName, "cat /var/log/postfix.log") }
	sinkCount := func() int {
		out := container(mxName, "find /home/sink/Maildir -type f 2>/dev/null | wc -l")
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d", &n)
		return n
	}

	// Phase 1: hold active. The sender's first attempt must receive the
	// agreed 4xx and retain the message; the MX target stays empty.
	inject("p3-sample-held")
	waitFor("held sample queued", 20*time.Second, func() bool { return len(queueIDs()) == 1 })
	heldID := queueIDs()[0]
	waitFor("4.7.1 deferral in sender log", 30*time.Second, func() bool {
		return strings.Contains(txLog(), heldID) && strings.Contains(txLog(), "450 4.7.1")
	})
	heldLog := txLog()
	if strings.Contains(heldLog, heldID+": to=<alice@example.test>") && strings.Contains(heldLog, "status=sent") {
		t.Fatalf("held sample was accepted:\n%s", heldLog)
	}
	if got := sinkCount(); got != 0 {
		t.Fatalf("held window delivered %d file(s)", got)
	}
	t.Logf("HELD_DEFERRAL %s", firstLineContaining(heldLog, "450 4.7.1"))

	// Phase 2: release. The same queued sample must deliver exactly once.
	clearHold()
	container(txName, "postqueue -f")
	waitFor("queue drained", 45*time.Second, func() bool { return len(queueIDs()) == 0 })
	waitFor("single delivery", 30*time.Second, func() bool { return sinkCount() == 1 })
	sentLine := firstLineContaining(txLog(), "status=sent")
	if !strings.Contains(sentLine, heldID) {
		t.Fatalf("unexpected delivery log: %s", sentLine)
	}
	container(txName, "postqueue -f")
	time.Sleep(3 * time.Second)
	if got := sinkCount(); got != 1 {
		t.Fatalf("replay delivered %d times", got)
	}
	// Independent readback: the one delivered file must carry the sample's
	// own Message-ID, so exactly-once is observed rather than inferred.
	readback := container(mxName, "grep -l 'p3-sample-held@example.test' /home/sink/Maildir/new/* 2>/dev/null | wc -l")
	if strings.TrimSpace(readback) != "1" {
		t.Fatalf("delivered sample not readable back: %q", readback)
	}
	t.Logf("REPLAY_DELIVERY %s", sentLine)
	t.Logf("REPLAY_READBACK files=%s message-id=p3-sample-held@example.test", strings.TrimSpace(readback))
	replaySinkCount := sinkCount()

	// Phase 3: a connection failure is not a 4xx reply. With the MX stopped
	// the sender records a connection error and keeps the message, but no
	// SMTP reply exists for that attempt.
	writeHold()
	docker("stop", mxName)
	inject("p3-sample-disconnect")
	waitFor("disconnect sample queued", 20*time.Second, func() bool { return len(queueIDs()) == 1 })
	disconnectID := queueIDs()[0]
	waitFor("connection failure in sender log", 30*time.Second, func() bool {
		log := txLog()
		return strings.Contains(log, disconnectID) && strings.Contains(log, "status=deferred")
	})
	disconnectLog := txLog()
	attempt := ""
	for _, line := range strings.Split(disconnectLog, "\n") {
		if strings.Contains(line, disconnectID) && strings.Contains(line, "status=deferred") {
			attempt = strings.TrimSpace(line)
			break
		}
	}
	if attempt == "" {
		t.Fatalf("no deferred attempt recorded for %s", disconnectID)
	}
	if strings.Contains(attempt, "said: 450") || strings.Contains(attempt, "4.7.1") || strings.Contains(attempt, "said: 4") {
		t.Fatalf("disconnect was recorded as a policy 4xx: %s", attempt)
	}
	t.Logf("DISCONNECT_ATTEMPT %s", attempt)

	// Drain the disconnect sample after the MX returns; the hold still
	// defers it with a real 4xx, so release is needed for delivery.
	clearHold()
	docker("start", mxName)
	container(mxName, "postfix start")
	if !waitPort(mxIP()+":25", 20*time.Second) {
		t.Fatal("MX smtpd did not restart")
	}
	container(txName, "postqueue -f")
	waitFor("disconnect sample drained", 45*time.Second, func() bool { return len(queueIDs()) == 0 })
	// The disconnect sample is a second, distinct message; each sample is
	// delivered once, which is all this isolated fixture claims.
	waitFor("second sample delivered once", 30*time.Second, func() bool { return sinkCount() == 2 })

	if evidenceDir != "" {
		if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"sender.log": txLog(), "mx.log": mxLog()} {
			if err := os.WriteFile(filepath.Join(evidenceDir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		summary := map[string]any{
			"held_queue_id": heldID, "held_reply": firstLineContaining(heldLog, "450 4.7.1"),
			"replay": sentLine, "readback_files": strings.TrimSpace(readback), "sink_files_after_replay": replaySinkCount, "sink_files_final": sinkCount(), "disconnect_attempt": attempt,
			"message_id_dedup": "not used; each isolated sample delivered once, no global exactly-once claim",
		}
		raw, _ := json.MarshalIndent(summary, "", "  ")
		if err := os.WriteFile(filepath.Join(evidenceDir, "summary.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func firstLineContaining(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
