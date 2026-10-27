package nsd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/mail/nsd"
)

// goldenFor compares (or records with -update) each rendered file under
// testdata/golden/<case>/.
func goldenFor(t *testing.T, caseName string, out map[string]string) {
	t.Helper()
	goldenDir := filepath.Join("testdata", "golden", caseName)
	if *update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", goldenDir, err)
		}
	}
	for key, content := range out {
		if !strings.HasSuffix(content, "\n") {
			t.Errorf("%s does not end with newline", key)
		}
		safeFilename := strings.ReplaceAll(key, "/", "_")
		goldenFile := filepath.Join(goldenDir, safeFilename)
		if *update {
			if err := os.WriteFile(goldenFile, []byte(content), 0o644); err != nil {
				t.Fatalf("write golden %s: %v", goldenFile, err)
			}
			continue
		}
		expected, err := os.ReadFile(goldenFile)
		if err != nil {
			t.Fatalf("read golden %s: %v", goldenFile, err)
		}
		if string(expected) != content {
			t.Errorf("mismatch in %s:\n--- Expected ---\n%s\n--- Got ---\n%s", safeFilename, string(expected), content)
		}
	}
}

func signingKeys() []nsd.DNSSECKey {
	return []nsd.DNSSECKey{
		{
			KeyTag: 23417, Flags: nsd.FlagKSK, Algorithm: nsd.AlgorithmECDSAP256SHA256,
			PublicKey: "mHcExAMPLEKSKKEYMATERIAL1", DSRecord: "23417 13 2 ABCDEF00",
			State: nsd.KeyStateSigning, CreatedAt: "2026-10-01T00:00:00Z", PublishedAt: "2026-10-01T06:00:00Z",
		},
		{
			KeyTag: 40211, Flags: nsd.FlagZSK, Algorithm: nsd.AlgorithmECDSAP256SHA256,
			PublicKey: "mHcExAMPLEZSKKEYMATERIAL2",
			State:     nsd.KeyStateSigning, CreatedAt: "2026-10-01T00:00:00Z", PublishedAt: "2026-10-01T06:00:00Z",
		},
		// A freshly generated successor KSK: staged but not yet published.
		{
			KeyTag: 51993, Flags: nsd.FlagKSK, Algorithm: nsd.AlgorithmECDSAP256SHA256,
			PublicKey: "mHcExAMPLENEWKSKMATERIAL3",
			State:     nsd.KeyStateGenerated, CreatedAt: "2026-10-20T00:00:00Z",
		},
	}
}

func dnssecZone() nsd.Zone {
	return nsd.Zone{
		Domain: "signed-example.com",
		Mode:   "managed",
		Serial: 2026102701,
		DNSSEC: &nsd.DNSSECConfig{
			KeyDirectory: "/var/lib/nsd/keys/signed-example.com",
			Keys:         signingKeys(),
		},
	}
}

func TestGoldenSignedZone(t *testing.T) {
	node := defaultNodeConfig()
	snap := nsd.Snapshot{Zones: []nsd.Zone{dnssecZone()}}
	out, err := nsd.Generate(node, snap)
	if err != nil {
		t.Fatalf("Generate signed zone: %v", err)
	}
	wantFiles := []string{
		"zones.conf",
		"zones/signed-example.com.txt",
		"signconf/signed-example.com.conf",
		"keys/signed-example.com/rollover.state",
	}
	for _, f := range wantFiles {
		if _, ok := out[f]; !ok {
			t.Errorf("signed zone output missing %q; have keys: %v", f, keysOf(out))
		}
	}
	goldenFor(t, "dnssec_signed_zone", out)
}

func TestGoldenSecondaryRedundancy(t *testing.T) {
	node := defaultNodeConfig()
	snap := nsd.Snapshot{Zones: []nsd.Zone{
		{Domain: "signed-example.com", Mode: "managed", Serial: 2026102701},
		{Domain: "other.example", Mode: "delegated", Serial: 2026102702},
	}}
	sec := nsd.SecondaryConfig{
		Hostname:       "ns2.urmail.cn",
		Address:        "198.51.100.2",
		PrimaryAddress: "192.0.2.10",
		TSIGKeyName:    "axfr-urmail",
	}

	primary, err := nsd.RenderSecondaryConf(node, snap, sec)
	if err != nil {
		t.Fatalf("RenderSecondaryConf: %v", err)
	}
	peer, err := nsd.RenderSecondaryPeerConf(node, snap, sec)
	if err != nil {
		t.Fatalf("RenderSecondaryPeerConf: %v", err)
	}
	runbook := nsd.RenderAXFRCredRunbook(sec)

	// The TSIG secret must never appear in any render: only the key name.
	for name, content := range map[string]string{
		"secondary.conf": primary, "secondary-peer.conf": peer, "axfr-runbook.md": runbook,
	} {
		if strings.Contains(content, "secret-example") {
			t.Errorf("%s must never contain a TSIG secret", name)
		}
	}
	goldenFor(t, "secondary_redundancy", map[string]string{
		"secondary.conf":      primary,
		"secondary-peer.conf": peer,
		"axfr-runbook.md":     runbook,
	})
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestKeyStateTransitions(t *testing.T) {
	valid := []nsd.DNSSECKeyState{
		nsd.KeyStateGenerated, nsd.KeyStatePublished, nsd.KeyStateSigning,
		nsd.KeyStateRetiring, nsd.KeyStateRemoved,
	}
	for i := 0; i < len(valid)-1; i++ {
		got, err := nsd.TransitionKeyState(valid[i], valid[i+1])
		if err != nil || got != valid[i+1] {
			t.Errorf("%s -> %s must be allowed, got %s, %v", valid[i], valid[i+1], got, err)
		}
		// No-op stays allowed.
		if got, err := nsd.TransitionKeyState(valid[i], valid[i]); err != nil || got != valid[i] {
			t.Errorf("%s no-op must be allowed, got %s, %v", valid[i], got, err)
		}
	}
	// From removed nothing may follow.
	if _, err := nsd.TransitionKeyState(nsd.KeyStateRemoved, nsd.KeyStateGenerated); !errors.Is(err, nsd.ErrInvalidRollover) {
		t.Errorf("removed -> generated must be rejected, got %v", err)
	}
	invalid := [][2]nsd.DNSSECKeyState{
		{nsd.KeyStateGenerated, nsd.KeyStateSigning},   // skipping publication
		{nsd.KeyStateGenerated, nsd.KeyStateRetiring},  // skipping two states
		{nsd.KeyStatePublished, nsd.KeyStateGenerated}, // backwards
		{nsd.KeyStateSigning, nsd.KeyStatePublished},   // backwards
		{nsd.KeyStateRetiring, nsd.KeyStateSigning},    // backwards (un-retire)
		{nsd.KeyStateRemoved, nsd.KeyStatePublished},   // resurrect
		{nsd.KeyStatePublished, nsd.KeyStateRemoved},   // skipping retire
	}
	for _, tc := range invalid {
		if _, err := nsd.TransitionKeyState(tc[0], tc[1]); !errors.Is(err, nsd.ErrInvalidRollover) {
			t.Errorf("%s -> %s must be rejected, got %v", tc[0], tc[1], err)
		}
	}
}

func TestPlanKeyRollover(t *testing.T) {
	oldKSK := nsd.DNSSECKey{
		KeyTag: 23417, Flags: nsd.FlagKSK, Algorithm: nsd.AlgorithmECDSAP256SHA256,
		PublicKey: "OLD", DSRecord: "23417 13 2 ABCDEF00",
		State: nsd.KeyStateSigning, CreatedAt: "2026-01-01T00:00:00Z", PublishedAt: "2026-01-01T01:00:00Z",
	}
	newKSK := nsd.DNSSECKey{
		KeyTag: 51993, Flags: nsd.FlagKSK, Algorithm: nsd.AlgorithmECDSAP256SHA256,
		PublicKey: "NEW", DSRecord: "51993 13 2 00FEDCBA",
		State: nsd.KeyStatePublished, CreatedAt: "2026-10-20T00:00:00Z",
	}

	plan, err := nsd.PlanKeyRollover("Signed-Example.com.", oldKSK, newKSK, 3600)
	if err != nil {
		t.Fatalf("PlanKeyRollover: %v", err)
	}
	if plan.Zone != "signed-example.com" {
		t.Errorf("zone must be normalized, got %q", plan.Zone)
	}
	if plan.WaitTTL != 3600 {
		t.Errorf("wait ttl must be honored, got %d", plan.WaitTTL)
	}
	// Ordered: strictly increasing, starts with publish, ends with remove.
	steps := plan.SortSteps()
	if len(steps) < 5 {
		t.Fatalf("expected at least 5 steps, got %d", len(steps))
	}
	if steps[0].Kind != nsd.StepPublishNewDNSKEY {
		t.Errorf("first step must publish the new DNSKEY, got %s", steps[0].Kind)
	}
	if steps[1].Kind != nsd.StepWaitPropagation || steps[1].WaitSeconds != 3600 {
		t.Errorf("second step must wait the DNSKEY TTL, got %s/%d", steps[1].Kind, steps[1].WaitSeconds)
	}
	if steps[len(steps)-1].Kind != nsd.StepRemoveOld {
		t.Errorf("last step must remove the old key, got %s", steps[len(steps)-1].Kind)
	}
	for i, s := range steps {
		if s.Order != i+1 {
			t.Errorf("step order must be dense 1..n, got %d at index %d", s.Order, i)
		}
	}
	// KSK rollover must move the DS at the parent in both directions.
	kinds := map[nsd.RolloverStepKind]bool{}
	for _, s := range steps {
		kinds[s.Kind] = true
	}
	if !kinds[nsd.StepPublishDS] || !kinds[nsd.StepWithdrawOldDS] {
		t.Errorf("KSK rollover must include DS publish and withdraw, got %v", kinds)
	}
	runbook := nsd.RenderRolloverRunbook(plan)
	if !strings.Contains(runbook, "signed-example.com") || !strings.Contains(runbook, "51993") {
		t.Errorf("runbook must name the zone and new key tag, got:\n%s", runbook)
	}
	goldenFor(t, "rollover_runbook", map[string]string{"runbook.md": runbook})

	// ZSK rollover: no DS steps.
	newZSK := newKSK
	newZSK.KeyTag, newZSK.Flags, newZSK.DSRecord = 60111, nsd.FlagZSK, ""
	oldZSK := oldKSK
	oldZSK.KeyTag, oldZSK.Flags, oldZSK.DSRecord = 40211, nsd.FlagZSK, ""
	zskPlan, err := nsd.PlanKeyRollover("signed-example.com", oldZSK, newZSK, 0)
	if err != nil {
		t.Fatalf("PlanKeyRollover zsk: %v", err)
	}
	if zskPlan.WaitTTL != nsd.DefaultRolloverWaitTTL {
		t.Errorf("zero ttl must default to %d, got %d", nsd.DefaultRolloverWaitTTL, zskPlan.WaitTTL)
	}
	for _, s := range zskPlan.Steps {
		if s.Kind == nsd.StepPublishDS || s.Kind == nsd.StepWithdrawOldDS {
			t.Errorf("ZSK rollover must not touch the DS, got step %s", s.Kind)
		}
	}
}

func TestPlanKeyRolloverRejectsInvalidTransitions(t *testing.T) {
	base := nsd.DNSSECKey{
		KeyTag: 100, Flags: nsd.FlagKSK, Algorithm: nsd.AlgorithmECDSAP256SHA256,
		PublicKey: "K", State: nsd.KeyStateSigning,
	}
	staged := base
	staged.KeyTag, staged.State = 200, nsd.KeyStateGenerated

	cases := []struct {
		name   string
		oldKey nsd.DNSSECKey
		newKey nsd.DNSSECKey
	}{
		{"old_key_generated", func() nsd.DNSSECKey { k := base; k.State = nsd.KeyStateGenerated; return k }(), staged},
		{"old_key_already_removed", func() nsd.DNSSECKey { k := base; k.State = nsd.KeyStateRemoved; return k }(), staged},
		{"new_key_already_signing", base, func() nsd.DNSSECKey { k := staged; k.State = nsd.KeyStateSigning; k.KeyTag = 200; return k }()},
		{"new_key_removed", base, func() nsd.DNSSECKey { k := staged; k.State = nsd.KeyStateRemoved; k.KeyTag = 200; return k }()},
		{"same_key_tag", base, base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := nsd.PlanKeyRollover("example.com", tc.oldKey, tc.newKey, 0)
			if !errors.Is(err, nsd.ErrInvalidRollover) {
				t.Fatalf("expected ErrInvalidRollover, got %v", err)
			}
		})
	}
	if _, err := nsd.PlanKeyRollover("", base, staged, 0); !errors.Is(err, nsd.ErrInvalidRollover) {
		t.Errorf("empty zone must be rejected, got %v", err)
	}
}

func TestDNSSECValidationErrors(t *testing.T) {
	node := defaultNodeConfig()
	build := func(mutate func(*nsd.DNSSECConfig)) nsd.Zone {
		cfg := nsd.DNSSECConfig{
			KeyDirectory: "/var/lib/nsd/keys/example.com",
			Keys:         signingKeys(),
		}
		mutate(&cfg)
		return nsd.Zone{Domain: "example.com", Mode: "managed", Serial: 1, DNSSEC: &cfg}
	}
	cases := map[string]nsd.Zone{
		"missing_key_directory":  build(func(c *nsd.DNSSECConfig) { c.KeyDirectory = "" }),
		"relative_key_directory": build(func(c *nsd.DNSSECConfig) { c.KeyDirectory = "keys/example.com" }),
		"no_keys":                build(func(c *nsd.DNSSECConfig) { c.Keys = nil }),
		"no_signing_key": build(func(c *nsd.DNSSECConfig) {
			for i := range c.Keys {
				c.Keys[i].State = nsd.KeyStatePublished
			}
		}),
		"duplicate_tag":    build(func(c *nsd.DNSSECConfig) { c.Keys[1].KeyTag = c.Keys[0].KeyTag }),
		"bad_flags":        build(func(c *nsd.DNSSECConfig) { c.Keys[0].Flags = 128 }),
		"bad_algorithm":    build(func(c *nsd.DNSSECConfig) { c.Keys[0].Algorithm = 7 }),
		"empty_public_key": build(func(c *nsd.DNSSECConfig) { c.Keys[0].PublicKey = " " }),
		"refresh_after_validity": build(func(c *nsd.DNSSECConfig) {
			c.SignatureValidity, c.SignatureRefresh = 3600, 7200
		}),
	}
	for name, zone := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := nsd.Generate(node, nsd.Snapshot{Zones: []nsd.Zone{zone}}); err == nil {
				t.Fatalf("expected validation error for %s", name)
			}
		})
	}
}
