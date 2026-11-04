package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

// mockRealZoneVersionStore implements reconcile.ZoneVersionStore in memory for real tests.
type mockRealZoneVersionStore struct {
	serials map[string]uint32
}

func newMockRealZoneVersionStore() *mockRealZoneVersionStore {
	return &mockRealZoneVersionStore{serials: make(map[string]uint32)}
}

func (s *mockRealZoneVersionStore) ReserveZoneSerial(_ context.Context, domain, _ string, seed uint32) (uint32, error) {
	current := s.serials[domain]
	if current == 0 {
		current = seed
	}
	next := uint32((uint64(current) + 1) % 4294967296)
	s.serials[domain] = next
	return next, nil
}

func checkRealDNSSECTools(t *testing.T) {
	t.Helper()
	if os.Getenv("URMAIL_REAL_DNSSEC") != "1" {
		t.Skip("real BIND/NSD tools were not requested (URMAIL_REAL_DNSSEC=1 missing)")
	}
	for _, tool := range []string{
		"/usr/bin/dnssec-keygen",
		"/usr/bin/dnssec-signzone",
		"/usr/bin/dnssec-verify",
		"/usr/bin/dnssec-dsfromkey",
		"/usr/sbin/nsd-checkzone",
	} {
		if _, err := os.Stat(tool); err != nil {
			t.Fatalf("required real tool missing in environment: %s: %v", tool, err)
		}
	}
}

func newRealTestManager(t *testing.T, domain string) (*ApplyManager, *mockRealZoneVersionStore) {
	t.Helper()
	checkRealDNSSECTools(t)

	dir := os.Getenv("URMAIL_DNSSEC_FIXTURE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(dir, "state")
	systemDir := filepath.Join(dir, "system")
	acmeDir := filepath.Join(dir, "acme")

	for _, d := range []string{stateDir, systemDir, acmeDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		MyHostname:         "box." + domain,
		PrimaryDomain:      domain,
		StorageRoot:        filepath.Join(dir, "mail"),
		DatabaseURLDovecot: "host=127.0.0.1 user=dovecot password=secret",
		ListenAddr:         "127.0.0.1:8080",
		PublicIPv4:         "192.0.2.1",
		Nameservers:        []string{"ns1." + domain, "ns2." + domain},
	}

	mockStore := newMockRealZoneVersionStore()
	manager := &ApplyManager{
		root:           stateDir,
		targetRoot:     systemDir,
		webroot:        acmeDir,
		cfg:            cfg,
		versions:       mockStore,
		fileWriter:     writeFileAtomic,
		readinessProbe: func(context.Context) error { return nil },
		// Notice: manager.output is NIL so that manager.dnsOutput uses real exec.CommandContext!
	}

	return manager, mockStore
}

func realToolRunner(m *ApplyManager, shouldFailProbe *bool, reloadedUnits *[]string) CommandRunner {
	return func(ctx context.Context, path string, args ...string) error {
		// Disclaimer: systemctl is mocked by this fixture because systemd is not PID 1
		// in container/test environments. Real cryptographic signing and validation binaries
		// are fully executed.
		if path == "/usr/bin/systemctl" {
			if len(args) >= 2 && (args[0] == "restart" || args[0] == "reload") {
				if reloadedUnits != nil {
					*reloadedUnits = append(*reloadedUnits, args[1])
				}
				return nil
			}
			if len(args) >= 2 && args[0] == "is-active" {
				if shouldFailProbe != nil && *shouldFailProbe {
					return errors.New("injected probe failure to trigger rollback")
				}
				return nil
			}
			return nil
		}
		if path == "/usr/sbin/nsd-checkzone" {
			// Execute real nsd-checkzone against the zone file in targetRoot
			zoneFile := m.target(args[1])
			cmd := exec.CommandContext(ctx, "/usr/sbin/nsd-checkzone", args[0], zoneFile)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("real nsd-checkzone failed on %s: %w (%s)", zoneFile, err, string(out))
			}
			return nil
		}
		// Deterministic stub for sievec: only BIND/NSD DNS tools are executed against real binaries in this test suite.
		// Precisely validates fixed arguments from compileSpamScript and writes the exact requested destination.
		if path == "/usr/bin/sievec" {
			if len(args) != 6 || args[0] != "-P" || args[1] != "sieve_extprograms" || args[2] != "-x" || args[3] != "+vnd.dovecot.execute" {
				return fmt.Errorf("unexpected sievec arguments: %v", args)
			}
			sourcePath := m.target(args[4])
			destPath := m.target(args[5])
			source, err := os.ReadFile(sourcePath)
			if err != nil {
				return fmt.Errorf("read sieve source %s: %w", sourcePath, err)
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return err
			}
			return os.WriteFile(destPath, append([]byte("compiled sieve: "), source...), 0640)
		}
		// Deterministic stub for postmap: precisely generates the .db file for the exact target called.
		if path == "/usr/sbin/postmap" {
			if len(args) != 1 {
				return fmt.Errorf("unexpected postmap arguments: %v", args)
			}
			sourcePath := m.target(args[0])
			destPath := m.target(args[0] + ".db")
			if _, err := os.Stat(sourcePath); err != nil {
				return fmt.Errorf("postmap source missing %s: %w", sourcePath, err)
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return err
			}
			return os.WriteFile(destPath, []byte("compiled map"), 0644)
		}
		// opendkim, doveconf, postfix, nginx, nsd-checkconf configs are isolated in targetRoot
		return nil
	}
}

// verifySignedZoneWithRealTools asserts that the signed zone file exists, is valid DNS syntax (nsd-checkzone),
// and carries valid cryptographic signatures for all resource records (dnssec-verify).
func verifySignedZoneWithRealTools(ctx context.Context, domain, signedPath string) error {
	info, err := os.Stat(signedPath)
	if err != nil {
		return fmt.Errorf("signed zone file missing: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("signed zone file is empty")
	}

	// 1. Verify signatures with real dnssec-verify
	cmdVerify := exec.CommandContext(ctx, "/usr/bin/dnssec-verify", "-o", domain, signedPath)
	if out, err := cmdVerify.CombinedOutput(); err != nil {
		return fmt.Errorf("real dnssec-verify failed on %s: %w (output: %s)", signedPath, err, string(out))
	}

	// 2. Verify zone syntax and RRs with real nsd-checkzone
	cmdCheck := exec.CommandContext(ctx, "/usr/sbin/nsd-checkzone", domain, signedPath)
	if out, err := cmdCheck.CombinedOutput(); err != nil {
		return fmt.Errorf("real nsd-checkzone failed on %s: %w (output: %s)", signedPath, err, string(out))
	}

	return nil
}

func readZoneSerial(zoneContent []byte) (string, error) {
	re := regexp.MustCompile(`(?m)^\s*([0-9]+)\s*;\s*serial`)
	match := re.FindSubmatch(zoneContent)
	if len(match) != 2 {
		return "", fmt.Errorf("serial pattern not matched in zone content")
	}
	return string(match[1]), nil
}

// TestRealDNSSECJournalRollbackAdvancesSerialAndResigns tests that an apply failure at final probe
// initiates a complete rollback:
// 1. Restores the previous zone content.
// 2. Advances the SOA serial (RFC 1982 wrap-safe via ZoneVersionStore).
// 3. Executes real /usr/bin/dnssec-signzone, /usr/bin/dnssec-verify, and /usr/sbin/nsd-checkzone to re-sign.
// 4. Atomically replaces the .signed zone.
// 5. Triggers NSD reload.
// 6. Confirms the resulting .signed zone is cryptographically valid and carries the new serial.
func TestRealDNSSECJournalRollbackAdvancesSerialAndResigns(t *testing.T) {
	domain := "example.test"
	m, mockStore := newRealTestManager(t, domain)

	t.Log("Note: systemctl commands are intercepted by fixture mock; real BIND/NSD binaries are executed.")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 1. Prepare real keys using real /usr/bin/dnssec-keygen
	gen, err := m.prepareDNSKeys(ctx, domain)
	if err != nil {
		t.Fatalf("real prepareDNSKeys failed: %v", err)
	}
	if len(gen.Keys) != 2 {
		t.Fatalf("expected 2 keys generated, got %d", len(gen.Keys))
	}
	initialState := dnsKeyState{
		Current:      gen,
		Phase:        "prepared",
		PublishedAt:  time.Now().UTC(),
		SigningEpoch: time.Now().UTC(),
	}
	if err := m.saveDNSState(domain, initialState); err != nil {
		t.Fatalf("saveDNSState failed: %v", err)
	}

	// Verify DS record can be computed from real key
	ds, err := m.dnsDS(ctx, domain, gen)
	if err != nil || len(ds) == 0 {
		t.Fatalf("real dnsDS failed: %v, ds: %v", err, ds)
	}
	t.Logf("Generated real keys: %v, DS: %v", gen.Keys, ds)

	// 2. Perform initial successful Apply
	var reloaded []string
	failProbe := false
	m.run = realToolRunner(m, &failProbe, &reloaded)

	initialSerial := uint32(2026110101)
	mockStore.serials[domain] = initialSerial

	p := ApplyParams{
		Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: domain, Status: "active"}}},
		DNS:     nsd.Snapshot{Zones: []nsd.Zone{{Domain: domain, Mode: "managed", Sign: true}}},
	}
	raw1, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	gen1, _, err := m.Apply(raw1)
	if err != nil {
		t.Fatalf("initial real apply failed: %v", err)
	}
	if !m.status.Ready || gen1 == "" {
		t.Fatalf("expected ready generation, got status: %+v", m.status)
	}

	signedPath := m.target("/etc/nsd/zones/" + domain + ".signed")
	txtPath := m.target("/etc/nsd/zones/" + domain + ".txt")

	// Verify initial signed zone with real tools
	if err := verifySignedZoneWithRealTools(ctx, domain, signedPath); err != nil {
		t.Fatalf("initial signed zone verification failed: %v", err)
	}

	initialTxt, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatal(err)
	}
	initialSerialStr, err := readZoneSerial(initialTxt)
	if err != nil {
		t.Fatalf("failed to read initial serial: %v", err)
	}
	t.Logf("Initial Apply succeeded: serial=%s, gen=%s", initialSerialStr, gen1)

	initialSigned, err := os.ReadFile(signedPath)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Second apply mutates zone, stages and signs, but fails at final probe
	failProbe = true
	reloaded = nil
	p.DNS.Zones[0].Records = []nsd.Record{{Type: "TXT", Name: "canary", Value: "mutated-data-before-rollback"}}
	raw2, _ := json.Marshal(p)

	_, _, err = m.Apply(raw2)
	if err == nil {
		t.Fatal("expected second apply to fail due to injected probe failure")
	}

	// 4. Verify Rollback behavior with Real Tools:
	// a) Serial in txt zone must be strictly advanced
	restoredTxt, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredSerialStr, err := readZoneSerial(restoredTxt)
	if err != nil {
		t.Fatalf("failed to read restored serial: %v", err)
	}
	if restoredSerialStr == initialSerialStr {
		t.Fatalf("restored serial was not advanced: %s == %s", restoredSerialStr, initialSerialStr)
	}
	t.Logf("Restored serial strictly advanced from %s to %s", initialSerialStr, restoredSerialStr)

	// Canary record must NOT exist in the restored txt file
	if strings.Contains(string(restoredTxt), "canary") {
		t.Fatal("restored txt still contains mutated canary record!")
	}

	// b) Signed zone file must be re-signed and cryptographically valid
	if err := verifySignedZoneWithRealTools(ctx, domain, signedPath); err != nil {
		t.Fatalf("re-signed zone failed real cryptographic verification: %v", err)
	}

	restoredSigned, err := os.ReadFile(signedPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(initialSigned, restoredSigned) {
		t.Fatal("signed zone was not re-signed: identical to initial signature")
	}

	// c) Serial in re-signed zone must match the newly advanced serial
	signedSerialStr, err := readZoneSerial(restoredSigned)
	if err != nil {
		t.Fatalf("failed to read serial from signed zone: %v", err)
	}
	if signedSerialStr != restoredSerialStr {
		t.Fatalf("signed zone serial %s does not match restored txt serial %s", signedSerialStr, restoredSerialStr)
	}
	t.Logf("Re-signed zone verified with real tools; serial=%s matches restored txt", signedSerialStr)

	// d) NSD was reloaded
	nsdRestarted := false
	for _, u := range reloaded {
		if u == "nsd" {
			nsdRestarted = true
			break
		}
	}
	if !nsdRestarted {
		t.Fatalf("expected NSD restart in reloaded units: %v", reloaded)
	}

	// e) Journal pending.json was cleanly removed after successful rollback
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); !os.IsNotExist(err) {
		t.Fatal("pending.json still exists after successful rollback")
	}
}

// TestRealDNSSECInterruptedCrashRecovery tests recover() when a process crash leaves
// an uncommitted pending.json journal on disk.
func TestRealDNSSECInterruptedCrashRecovery(t *testing.T) {
	domain := "recovery.test"
	m, mockStore := newRealTestManager(t, domain)

	t.Log("Note: systemctl commands are intercepted by fixture mock; real BIND/NSD binaries are executed.")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Prepare real keys
	gen, err := m.prepareDNSKeys(ctx, domain)
	if err != nil {
		t.Fatalf("real prepareDNSKeys failed: %v", err)
	}
	state := dnsKeyState{Current: gen, Phase: "prepared", PublishedAt: time.Now().UTC()}
	if err := m.saveDNSState(domain, state); err != nil {
		t.Fatal(err)
	}

	// Initial apply to establish baseline
	var reloaded []string
	failProbe := false
	m.run = realToolRunner(m, &failProbe, &reloaded)
	mockStore.serials[domain] = 2026110201

	p := ApplyParams{
		Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: domain, Status: "active"}}},
		DNS:     nsd.Snapshot{Zones: []nsd.Zone{{Domain: domain, Mode: "managed", Sign: true}}},
	}
	raw, _ := json.Marshal(p)
	if _, _, err := m.Apply(raw); err != nil {
		t.Fatalf("baseline apply failed: %v", err)
	}

	txtPath := m.target("/etc/nsd/zones/" + domain + ".txt")
	signedPath := m.target("/etc/nsd/zones/" + domain + ".signed")

	oldTxtData, _ := os.ReadFile(txtPath)
	oldSignedData, _ := os.ReadFile(signedPath)
	oldTxtSerial, _ := readZoneSerial(oldTxtData)

	// Simulate crash during an interrupted apply:
	// A valid journal Version: 2 is written, then files on disk are corrupted/partially written.
	journal := applyJournal{
		Version: ApplyJournalVersion,
		Files:   map[string]savedFile{txtPath: {Content: oldTxtData, Mode: 0644, Exists: true}},
		DNS: map[string]DNSRollbackItem{
			domain: {
				Domain:        domain,
				OldSigned:     savedFile{Content: oldSignedData, Mode: 0644, Exists: true},
				OldTxt:        savedFile{Content: oldTxtData, Mode: 0644, Exists: true},
				StateSnapshot: &state,
				SignMode:      true,
				ZoneMode:      "managed",
			},
		},
		Previous:        m.status,
		PreviousPaths:   m.paths,
		PreviousDomains: m.domains,
		Reloaded:        []string{"nsd"},
	}
	if err := m.saveJSON("pending.json", journal); err != nil {
		t.Fatal(err)
	}

	// Corrupt live txt file to simulate partial write
	if err := os.WriteFile(txtPath, []byte("; corrupted partial zone content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Now execute recover() as would happen on daemon restart
	reloaded = nil
	if err := m.recover(); err != nil {
		t.Fatalf("real recover() failed: %v", err)
	}

	// Assertions:
	// 1. Txt zone was restored and serial advanced
	recoveredTxt, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatal(err)
	}
	recoveredSerial, err := readZoneSerial(recoveredTxt)
	if err != nil {
		t.Fatalf("read serial after recovery failed: %v", err)
	}
	if recoveredSerial == oldTxtSerial {
		t.Fatalf("serial was not advanced during recovery: %s == %s", recoveredSerial, oldTxtSerial)
	}
	t.Logf("Interrupted crash recovery advanced serial from %s to %s", oldTxtSerial, recoveredSerial)

	// 2. Signed zone was re-signed with real tools and passes full verification
	if err := verifySignedZoneWithRealTools(ctx, domain, signedPath); err != nil {
		t.Fatalf("recovered signed zone failed real verification: %v", err)
	}

	recoveredSigned, err := os.ReadFile(signedPath)
	if err != nil {
		t.Fatal(err)
	}
	signedSerial, err := readZoneSerial(recoveredSigned)
	if err != nil {
		t.Fatalf("read serial from recovered signed zone: %v", err)
	}
	if signedSerial != recoveredSerial {
		t.Fatalf("signed serial %s does not match recovered txt serial %s", signedSerial, recoveredSerial)
	}

	// 3. pending.json was cleaned up
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); !os.IsNotExist(err) {
		t.Fatal("pending.json was not cleaned up after recovery")
	}
}

// TestRealDNSSECRollbackResignFailureDegrades tests that if real re-signing fails during
// rollback, the system sets Degraded=true, Ready=false, and retains pending.json on disk.
func TestRealDNSSECRollbackResignFailureDegrades(t *testing.T) {
	domain := "resignfail.test"
	m, mockStore := newRealTestManager(t, domain)

	t.Log("Note: systemctl commands are intercepted by fixture mock; real BIND/NSD binaries are executed.")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	gen, err := m.prepareDNSKeys(ctx, domain)
	if err != nil {
		t.Fatal(err)
	}
	state := dnsKeyState{Current: gen, Phase: "prepared", PublishedAt: time.Now().UTC()}
	if err := m.saveDNSState(domain, state); err != nil {
		t.Fatal(err)
	}

	failProbe := false
	m.run = realToolRunner(m, &failProbe, nil)
	mockStore.serials[domain] = 2026110301

	p := ApplyParams{
		Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: domain, Status: "active"}}},
		DNS:     nsd.Snapshot{Zones: []nsd.Zone{{Domain: domain, Mode: "managed", Sign: true}}},
	}
	raw, _ := json.Marshal(p)
	if _, _, err := m.Apply(raw); err != nil {
		t.Fatal(err)
	}

	// In the second apply, fail at probe to trigger rollback.
	// But delete the private key directory before rollback so that real re-signing fails!
	failProbe = true
	p.DNS.Zones[0].Records = []nsd.Record{{Type: "TXT", Name: "fail", Value: "trigger"}}
	raw2, _ := json.Marshal(p)

	// Intercept command runner to sabotage the key directory right when probe fails
	origRunner := m.run
	m.run = func(ctx context.Context, path string, args ...string) error {
		if path == "/usr/bin/systemctl" && len(args) >= 2 && args[0] == "is-active" {
			// Wipe keys to cause dnssec-signzone failure during rollback
			_ = os.RemoveAll(m.dnsDirectory(domain))
			return errors.New("injected probe failure")
		}
		return origRunner(ctx, path, args...)
	}

	_, _, err = m.Apply(raw2)
	if err == nil {
		t.Fatal("expected apply to fail")
	}

	// Status must reflect failure and degradation
	if !m.status.Degraded || m.status.Ready {
		t.Fatalf("expected Degraded=true, Ready=false; got status: %+v", m.status)
	}
	if !strings.Contains(m.status.Error, "rollback failed") {
		t.Fatalf("expected rollback failure in status.Error: %s", m.status.Error)
	}

	// pending.json MUST be retained on disk
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); os.IsNotExist(err) {
		t.Fatal("pending.json was deleted despite rollback failure!")
	}

	// Subsequent apply must be blocked by pending journal
	if _, _, err := m.Apply(raw); err == nil {
		t.Fatal("subsequent apply was accepted despite pending uncommitted journal")
	}
	t.Log("Rollback re-sign failure correctly degraded system, preserved journal, and blocked subsequent apply.")
}
