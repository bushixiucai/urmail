package agentd

// Opt-in daemon integration, not an end-to-end urmaild-agent/systemd/PG test.
// BIND signing, NSD reload, UDP/TCP queries and SIGKILL of an ApplyManager
// subprocess are real. Other services retain the existing fixture runner.
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

const liveDomain = "live-recovery.test"

// File-backed serial allocation preserves high-water state across the test
// subprocess crash. It is deliberately not a claim to test PostgreSQL locking.
type liveSerialStore struct{ path string }

func (s liveSerialStore) ReserveZoneSerial(_ context.Context, domain, _ string, seed uint32) (uint32, error) {
	values := map[string]uint32{}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		if err = json.Unmarshal(raw, &values); err != nil {
			return 0, err
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	n, ok := values[domain]
	if !ok {
		n = seed
	}
	n++
	values[domain] = n
	raw, err = json.Marshal(values)
	if err != nil {
		return 0, err
	}
	_, err = writeFileAtomic(s.path, raw, 0600, []string{filepath.Dir(s.path)})
	return n, err
}
func liveCommand(ctx context.Context, path string, args ...string) error {
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", path, args, err, out)
	}
	return nil
}
func liveManager(t *testing.T) *ApplyManager {
	t.Helper()
	m, _ := newRealTestManager(t, liveDomain)
	m.versions = liveSerialStore{filepath.Join(filepath.Dir(m.root), "serials.json")}
	raw, err := os.ReadFile(filepath.Join(m.root, "manifest.json"))
	if err == nil {
		var mf applyManifest
		if err = json.Unmarshal(raw, &mf); err != nil {
			t.Fatal(err)
		}
		m.status = mf.Status
		m.paths = mf.Paths
		m.domains = mf.Domains
		m.manifestFiles = mf.Files
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return m
}
func liveParams(mutated bool) []byte {
	value := "baseline"
	if mutated {
		value = "uncommitted"
	}
	p := ApplyParams{Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: liveDomain, Status: "active"}}}, DNS: nsd.Snapshot{Zones: []nsd.Zone{{Domain: liveDomain, Mode: "managed", Sign: true, Records: []nsd.Record{{Type: "TXT", Name: "canary", Value: value}}}}}}
	raw, _ := json.Marshal(p)
	return raw
}
func liveRunner(m *ApplyManager, conf string, start func() error) CommandRunner {
	base := realToolRunner(m, nil, nil)
	return func(ctx context.Context, path string, args ...string) error {
		if path == "/usr/bin/systemctl" && len(args) >= 2 && args[1] == "nsd" {
			if args[0] == "restart" || args[0] == "reload" {
				if start != nil {
					if err := start(); err != nil {
						return err
					}
				}
				return liveCommand(ctx, "/usr/sbin/nsd-control", "-c", conf, "reload", liveDomain)
			}
			if args[0] == "is-active" {
				return liveCommand(ctx, "/usr/sbin/nsd-control", "-c", conf, "status")
			}
			return fmt.Errorf("unexpected NSD systemctl operation %v", args)
		}
		return base(ctx, path, args...)
	}
}
func liveQuery(transport, name, rr string) (string, error) {
	args := []string{"@127.0.0.1", "-p", "1053", name, rr, "+dnssec", "+norecurse", "+time=1", "+tries=1"}
	if transport == "tcp" {
		args = append(args, "+tcp")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/dig", args...).CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}
func liveAssert(t *testing.T, m *ApplyManager, value string) uint64 {
	t.Helper()
	path := m.target("/etc/nsd/zones/" + liveDomain + ".signed")
	if err := verifySignedZoneWithRealTools(context.Background(), liveDomain, path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(m.target("/etc/nsd/zones/" + liveDomain + ".txt"))
	if err != nil {
		t.Fatal(err)
	}
	serial, err := readZoneSerial(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"udp", "tcp"} {
		deadline := time.Now().Add(10 * time.Second)
		var soa, txt string
		for {
			soa, err = liveQuery(transport, liveDomain, "SOA")
			var err2 error
			txt, err2 = liveQuery(transport, "canary."+liveDomain, "TXT")
			if err == nil && err2 == nil && strings.Contains(soa, "status: NOERROR") && strings.Contains(soa, "flags: qr aa;") && strings.Contains(soa, " "+serial+" ") && strings.Contains(soa, "RRSIG	SOA") && strings.Contains(txt, "status: NOERROR") && strings.Contains(txt, "flags: qr aa;") && strings.Contains(txt, "RRSIG	TXT") && strings.Contains(txt, strconv.Quote(value)) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("live %s query mismatch serial=%s value=%s: SOA=%s TXT=%s", transport, serial, value, soa, txt)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("REAL NSD %s :1053 serial=%s SOA+RRSIG and canary=%q TXT+RRSIG, AA NOERROR", transport, serial, value)
	}
	n, _ := strconv.ParseUint(serial, 10, 32)
	return n
}

// A separately exec'd test process runs the actual Apply/recover methods. Parent
// SIGKILLs it only after real NSD serves the uncommitted signed generation.
func TestLiveDNSSECProcessHelper(t *testing.T) {
	mode := os.Getenv("URMAIL_LIVE_HELPER")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	m := liveManager(t)
	m.run = liveRunner(m, os.Getenv("URMAIL_LIVE_CONF"), nil)
	if mode == "recover" {
		if err := m.recover(); err != nil {
			t.Fatal(err)
		}
		liveAssert(t, m, "baseline")
		return
	}
	if mode != "crash" {
		t.Fatal("unknown helper mode")
	}
	m.readinessProbe = func(context.Context) error {
		liveAssert(t, m, "uncommitted")
		fmt.Println("LIVE_PENDING_READY_FOR_SIGKILL")
		select {}
	}
	if _, _, err := m.Apply(liveParams(true)); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash helper unexpectedly returned")
}

func TestLiveDNSSECRollbackAndSIGKILLRecovery(t *testing.T) {
	if os.Getenv("URMAIL_LIVE_DNSSEC") != "1" {
		t.Skip("requires isolated container and URMAIL_LIVE_DNSSEC=1")
	}
	if os.Getenv("URMAIL_DNSSEC_FIXTURE_DIR") == "" {
		t.Setenv("URMAIL_DNSSEC_FIXTURE_DIR", t.TempDir())
	}
	m := liveManager(t)
	dir := filepath.Dir(m.root)
	conf := filepath.Join(dir, "live-nsd.conf")
	content := fmt.Sprintf(`server:
 ip-address: 127.0.0.1
 port: 1053
 username: ""
 chroot: ""
 zonesdir: ""
 database: ""
 pidfile: %s/nsd.pid
 logfile: %s/nsd.log
 zonelistfile: %s/zone.list
 xfrdfile: %s/xfrd.state
remote-control:
 control-enable: yes
 control-interface: %s/control.sock
zone:
 name: %s
 zonefile: %s
`, dir, dir, dir, dir, dir, liveDomain, m.target("/etc/nsd/zones/"+liveDomain+".signed"))
	if err := os.WriteFile(conf, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var daemon *exec.Cmd
	start := func() error {
		if daemon != nil {
			return nil
		}
		daemon = exec.CommandContext(t.Context(), "/usr/sbin/nsd", "-d", "-c", conf)
		log, err := os.Create(filepath.Join(dir, "daemon-stderr.log"))
		if err != nil {
			return err
		}
		daemon.Stdout = log
		daemon.Stderr = log
		if err = daemon.Start(); err != nil {
			log.Close()
			return err
		}
		t.Cleanup(func() { _ = daemon.Process.Kill(); _ = daemon.Wait(); _ = log.Close() })
		deadline := time.Now().Add(10 * time.Second)
		for {
			if err = liveCommand(context.Background(), "/usr/sbin/nsd-control", "-c", conf, "status"); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return err
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	m.run = liveRunner(m, conf, start)
	keys, err := m.prepareDNSKeys(context.Background(), liveDomain)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.saveDNSState(liveDomain, dnsKeyState{Current: keys, Phase: "prepared", PublishedAt: time.Now().UTC(), SigningEpoch: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.Apply(liveParams(false)); err != nil {
		t.Fatal(err)
	}
	baseline := liveAssert(t, m, "baseline")
	m.readinessProbe = func(context.Context) error {
		liveAssert(t, m, "uncommitted")
		return errors.New("injected post-reload readiness rejection")
	}
	if _, _, err = m.Apply(liveParams(true)); err == nil {
		t.Fatal("expected apply rejection")
	}
	rolled := liveAssert(t, m, "baseline")
	if rolled != baseline+2 {
		t.Fatalf("rollback serial got %d want %d", rolled, baseline+2)
	}
	assertNoPending := func() {
		if _, err := os.Stat(filepath.Join(m.root, "pending.json")); !os.IsNotExist(err) {
			t.Fatalf("pending journal remains: %v", err)
		}
	}
	assertNoPending()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childCtx, cancelChild := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancelChild()
	child := exec.CommandContext(childCtx, exe, "-test.run=^TestLiveDNSSECProcessHelper$", "-test.v", "-test.timeout=90s")
	child.Env = append(os.Environ(), "URMAIL_LIVE_HELPER=crash", "URMAIL_LIVE_CONF="+conf)
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	defer func() {
		if !killed {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Println(line)
			if line == "LIVE_PENDING_READY_FOR_SIGKILL" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child ended before crash checkpoint")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("crash checkpoint timed out")
	}
	pending, err := os.ReadFile(filepath.Join(m.root, "pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal applyJournal
	if err = json.Unmarshal(pending, &journal); err != nil {
		t.Fatal(err)
	}
	if !containsUnit(journal.Reloaded, "nsd") || len(journal.DNS) == 0 {
		t.Fatal("missing persisted NSD/DNS crash context")
	}
	liveAssert(t, m, "uncommitted")
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = child.Wait()
	killed = true
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("expected SIGKILL result, got %v", err)
	}
	t.Logf("ApplyManager subprocess SIGKILL confirmed: %v; persisted pending journal bytes=%d", err, len(pending))
	recoveryCtx, cancelRecovery := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancelRecovery()
	recovery := exec.CommandContext(recoveryCtx, exe, "-test.run=^TestLiveDNSSECProcessHelper$", "-test.v", "-test.timeout=90s")
	recovery.Env = append(os.Environ(), "URMAIL_LIVE_HELPER=recover", "URMAIL_LIVE_CONF="+conf)
	output, err := recovery.CombinedOutput()
	t.Logf("fresh recovery process:\n%s", output)
	if err != nil {
		t.Fatal(err)
	}
	recovered := liveAssert(t, m, "baseline")
	if recovered != rolled+2 {
		t.Fatalf("crash recovery serial got %d want %d", recovered, rolled+2)
	}
	assertNoPending()
	t.Log("SCOPE: real NSD/BIND and killed ApplyManager subprocess; NOT systemd, full agent/socket, PostgreSQL, or other daemon acceptance")
}
