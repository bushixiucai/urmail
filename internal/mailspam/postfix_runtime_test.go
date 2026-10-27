package mailspam

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

// TestRealPostfixMilterInterop delegates parsing of the wire actions and rate
// limits to distro Postfix. An imitation MTA cannot validate this contract.
func TestRealPostfixMilterInterop(t *testing.T) {
	image := os.Getenv("URMAIL_POSTFIX_TEST_IMAGE")
	if image == "" {
		t.Skip("URMAIL_POSTFIX_TEST_IMAGE is not configured")
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := staticHandler(VerdictResponse{InsertHeaders: VerdictHeaders(Verdict{Class: ClassHam, Score: 0.25})})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _ = serveConn(ctx, conn, MilterConfig{}.withDefaults(), handler) }()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	files, err := postfix.Generate(postfix.NodeConfig{Hostname: "box.example.test", PrimaryDomain: "example.test", StorageRoot: "/var/mail", TLSCertFile: "/tmp/cert", TLSKeyFile: "/tmp/key", PolicySocket: "private/policy", ArchiveSocket: "private/archive"}, postfix.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, data := range map[string]string{"generated-main.cf": files[postfix.FileMainCF], "checks": files[postfix.FileSpamHeaderChecks], "verify.py": realPostfixCheck} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, cleanup := range []string{"enabled", "disabled"} {
		t.Run(cleanup, func(t *testing.T) {
			runCtx, stop := context.WithTimeout(t.Context(), 90*time.Second)
			defer stop()
			command := exec.CommandContext(runCtx, "docker", "run", "--rm", "--name", "urmail-mailfix-postfix-"+cleanup, "-v", dir+":/test:ro", "-e", "MILTER_PORT="+strconv.Itoa(port), "-e", "HEADER_CLEANUP="+cleanup, image, "python3", "/test/verify.py")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("real Postfix rejected config/protocol: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "POSTFIX_MILTER_INTEROP_OK") {
				t.Fatalf("missing interop proof: %s", output)
			}
			t.Log(strings.TrimSpace(string(output)))
		})
	}
}

const realPostfixCheck = `import json, os, pathlib, smtplib, subprocess, time

def call(*args):
    return subprocess.check_output(args, stderr=subprocess.STDOUT, text=True)

wanted = ('anvil_rate_time_unit', 'smtpd_client_connection_count_limit',
          'smtpd_client_connection_rate_limit', 'smtpd_client_auth_rate_limit',
          'smtpd_client_message_rate_limit', 'smtpd_client_recipient_rate_limit',
          'smtpd_error_sleep_time', 'smtpd_soft_error_limit', 'smtpd_hard_error_limit')
for line in pathlib.Path('/test/generated-main.cf').read_text().splitlines():
    if line.split('=',1)[0].strip() in wanted:
        call('postconf','-e',line)
for setting in ('myhostname=box.example.test', 'inet_interfaces=loopback-only',
                'inet_protocols=ipv4', 'mydestination=localhost',
                'relay_domains=example.test', 'defer_transports=relay',
                'mynetworks=127.0.0.0/8', 'smtpd_tls_security_level=none',
                'smtpd_relay_restrictions=reject_unauth_destination',
                'smtpd_milters=inet:host.docker.internal:'+os.environ['MILTER_PORT'],
                'non_smtpd_milters=', 'milter_default_action=tempfail', 'milter_protocol=6',
                'maillog_file=/var/log/postfix.log', 'header_checks='+('regexp:/test/checks' if os.environ['HEADER_CLEANUP']=='enabled' else '')):
    call('postconf','-e',setting)
call('postconf','-M','smtp/inet=smtp inet n - n - - smtpd')
call('postconf','-M','cleanup/unix=cleanup unix n - n - 0 cleanup')
call('postfix','check')
subprocess.run(['postfix','start'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    client=None
    for attempt in range(60):
        try:
            client=smtplib.SMTP('127.0.0.1',25,timeout=4)
            break
        except (OSError,smtplib.SMTPException):
            time.sleep(.2)
    if client is None:
        raise RuntimeError('real smtpd failed to initialize generated integer budgets')
    message=('From: Sender <sender@outside.test>\r\nTo: alice@example.test\r\n'
             'Subject: interop\r\nX-UrMail-Spam: yes\r\nx-urmail-spam: yes\r\n'
             'X-UrMail-Spam-Score: 999\r\n\r\nbody\r\n')
    with client:
        client.sendmail('sender@outside.test',['alice@example.test'],message)
    queued=[]
    for _ in range(30):
        queued=[json.loads(line) for line in call('postqueue','-j').splitlines() if line.strip()]
        if queued:break
        time.sleep(.1)
    assert len(queued)==1,queued
    content=call('postcat','-bhq',queued[0]['queue_id'])
    lines=content.lower().splitlines()
    assert sum(line.startswith('x-urmail-spam:') for line in lines)==1,content
    assert 'x-urmail-spam: no' in lines,content
    assert 'x-urmail-spam-score: 0.25' in lines,content
    assert 'x-urmail-spam-score: 999' not in lines,content
    print('POSTFIX_MILTER_INTEROP_OK: real smtpd accepted generated budgets; 32-bit insertion and forged-header removal verified')
except Exception:
    print(pathlib.Path('/var/log/postfix.log').read_text() if pathlib.Path('/var/log/postfix.log').exists() else 'no postfix logfile', flush=True)
    raise
finally:
    subprocess.run(['postfix','stop'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
`
