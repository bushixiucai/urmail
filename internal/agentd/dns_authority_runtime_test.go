package agentd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
)

// This opt-in fixture executes the same root signing methods as config.apply.
// Persistent fixture output contains disposable test keys, never production data.
func TestDNSSECRealTools(t *testing.T) {
	if os.Getenv("URMAIL_REAL_DNSSEC") != "1" {
		t.Skip("real BIND/NSD tools were not requested")
	}
	for _, path := range []string{"/usr/bin/dnssec-keygen", "/usr/bin/dnssec-signzone", "/usr/bin/dnssec-verify", "/usr/bin/dnssec-dsfromkey", "/usr/sbin/nsd-checkzone"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("required real tool missing", path)
		}
	}
	directory := os.Getenv("URMAIL_DNSSEC_FIXTURE_DIR")
	if directory == "" {
		directory = t.TempDir()
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	manager := &ApplyManager{root: filepath.Join(directory, "state"), targetRoot: filepath.Join(directory, "system"), cfg: &config.Config{}}
	if err := os.MkdirAll(manager.root, 0700); err != nil {
		t.Fatal(err)
	}
	domain := "example.test"
	zonePath := manager.target("/etc/nsd/zones/" + domain + ".txt")
	if err := os.MkdirAll(filepath.Dir(zonePath), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(zonePath); os.IsNotExist(err) {
		zone := `$ORIGIN example.test.
$TTL 86400
@ IN SOA ns1.example.test. hostmaster.example.test. (2026102801 30 10 1209600 60)
@ IN NS ns1.example.test.
@ IN NS ns2.example.test.
ns1 IN A 127.0.0.2
ns2 IN A 127.0.0.3
@ IN MX 10 mail.example.test.
mail IN A 127.0.0.5
`
		if err = os.WriteFile(zonePath, []byte(zone), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	phase := os.Getenv("URMAIL_DNSSEC_PHASE")
	if phase == "" {
		phase = "initial"
	}
	state, err := manager.loadDNSState(domain)
	switch phase {
	case "initial":
		if !os.IsNotExist(err) {
			t.Fatal("initial fixture already has state", err)
		}
		generation, createErr := manager.prepareDNSKeys(ctx, domain)
		if createErr != nil {
			t.Fatal(createErr)
		}
		state = dnsKeyState{Current: generation, Phase: "prepared"}
	case "rotate":
		if err != nil || state.Previous != nil {
			t.Fatal("invalid rotate state", err)
		}
		previous := state.Current
		state.Previous = &previous
		state.Current, err = manager.prepareDNSKeys(ctx, domain)
		if err != nil {
			t.Fatal(err)
		}
		state.Phase = "dual-signing"
		state.PublishedAt = time.Time{}
	case "retire":
		if err != nil || state.Previous == nil {
			t.Fatal("invalid retire state", err)
		}
		state.Phase = "retired"
		if err = manager.archiveDNSGeneration(domain, *state.Previous); err != nil {
			t.Fatal(err)
		}
	case "rollback":
		if err != nil || state.Previous == nil {
			t.Fatal("invalid rollback state", err)
		}
		if err = manager.restoreDNSGeneration(domain, *state.Previous); err != nil {
			t.Fatal(err)
		}
		state.Phase = "dual-signing"
	default:
		t.Fatal("unknown isolated phase")
	}
	if err = manager.saveDNSState(domain, state); err != nil {
		t.Fatal(err)
	}
	if err = manager.signDNSZone(ctx, domain); err != nil {
		t.Fatal(err)
	}
	signed := manager.target("/etc/nsd/zones/" + domain + ".signed")
	before, err := os.ReadFile(signed)
	if err != nil {
		t.Fatal(err)
	}
	if !signatureExpiry(signed).After(time.Now().Add(25 * 24 * time.Hour)) {
		t.Fatal("missing or prematurely expiring signatures")
	}
	ds, err := manager.dnsDS(ctx, domain, state.Current)
	if err != nil || len(ds) != 1 {
		t.Fatal("KSK DS output invalid", ds, err)
	}
	manager.output = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "dnssec-signzone") {
			return nil, errors.New("injected signer failure")
		}
		return exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	}
	if err = manager.signDNSZone(ctx, domain); err == nil {
		t.Fatal("signing fault was ignored")
	}
	after, err := os.ReadFile(signed)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed signing changed the last valid zone")
	}
	facts := map[string]any{"phase": phase, "signed_zone": signed, "unsigned_zone": zonePath, "ds": ds, "keys": state.Current.Keys, "rollback_available": state.Previous != nil, "signature_expiry": signatureExpiry(signed)}
	raw, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "facts.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	t.Log("real keygen/sign/verify/checkzone/DS and failure-preservation passed", phase)
}

// TestDNSSECDelegationRuntime observes real parent/child authoritative answers
// through a validating resolver. The only injected setting is the disposable
// fixture parent's trust anchor; production always uses the system root anchor.
func TestDNSSECDelegationRuntime(t *testing.T) {
	if os.Getenv("URMAIL_REAL_DNSSEC") != "1" {
		t.Skip("real DNS runtime not requested")
	}
	directory := os.Getenv("URMAIL_DNSSEC_FIXTURE_DIR")
	if directory == "" {
		t.Fatal("explicit isolated fixture required")
	}
	manager := &ApplyManager{root: filepath.Join(directory, "state"), targetRoot: filepath.Join(directory, "system"), cfg: &config.Config{}}
	manager.output = func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[0], "delv") {
			args = append([]string{args[0], "-a", filepath.Join(directory, "parent.trust"), "+root=test."}, args[1:]...)
		}
		return exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	}
	state, err := manager.loadDNSState("example.test")
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.observeDNS(t.Context(), "example.test", state)
	if err != nil {
		t.Fatal(err)
	}
	phase := os.Getenv("URMAIL_DNSSEC_EXPECT")
	switch phase {
	case "ready":
		if !status.Ready || !status.ParentDSMatches || !status.ChainValid {
			t.Fatalf("delegation not validated: %+v", status)
		}
	case "wrong-ds":
		if status.Ready || status.ParentDSMatches || status.ChainValid {
			t.Fatalf("wrong DS accepted: %+v", status)
		}
	case "expired", "future":
		if status.Ready || status.LocalSignaturesValid || status.ChainValid {
			t.Fatalf("temporally invalid signature accepted (%s): %+v", phase, status)
		}
	default:
		t.Fatal("explicit expected phase required")
	}
	raw, _ := json.MarshalIndent(status, "", "  ")
	if err = os.WriteFile(filepath.Join(directory, "observation-"+phase+".json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestDNSSecondaryInstallRealTools validates the generated NSD configuration
// with the real parser and injects only service-control failures to verify
// exact configuration/secret/state rollback. Replication is tested separately.
func TestDNSSecondaryInstallRealTools(t *testing.T) {
	if os.Getenv("URMAIL_REAL_DNSSEC") != "1" {
		t.Skip("real NSD parser not requested")
	}
	root := t.TempDir()
	manager := &ApplyManager{root: filepath.Join(root, "state"), targetRoot: filepath.Join(root, "system"), cfg: &config.Config{DNSSecondaryOnly: true, MyHostname: "ns2.example.test", DNSListenAddresses: []string{"127.0.0.3"}}}
	imports := filepath.Join(manager.dnsRoot(), "imports")
	if err := os.MkdirAll(imports, 0700); err != nil {
		t.Fatal(err)
	}
	bundle := DNSSecondaryBundle{Version: 1, Domain: "example.test", Hostname: "ns2.example.test", Address: "127.0.0.3", PrimaryAddress: "127.0.0.2", KeyName: "transfer-one", Secret: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}
	raw, _ := json.Marshal(bundle)
	path := filepath.Join(imports, "example.test.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	manager.run = func(context.Context, string, ...string) error { return nil }
	if _, _, err := manager.installSecondaryDNS("example.test"); err != nil {
		t.Fatal(err)
	}
	runtimePath := manager.target(dnsSecondaryRuntime)
	before, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(manager.dnsRoot(), "installed", "example.test.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Secret = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	raw, _ = json.Marshal(bundle)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	failed := false
	manager.run = func(_ context.Context, _ string, args ...string) error {
		if len(args) > 0 && args[0] == "restart" && !failed {
			failed = true
			return errors.New("injected restart failure")
		}
		return nil
	}
	if _, _, err = manager.installSecondaryDNS("example.test"); err == nil {
		t.Fatal("restart failure accepted")
	}
	after, err := os.ReadFile(runtimePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed secondary apply did not restore runtime/secret bytes", err)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("failed secondary apply changed installed identity", err)
	}
	info, err := os.Stat(runtimePath)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("secondary secret mode changed on rollback", err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err = manager.installSecondaryDNS("example.test"); err == nil {
		t.Fatal("publicly readable private bundle accepted")
	}
}
