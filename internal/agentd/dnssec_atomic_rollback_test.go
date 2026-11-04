package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/nsd"
	"github.com/bushixiucai/urmail/internal/mail/postfix"
)

// mockZoneVersionStore implements reconcile.ZoneVersionStore in memory for tests.
type mockZoneVersionStore struct {
	serials map[string]uint32
}

func newMockZoneVersionStore() *mockZoneVersionStore {
	return &mockZoneVersionStore{serials: make(map[string]uint32)}
}

func (s *mockZoneVersionStore) ReserveZoneSerial(_ context.Context, domain, _ string, seed uint32) (uint32, error) {
	current := s.serials[domain]
	if current == 0 {
		current = seed
	}
	next := uint32((uint64(current) + 1) % 4294967296)
	s.serials[domain] = next
	return next, nil
}

// helper to prepare mock DNS keys and state for a domain
func setupMockDNSState(t *testing.T, m *ApplyManager, domain string, phase string) dnsKeyState {
	t.Helper()
	dir := m.dnsDirectory(domain)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ksk := "K" + domain + ".+013+11111"
	zsk := "K" + domain + ".+013+22222"
	for _, key := range []string{ksk, zsk} {
		for _, suffix := range []string{".key", ".private"} {
			content := "mock key content"
			if suffix == ".key" && key == ksk {
				content = domain + ". IN DNSKEY 257 3 13 mock-public-key"
			}
			if err := os.WriteFile(filepath.Join(dir, key+suffix), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	state := dnsKeyState{
		Current:      dnsKeyGeneration{Keys: []string{ksk, zsk}, CreatedAt: time.Now().UTC()},
		Phase:        phase,
		PublishedAt:  time.Now().UTC().Add(-24 * time.Hour),
		SigningEpoch: time.Now().UTC().Add(-24 * time.Hour),
	}
	if err := m.saveDNSState(domain, state); err != nil {
		t.Fatal(err)
	}
	return state
}

func setupMockZoneFiles(t *testing.T, m *ApplyManager, domain string, serial uint32, signedContent string) {
	t.Helper()
	zoneDir := m.target("/etc/nsd/zones")
	if err := os.MkdirAll(zoneDir, 0755); err != nil {
		t.Fatal(err)
	}
	txt := fmt.Sprintf(`$ORIGIN %s.
$TTL 86400
@ IN SOA ns1.%s. hostmaster.%s. (
    %d ; serial
    3600
    1800
    1209600
    300
)
@ IN NS ns1.%s.
`, domain, domain, domain, serial, domain)
	if err := os.WriteFile(m.target("/etc/nsd/zones/"+domain+".txt"), []byte(txt), 0644); err != nil {
		t.Fatal(err)
	}
	if signedContent != "" {
		if err := os.WriteFile(m.target("/etc/nsd/zones/"+domain+".signed"), []byte(signedContent), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// Test 1: Journal version 2 format and DNS typed rollback items
func TestJournalVersion2UpgradeAndRollbackItems(t *testing.T) {
	m := testManager(t)
	domain := "example.com"
	setupMockDNSState(t, m, domain, "signed")
	setupMockZoneFiles(t, m, domain, 2026102801, "initial valid signed content")

	p := ApplyParams{
		Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: domain, Status: "active"}}},
		DNS:     nsd.Snapshot{Zones: []nsd.Zone{{Domain: domain, Mode: "managed", Sign: true}}},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	// Intercept write to inspect pending.json
	journalCaptured := false
	m.fileWriter = func(path string, raw []byte, mode os.FileMode, roots []string) (string, error) {
		if !journalCaptured {
			journalRaw, err := os.ReadFile(filepath.Join(m.root, "pending.json"))
			if err == nil {
				var j applyJournal
				if err := json.Unmarshal(journalRaw, &j); err != nil {
					t.Fatalf("failed to unmarshal journal: %v", err)
				}
				if j.Version != 2 {
					t.Fatalf("expected journal version 2, got %d", j.Version)
				}
				dnsItem, ok := j.DNS[domain]
				if !ok {
					t.Fatalf("expected DNS rollback item for %s", domain)
				}
				if !dnsItem.OldSigned.Exists || string(dnsItem.OldSigned.Content) != "initial valid signed content" {
					t.Fatalf("unexpected OldSigned: %+v", dnsItem.OldSigned)
				}
				if !dnsItem.OldTxt.Exists || !strings.Contains(string(dnsItem.OldTxt.Content), "2026102801 ; serial") {
					t.Fatalf("unexpected OldTxt: %+v", dnsItem.OldTxt)
				}
				if dnsItem.StateSnapshot == nil || dnsItem.StateSnapshot.Phase != "signed" {
					t.Fatalf("unexpected StateSnapshot: %+v", dnsItem.StateSnapshot)
				}
				if !dnsItem.SignMode {
					t.Fatal("expected SignMode true")
				}
				journalCaptured = true
			}
		}
		return writeFileAtomic(path, raw, mode, roots)
	}

	// Mock dnsOutput to simulate signing
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		cmd := args[0]
		if strings.HasSuffix(cmd, "dnssec-signzone") {
			// Find -f flag
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-f" {
					_ = os.WriteFile(args[i+1], []byte("newly signed content"), 0644)
					break
				}
			}
			return []byte("signed zone"), nil
		}
		return []byte("ok"), nil
	}

	if _, _, err := m.Apply(raw); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !journalCaptured {
		t.Fatal("pending.json was not captured")
	}
}

// Test 2: Legacy Version 1 journal with DNS zones fails closed and blocks apply
func TestLegacyVersion1JournalWithDNSFailsClosed(t *testing.T) {
	m := testManager(t)
	domain := "example.com"
	txtPath := "/etc/nsd/zones/" + domain + ".txt"

	// Create legacy v1 journal (Version: 1, no DNS field) containing DNS files
	journal := applyJournal{
		Version:         1,
		Files:           map[string]savedFile{txtPath: {Content: []byte("legacy txt content"), Mode: 0644, Exists: true}},
		Previous:        ApplyStatus{Applied: "prev-gen"},
		PreviousPaths:   []string{txtPath},
		PreviousDomains: []string{domain},
	}
	if err := m.saveJSON("pending.json", journal); err != nil {
		t.Fatal(err)
	}

	// recover() must fail closed
	err := m.recover()
	if err == nil {
		t.Fatal("expected recover() to fail on legacy v1 journal with DNS zones")
	}
	if !m.status.Degraded || m.status.Ready {
		t.Fatalf("expected Degraded=true, Ready=false; got status: %+v", m.status)
	}

	// pending.json must NOT be erased
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); os.IsNotExist(err) {
		t.Fatal("pending.json was silently deleted!")
	}

	// Calling Apply() must be blocked
	if _, _, err := m.Apply(emptyApply()); err == nil {
		t.Fatal("expected Apply() to be blocked by existing uncommitted pending.json")
	}
}

// Test 3: Legacy Version 1 journal without DNS zones can recover
func TestLegacyVersion1JournalWithoutDNSRecovers(t *testing.T) {
	m := testManager(t)
	path := "/etc/postfix/main.cf"
	if err := m.write(path, []byte("good old config"), 0644); err != nil {
		t.Fatal(err)
	}
	old, err := saveFile(m.target(path))
	if err != nil {
		t.Fatal(err)
	}

	journal := applyJournal{
		Version:         1,
		Files:           map[string]savedFile{path: old},
		Previous:        ApplyStatus{Applied: "prev-gen"},
		PreviousPaths:   []string{path},
		PreviousDomains: []string{"example.com"},
	}
	if err := m.saveJSON("pending.json", journal); err != nil {
		t.Fatal(err)
	}
	// Write interrupted file
	if err := m.write(path, []byte("broken config"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := m.recover(); err != nil {
		t.Fatalf("expected non-DNS legacy journal to recover: %v", err)
	}
	raw, err := os.ReadFile(m.target(path))
	if err != nil || string(raw) != "good old config" {
		t.Fatalf("file not restored: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); !os.IsNotExist(err) {
		t.Fatal("pending.json should be cleaned up on successful recovery")
	}
}

// Test 4: Staging mode atomicity in signDNSZone
func TestDNSSECSigningStagingModeAtomicity(t *testing.T) {
	m := testManager(t)
	domain := "example.com"
	setupMockDNSState(t, m, domain, "prepared")
	setupMockZoneFiles(t, m, domain, 2026102801, "existing valid signature")

	signedPath := m.target("/etc/nsd/zones/" + domain + ".signed")
	ctx := context.Background()

	// Case A: dnssec-signzone fails
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "dnssec-signzone") {
			return nil, errors.New("injected signer fault")
		}
		return []byte("ok"), nil
	}
	if err := m.signDNSZone(ctx, domain); err == nil {
		t.Fatal("expected signing to fail")
	}
	raw, err := os.ReadFile(signedPath)
	if err != nil || string(raw) != "existing valid signature" {
		t.Fatal("live .signed was corrupted or changed upon signer failure")
	}

	// Case B: dnssec-verify fails
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "dnssec-signzone") {
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-f" {
					_ = os.WriteFile(args[i+1], []byte("bad signature"), 0644)
					break
				}
			}
			return []byte("signed"), nil
		}
		if strings.HasSuffix(args[0], "dnssec-verify") {
			return nil, errors.New("injected verify fault")
		}
		return []byte("ok"), nil
	}
	if err := m.signDNSZone(ctx, domain); err == nil {
		t.Fatal("expected verify to fail")
	}
	raw, err = os.ReadFile(signedPath)
	if err != nil || string(raw) != "existing valid signature" {
		t.Fatal("live .signed was modified upon verify failure")
	}

	// Case C: nsd-checkzone fails
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "dnssec-signzone") {
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-f" {
					_ = os.WriteFile(args[i+1], []byte("syntax invalid"), 0644)
					break
				}
			}
			return []byte("signed"), nil
		}
		if strings.HasSuffix(args[0], "nsd-checkzone") {
			return nil, errors.New("injected checkzone fault")
		}
		return []byte("ok"), nil
	}
	if err := m.signDNSZone(ctx, domain); err == nil {
		t.Fatal("expected nsd-checkzone to fail")
	}
	raw, err = os.ReadFile(signedPath)
	if err != nil || string(raw) != "existing valid signature" {
		t.Fatal("live .signed was modified upon checkzone failure")
	}

	// Verify no stray temporary files in /etc/nsd/zones/
	entries, err := os.ReadDir(m.target("/etc/nsd/zones"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".urmail-sign-") || strings.HasPrefix(entry.Name(), ".staging-") {
			t.Fatalf("found leftover temporary staging file in zones dir: %s", entry.Name())
		}
	}

	// Case D: Success commits atomically
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "dnssec-signzone") {
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-f" {
					_ = os.WriteFile(args[i+1], []byte("new verified signature"), 0644)
					break
				}
			}
			return []byte("signed"), nil
		}
		return []byte("ok"), nil
	}
	if err := m.signDNSZone(ctx, domain); err != nil {
		t.Fatalf("unexpected failure: %v", err)
	}
	raw, err = os.ReadFile(signedPath)
	if err != nil || string(raw) != "new verified signature" {
		t.Fatalf("expected new verified signature, got %s", raw)
	}
}

// Test 5: Rollback advances serial and re-signs old content
func TestDNSSECRollbackAdvancesSerialAndResigns(t *testing.T) {
	m := testManager(t)
	mockStore := newMockZoneVersionStore()
	m.versions = mockStore

	domain := "example.com"
	setupMockDNSState(t, m, domain, "signed")
	initialSerial := uint32(2026102801)
	mockStore.serials[domain] = initialSerial
	setupMockZoneFiles(t, m, domain, initialSerial, "initial valid signed content")

	p := ApplyParams{
		Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: domain, Status: "active"}}},
		DNS:     nsd.Snapshot{Zones: []nsd.Zone{{Domain: domain, Mode: "managed", Sign: true}}},
	}
	raw, _ := json.Marshal(p)

	// First successful apply
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "dnssec-signzone") {
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-f" {
					_ = os.WriteFile(args[i+1], []byte("generation-1-signed"), 0644)
					break
				}
			}
			return []byte("signed"), nil
		}
		return []byte("ok"), nil
	}
	if _, _, err := m.Apply(raw); err != nil {
		t.Fatal(err)
	}

	// Capture state after gen 1
	signedFile := m.target("/etc/nsd/zones/" + domain + ".signed")
	txtFile := m.target("/etc/nsd/zones/" + domain + ".txt")

	// Now start a second apply that mutates zone content, signs it, but fails at probe
	p.DNS.Zones[0].Records = []nsd.Record{{Type: "TXT", Name: "test", Value: "changed"}}
	raw2, _ := json.Marshal(p)

	reloadedNSD := false
	m.run = func(_ context.Context, path string, args ...string) error {
		if path == "/usr/bin/systemctl" && len(args) >= 2 && args[0] == "restart" && args[1] == "nsd" {
			reloadedNSD = true
		}
		if path == "/usr/bin/systemctl" && len(args) >= 2 && args[0] == "is-active" {
			return errors.New("injected probe failure to trigger rollback")
		}
		return testCommand(m, path, args...)
	}

	var signedOutputs [][]byte
	signCallCount := 0
	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "dnssec-signzone") {
			signCallCount++
			var outPath string
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-f" {
					outPath = args[i+1]
					break
				}
			}
			inputTxt := args[len(args)-1]
			content, _ := os.ReadFile(inputTxt)
			re := regexp.MustCompile(`(?m)^\s*([0-9]+)\s*;\s*serial`)
			match := re.FindSubmatch(content)
			serialStr := "unknown"
			if len(match) == 2 {
				serialStr = string(match[1])
			}
			sig := fmt.Sprintf("signed-content-for-serial-%s", serialStr)
			_ = os.WriteFile(outPath, []byte(sig), 0644)
			signedOutputs = append(signedOutputs, []byte(sig))
			return []byte("signed"), nil
		}
		return []byte("ok"), nil
	}

	if _, _, err := m.Apply(raw2); err == nil {
		t.Fatal("expected apply to fail at probe")
	}
	// After rollback:
	// 1. Txt file should have an advanced serial (greater than initialSerial)
	txtContent, err := os.ReadFile(txtFile)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\s*([0-9]+)\s*;\s*serial`)
	match := re.FindSubmatch(txtContent)
	if len(match) != 2 {
		t.Fatalf("serial missing in restored txt: %s", txtContent)
	}
	restoredSerial := string(match[1])
	if restoredSerial == fmt.Sprintf("%d", initialSerial) {
		t.Fatalf("rollback serial was not advanced! still %s", restoredSerial)
	}

	// 2. Signed file must match the restored serial
	signedContent, err := os.ReadFile(signedFile)
	if err != nil {
		t.Fatal(err)
	}
	expectedSig := fmt.Sprintf("signed-content-for-serial-%s", restoredSerial)
	if string(signedContent) != expectedSig {
		t.Fatalf("expected signed file to contain %q, got %q", expectedSig, string(signedContent))
	}

	// 3. NSD must have been reloaded with the rollback zone
	if !reloadedNSD {
		t.Fatal("NSD was not reloaded after rollback re-signing")
	}
}

// Test 6: Re-sign failure during rollback sets Degraded=true and retains pending.json
func TestDNSSECRollbackResignFailureDegradesAndPreservesJournal(t *testing.T) {
	m := testManager(t)
	domain := "example.com"
	setupMockDNSState(t, m, domain, "signed")
	setupMockZoneFiles(t, m, domain, 2026102801, "initial valid signature")

	p := ApplyParams{
		Postfix: postfix.Snapshot{Domains: []postfix.Domain{{FQDN: domain, Status: "active"}}},
		DNS:     nsd.Snapshot{Zones: []nsd.Zone{{Domain: domain, Mode: "managed", Sign: true}}},
	}
	raw, _ := json.Marshal(p)

	resignAttempt := false
	m.run = func(_ context.Context, path string, args ...string) error {
		if path == "/usr/bin/systemctl" && len(args) >= 2 && args[0] == "is-active" {
			return errors.New("injected probe failure")
		}
		return testCommand(m, path, args...)
	}

	m.output = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "dnssec-signzone") {
			if resignAttempt {
				return nil, errors.New("signer failed during rollback")
			}
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-f" {
					_ = os.WriteFile(args[i+1], []byte("forward-apply-signed"), 0644)
					break
				}
			}
			resignAttempt = true
			return []byte("signed"), nil
		}
		return []byte("ok"), nil
	}

	if _, _, err := m.Apply(raw); err == nil {
		t.Fatal("expected apply to fail")
	}

	// Verify rollback failed and system is Degraded
	if !m.status.Degraded || m.status.Ready {
		t.Fatalf("expected Degraded=true, Ready=false; got status: %+v", m.status)
	}
	if !strings.Contains(m.status.Error, "rollback failed") {
		t.Fatalf("expected rollback failure message in status.Error: %s", m.status.Error)
	}

	// pending.json MUST be retained on disk
	if _, err := os.Stat(filepath.Join(m.root, "pending.json")); os.IsNotExist(err) {
		t.Fatal("pending.json was deleted despite rollback failure!")
	}

	// Subsequent apply must be refused
	if _, _, err := m.Apply(raw); err == nil {
		t.Fatal("apply succeeded while pending uncommitted journal was present")
	}
}

// Test 7: enrichDNSSnapshot calculates in-memory and does not prematurely mutate disk state.json
func TestEnrichDNSSnapshotDoesNotMutateDiskLiveState(t *testing.T) {
	m := testManager(t)
	domain := "example.com"
	state := setupMockDNSState(t, m, domain, "signed")

	snap := nsd.Snapshot{
		Zones: []nsd.Zone{{Domain: domain, Sign: true}},
	}

	statePath := filepath.Join(m.dnsDirectory(domain), "state.json")
	initialRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := m.enrichDNSSnapshot(&snap); err != nil {
		t.Fatal(err)
	}

	if snap.Zones[0].SigningVersion == "" {
		t.Fatal("SigningVersion was not calculated in memory")
	}

	afterRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(initialRaw, afterRaw) {
		t.Fatalf("state.json was prematurely modified on disk during enrichDNSSnapshot!\nBefore: %s\nAfter: %s", initialRaw, afterRaw)
	}

	var readState dnsKeyState
	if err := json.Unmarshal(afterRaw, &readState); err != nil {
		t.Fatal(err)
	}
	if !readState.SigningEpoch.Equal(state.SigningEpoch) {
		t.Fatalf("disk state SigningEpoch changed: expected %v, got %v", state.SigningEpoch, readState.SigningEpoch)
	}
}
