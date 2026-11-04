package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

type onboardingMockQuerier struct {
	domains      []store.PlatformListDomainsRow
	mailboxes    []store.PlatformListMailboxesRow
	aliases      []store.PlatformListAliasesRow
	dkimDomains  []store.PlatformListDKIMDomainsRow
	spamPolicies []store.PlatformListSpamDeliveryPoliciesRow
}

func (m *onboardingMockQuerier) PlatformListTransportSecurity(context.Context) ([]store.PlatformListTransportSecurityRow, error) {
	return nil, nil
}
func (m *onboardingMockQuerier) PlatformListDNSSecurity(context.Context) ([]store.PlatformListDNSSecurityRow, error) {
	return nil, nil
}
func (m *onboardingMockQuerier) PlatformListSpamDeliveryPolicies(context.Context) ([]store.PlatformListSpamDeliveryPoliciesRow, error) {
	return m.spamPolicies, nil
}
func (m *onboardingMockQuerier) PlatformListManagedDNS(context.Context) ([]store.PlatformListManagedDNSRow, error) {
	return nil, nil
}
func (m *onboardingMockQuerier) PlatformListDomains(_ context.Context) ([]store.PlatformListDomainsRow, error) {
	return m.domains, nil
}
func (m *onboardingMockQuerier) PlatformListMailboxes(_ context.Context) ([]store.PlatformListMailboxesRow, error) {
	return m.mailboxes, nil
}
func (m *onboardingMockQuerier) PlatformListAliases(_ context.Context) ([]store.PlatformListAliasesRow, error) {
	return m.aliases, nil
}
func (m *onboardingMockQuerier) PlatformListDKIMDomains(_ context.Context) ([]store.PlatformListDKIMDomainsRow, error) {
	return m.dkimDomains, nil
}

func testSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ur")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

// TestOnboardingPreservesSpamPoliciesAndCompiledSieveAcrossAllSteps verifies requirement 4:
// When Tenant A has existing private spam delivery policies (blacklist/whitelist),
// Tenant B performing onboarding configuration apply across all steps (bootstrap,
// configuration_apply, protocol_readiness) must NEVER flush SpamPolicies or the global
// Sieve source/compiled binary into an empty list.
func TestOnboardingPreservesSpamPoliciesAndCompiledSieveAcrossAllSteps(t *testing.T) {
	cfg := &config.Config{
		MyHostname:         "box.example.com",
		PrimaryDomain:      "example.com",
		StorageRoot:        filepath.Join(t.TempDir(), "mail"),
		DatabaseURLDovecot: "host=127.0.0.1 user=dovecot password=secret",
		ListenAddr:         "127.0.0.1:8080",
		PublicIPv4:         "192.0.2.1",
		Nameservers:        []string{"ns.example.com"},
	}

	// 1. Tenant A has private spam policies, and Tenant B is the new domain being onboarded.
	querier := &onboardingMockQuerier{
		domains: []store.PlatformListDomainsRow{
			{Fqdn: "example.com", Status: "active"},
			{Fqdn: "tenant-a.com", Status: "active"},
			{Fqdn: "tenant-b.com", Status: "active"},
		},
		mailboxes: []store.PlatformListMailboxesRow{
			{Address: "user@example.com", Status: "active"},
			{Address: "alice@tenant-a.com", Status: "active"},
			{Address: "bob@tenant-b.com", Status: "active"},
		},
		spamPolicies: []store.PlatformListSpamDeliveryPoliciesRow{
			{
				MailboxAddress: "alice@tenant-a.com",
				Scope:          "mailbox",
				SenderDomain:   "spam-vendor.test",
				List:           "deny",
			},
			{
				MailboxAddress: "alice@tenant-a.com",
				Scope:          "mailbox",
				SenderAddress:  "vip-partner@safe.test",
				List:           "allow",
			},
		},
	}

	// 2. Set up ApplyManager and Agent server over a real unix socket.
	manager, err := agentd.NewTestApplyManager(t.TempDir(), cfg)
	if err != nil {
		t.Fatalf("NewTestApplyManager: %v", err)
	}

	registry := agentd.NewRegistry()
	registry.Register(agentd.OpConfigApply, manager.Apply)

	sockPath := testSocketPath(t)
	agentServer := agentd.NewServer(sockPath, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := agentServer.Listen(ctx); err != nil {
		t.Fatalf("agentServer.Listen: %v", err)
	}
	go func() { _ = agentServer.Serve() }()

	// Wait briefly for socket to become ready
	deadline := time.Now().Add(2 * time.Second)
	dialer := &net.Dialer{}
	for {
		conn, dialErr := dialer.DialContext(ctx, "unix", sockPath)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket not ready: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 3. Set up the Reconcile Pipeline with Applier pointing to the agent socket.
	client := agent.NewClient(sockPath)
	pipeline := reconcile.NewPipeline(cfg, querier, cfg.PublicIPv4, cfg.Nameservers).
		WithLogger(zap.NewNop()).
		WithApplier(func(callCtx context.Context, params reconcile.ApplyParams) (string, error) {
			resp, err := client.Do(callCtx, agentd.OpConfigApply, params)
			if err != nil {
				return "", err
			}
			return resp.Stdout, nil
		})

	applyFn := newOnboardingApply(pipeline)

	// 4. Test positive path: Tenant B onboarding steps.
	// Each onboarding step (bootstrap/bootstrap_config, configuration_apply, protocol_readiness)
	// invokes the unified apply function for "tenant-b.com".
	onboardingSteps := []string{"bootstrap_config", "configuration_apply", "protocol_readiness"}

	sieveSourcePath := manager.Target("/etc/dovecot/urmail-spam/spam-before.sieve")
	sieveBinaryPath := manager.Target("/etc/dovecot/urmail-spam/spam-before.svbin")

	for _, stepName := range onboardingSteps {
		t.Run("step_"+stepName, func(t *testing.T) {
			gen, applyErr := applyFn(ctx, "tenant-b.com")
			if applyErr != nil {
				t.Fatalf("apply at step %s failed: %v", stepName, applyErr)
			}
			if gen == "" {
				t.Fatalf("apply at step %s returned empty generation", stepName)
			}

			// Verify that pipeline snapshot retained Tenant A spam policies
			snap := pipeline.CurrentSnapshot()
			if snap == nil {
				t.Fatalf("CurrentSnapshot is nil after step %s", stepName)
			}
			if len(snap.DovecotNode.SpamPolicies) != 2 {
				t.Fatalf("CurrentSnapshot lost SpamPolicies at step %s: got %d, want 2", stepName, len(snap.DovecotNode.SpamPolicies))
			}

			// Verify the rendered Sieve source file on disk contains Tenant A's policies
			sieveContent, readErr := os.ReadFile(sieveSourcePath)
			if readErr != nil {
				t.Fatalf("read spam-before.sieve at step %s: %v", stepName, readErr)
			}
			sieveStr := string(sieveContent)
			if !strings.Contains(sieveStr, "alice@tenant-a.com") {
				t.Fatalf("spam-before.sieve at step %s missing alice@tenant-a.com: %s", stepName, sieveStr)
			}
			if !strings.Contains(sieveStr, "spam-vendor.test") {
				t.Fatalf("spam-before.sieve at step %s missing deny rule for spam-vendor.test: %s", stepName, sieveStr)
			}
			if !strings.Contains(sieveStr, "vip-partner@safe.test") {
				t.Fatalf("spam-before.sieve at step %s missing allow rule for vip-partner@safe.test: %s", stepName, sieveStr)
			}

			// Verify the compiled Sieve binary on disk was generated and contains Tenant A's rules
			binContent, binErr := os.ReadFile(sieveBinaryPath)
			if binErr != nil {
				t.Fatalf("read spam-before.svbin at step %s: %v", stepName, binErr)
			}
			binStr := string(binContent)
			if !strings.Contains(binStr, "alice@tenant-a.com") || !strings.Contains(binStr, "spam-vendor.test") {
				t.Fatalf("spam-before.svbin at step %s was compiled without Tenant A rules", stepName)
			}
		})
	}

	// 5. Deterministic Negative / Regression Test:
	// Verify that if a caller had sent ApplyParams without SpamPolicies (the defect in the old onboarding code),
	// the Sieve source and compiled binary WOULD be flushed to an empty list without Tenant A's rules!
	t.Run("negative_regression_handcrafted_apply_params_flushes_sieve", func(t *testing.T) {
		snap := pipeline.CurrentSnapshot()
		if snap == nil {
			t.Fatal("pipeline snapshot unavailable")
		}

		// Simulate the old buggy onboarding apply payload (omitted SpamPolicies)
		buggyParams := agentd.ApplyParams{
			Postfix: snap.PostfixSnap,
			DNS:     snap.NSDSnap,
			DKIM:    snap.DKIM,
			// SpamPolicies intentionally omitted (nil), exactly as in the old onboarding code
		}

		resp, applyErr := client.Do(ctx, agentd.OpConfigApply, buggyParams)
		if applyErr != nil {
			t.Fatalf("buggy apply call failed: %v", applyErr)
		}
		if !resp.OK {
			t.Fatalf("buggy apply response not OK: %v", resp.Error)
		}

		// Verify that with the old buggy params, Tenant A's rules WERE flushed to empty!
		sieveContent, readErr := os.ReadFile(sieveSourcePath)
		if readErr != nil {
			t.Fatalf("read spam-before.sieve: %v", readErr)
		}
		sieveStr := string(sieveContent)
		if strings.Contains(sieveStr, "alice@tenant-a.com") || strings.Contains(sieveStr, "spam-vendor.test") {
			t.Fatalf("expected Tenant A rules to be flushed when SpamPolicies is omitted, but found them in: %s", sieveStr)
		}

		binContent, binErr := os.ReadFile(sieveBinaryPath)
		if binErr != nil {
			t.Fatalf("read spam-before.svbin: %v", binErr)
		}
		if strings.Contains(string(binContent), "alice@tenant-a.com") {
			t.Fatalf("expected compiled binary to lose Tenant A rules when SpamPolicies is omitted")
		}

		// Now recover by calling the unified applyFn again, verifying immediate restoration!
		_, restoreErr := applyFn(ctx, "tenant-b.com")
		if restoreErr != nil {
			t.Fatalf("recovery apply failed: %v", restoreErr)
		}
		restoredContent, _ := os.ReadFile(sieveSourcePath)
		if !strings.Contains(string(restoredContent), "alice@tenant-a.com") {
			t.Fatalf("recovery apply failed to restore Tenant A rules")
		}
	})
}
