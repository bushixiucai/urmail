package sieve

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in check uses a local, already installed Linux runtime. It performs
// no network access and does not execute delivery or external autoreplies.
func TestStructuredRulesWithLinuxSieveCompiler(t *testing.T) {
	image := os.Getenv("URMAIL_SIEVE_TEST_IMAGE")
	if image == "" {
		t.Skip("URMAIL_SIEVE_TEST_IMAGE is not configured")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dovecot.conf"), []byte("mail_uid = 65534\nmail_gid = 65534\nmail_home = /tmp\nmail_location = maildir:/tmp/test-mail\nfirst_valid_uid = 1\nlog_path = /dev/stderr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rules := Rules{Rules: []Rule{{Header: "subject", Match: "contains", Value: "compile test", Action: "fileinto", Folder: "Reviewed"}}, Vacation: Vacation{Enabled: true, Subject: "Away", Message: "A local-only test.", Days: 7, From: now.Add(-time.Hour), Until: now.Add(time.Hour)}}
	script, err := Compile(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "script.sieve"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	messages := map[string]string{"normal": "From: sender@example.test\r\nTo: recipient@example.test\r\nSubject: hello\r\nMessage-ID: <normal@test>\r\n\r\nA message.\r\n", "list": "From: sender@example.test\r\nTo: recipient@example.test\r\nList-Id: <local-list.example.test>\r\nSubject: hello\r\nMessage-ID: <list@test>\r\n\r\nA list message.\r\n", "automated": "From: sender@example.test\r\nTo: recipient@example.test\r\nAuto-Submitted: auto-generated\r\nSubject: hello\r\nMessage-ID: <automated@test>\r\n\r\nAn automated message.\r\n"}
	for name, message := range messages {
		if err = os.WriteFile(filepath.Join(dir, name+".eml"), []byte(message), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "docker", append([]string{"run", "--rm", "--network", "none", "--entrypoint", "/bin/sh", "-v", dir + ":/test:ro", image, "-c"}, args...)...)
		output, runErr := command.CombinedOutput()
		if runErr != nil {
			t.Fatalf("Linux Sieve validation failed: %v\n%s", runErr, output)
		}
		return string(output)
	}
	run("sievec /test/script.sieve /tmp/script.svbin")
	for name := range messages {
		output := run("sieve-test -c /test/dovecot.conf -f sender@example.test -r recipient@example.test /test/script.sieve /test/" + name + ".eml")
		t.Logf("%s:\n%s", name, output)
		if name == "normal" && !strings.Contains(output, "send vacation message:") {
			t.Fatal("normal message did not exercise vacation")
		}
		if name != "normal" && strings.Contains(output, "send vacation message:") {
			t.Fatal("automated/list vacation was not suppressed")
		}
	}
}

func TestStructuredRulesWithIsolatedLMTP(t *testing.T) {
	image := os.Getenv("URMAIL_SIEVE_TEST_IMAGE")
	if image == "" {
		t.Skip("URMAIL_SIEVE_TEST_IMAGE is not configured")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	script, err := Compile(Rules{Rules: []Rule{{Header: "subject", Match: "contains", Value: "route locally", Action: "fileinto", Folder: "Reviewed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "script.sieve"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	runner := `#!/bin/bash
set -euo pipefail
mkdir -p /tmp/dovecot /tmp/home/recipient@example.test /tmp/mail/recipient@example.test/{cur,new,tmp}
chown -R 65534:65534 /tmp/home /tmp/mail
cp /test/script.sieve /tmp/script.sieve
cat > /tmp/dovecot.conf <<'CONFIG'
base_dir = /tmp/dovecot
protocols = lmtp
listen = 127.0.0.1
ssl = no
mail_uid = 65534
mail_gid = 65534
first_valid_uid = 1
mail_location = maildir:/tmp/mail/%u
log_path = /tmp/dovecot.log
passdb {
 driver = static
 args = password=local-test-only
}
userdb {
 driver = static
 args = uid=65534 gid=65534 home=/tmp/home/%u
}
service lmtp {
 unix_listener /tmp/urmail-lmtp {
  mode = 0600
 }
}
protocol lmtp {
 mail_plugins = sieve
}
plugin {
 sieve = file:/tmp/script.sieve
}
CONFIG
sievec -c /tmp/dovecot.conf /tmp/script.sieve /tmp/script.svbin
chmod 0644 /tmp/script.sieve /tmp/script.svbin
dovecot -c /tmp/dovecot.conf -F &
dovecot_pid=$!
trap 'kill "$dovecot_pid" 2>/dev/null || true' EXIT
python3 - <<'PY'
import socket,time,pathlib
for attempt in range(100):
 try:
  client=socket.socket(socket.AF_UNIX);client.connect('/tmp/urmail-lmtp');break
 except OSError:
  time.sleep(.05)
else: raise RuntimeError('LMTP did not start')
stream=client.makefile('rwb',buffering=0)
def response():
 lines=[]
 while True:
  line=stream.readline().decode();lines.append(line)
  if len(line)<4 or line[3]!='-':break
 return ''.join(lines)
def command(text,code):
 stream.write((text+'\r\n').encode());reply=response();print(reply.strip());assert reply.startswith(str(code)),reply
assert response().startswith('220')
command('LHLO local-test',250)
command('MAIL FROM:<sender@example.test>',250)
command('RCPT TO:<recipient@example.test>',250)
command('DATA',354)
stream.write(b'From: sender@example.test\r\nTo: recipient@example.test\r\nSubject: route locally\r\nMessage-ID: <urmail-sieve-lmtp@test>\r\n\r\nLocal delivery only.\r\n.\r\n')
reply=response();print(reply.strip());assert reply.startswith('250'),reply
command('QUIT',221)
files=list(pathlib.Path('/tmp/mail/recipient@example.test/.Reviewed/new').glob('*'))
assert len(files)==1,files
assert 'Local delivery only.' in files[0].read_text()
assert not list(pathlib.Path('/tmp/mail/recipient@example.test/new').glob('*'))
print('PASS: real isolated LMTP applied the structured fileinto rule')
PY
cat /tmp/dovecot.log
`
	if err = os.WriteFile(filepath.Join(dir, "run-lmtp.sh"), []byte(runner), 0644); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--network", "none", "--entrypoint", "/bin/bash", "-v", dir+":/test:ro", image, "/test/run-lmtp.sh")
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("isolated real LMTP failed: %v", err)
	}
}
