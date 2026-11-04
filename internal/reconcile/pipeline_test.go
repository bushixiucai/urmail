package reconcile_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/reconcile"
	"github.com/bushixiucai/urmail/internal/store"
)

func TestPipelineTriggerAndDebounce(t *testing.T) {
	synctest.Test(t, testPipelineTriggerAndDebounce)
}

func testPipelineTriggerAndDebounce(t *testing.T) {
	cfg := &config.Config{
		MyHostname:    "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
		DatabaseURL:   "host=127.0.0.1 dbname=urmail user=urmail_dovecot",
		ListenAddr:    "http://127.0.0.1:8080",
	}

	p := reconcile.NewPipeline(cfg, &mockQuerier{}, "1.2.3.4", []string{"ns1.urmail.cn"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var renderCount atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.StartWorker(ctx, 20*time.Millisecond, func(_ map[string]string) error {
			renderCount.Add(1)
			return nil
		})
	}()

	// Queue more events while the worker is already waiting for the first one.
	p.Trigger()
	synctest.Wait()
	time.Sleep(10 * time.Millisecond)
	p.Trigger()
	p.Trigger()
	synctest.Wait()

	// The later burst must extend the quiet period past the original deadline.
	time.Sleep(15 * time.Millisecond)
	synctest.Wait()
	if count := renderCount.Load(); count != 0 {
		t.Fatalf("rendered before the burst settled: %d", count)
	}
	time.Sleep(5 * time.Millisecond)
	synctest.Wait()

	count := renderCount.Load()
	if count != 1 {
		t.Errorf("expected 1 debounced render, got %d", count)
	}

	// Trigger second round
	p.Trigger()
	synctest.Wait()
	time.Sleep(20 * time.Millisecond)
	synctest.Wait()

	count = renderCount.Load()
	if count != 2 {
		t.Errorf("expected 2 total renders, got %d", count)
	}
	// Cancellation during another pending quiet period must stop the worker.
	p.Trigger()
	synctest.Wait()
	cancel()
	synctest.Wait()
	<-done
	if count = renderCount.Load(); count != 2 {
		t.Errorf("rendered after cancellation: %d", count)
	}
}

func TestPipelineRetriesWithoutAnotherTriggerAndClearsFailure(t *testing.T) {
	cfg := &config.Config{MyHostname: "box.urmail.cn", PrimaryDomain: "urmail.cn", StorageRoot: "/var/mail", DatabaseURL: "host=127.0.0.1 dbname=urmail", ListenAddr: "127.0.0.1:8080"}
	p := reconcile.NewPipeline(cfg, &mockQuerier{}, "192.0.2.1", []string{"ns.urmail.cn"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	done := make(chan struct{})
	go p.StartWorker(ctx, 0, func(map[string]string) error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("temporary apply failure")
		}
		close(done)
		return nil
	})
	p.Trigger()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("failed apply was not retried")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		failures, _, err := p.Stats()
		if failures == 0 && err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("successful retry did not clear failure")
}

// OPS-008 acceptance 1: rendering one snapshot twice yields byte-identical
// output, and rendering is a pure dry-run — RenderOnce returns the file map
// and writes nothing anywhere. Whether the newer HTTP services get vhost
// coverage is asserted by the agent apply tests: managed vhosts proxy the
// whole host to urmaild, so autoconfig, /.well-known/carddav,
// /api/v1/carddav, /api/v1/caldav and /api/v1/eas ride the existing domain
// vhosts without their own generated files.
func TestRenderOnceIsDeterministicDryRun(t *testing.T) {
	cfg := &config.Config{
		MyHostname:    "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
		DatabaseURL:   "host=127.0.0.1 dbname=urmail user=urmail_dovecot",
		ListenAddr:    "127.0.0.1:8080",
	}
	p := reconcile.NewPipeline(cfg, &mockQuerier{}, "192.0.2.1", []string{"ns1.urmail.cn"})
	ctx := context.Background()

	first, err := p.RenderOnce(ctx)
	if err != nil {
		t.Fatalf("RenderOnce: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("RenderOnce produced no files")
	}
	second, err := p.RenderOnce(ctx)
	if err != nil {
		t.Fatalf("second RenderOnce: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("the same snapshot rendered different output")
	}
	if _, ok := first["/etc/nginx/conf.d/urmail/local.conf"]; !ok {
		t.Fatal("missing /etc/nginx/conf.d/urmail/local.conf in rendered output")
	}
}

func TestRenderCycleIndivisibleWithSpamPolicies(t *testing.T) {
	cfg := &config.Config{
		MyHostname:    "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
		DatabaseURL:   "host=127.0.0.1 dbname=urmail user=urmail_dovecot",
		ListenAddr:    "127.0.0.1:8080",
	}
	querier := &mockQuerier{
		spamPolicies: []store.PlatformListSpamDeliveryPoliciesRow{
			{MailboxAddress: "alice@example.com", Scope: "mailbox", SenderDomain: "blocked.test", List: "deny"},
		},
	}
	p := reconcile.NewPipeline(cfg, querier, "192.0.2.1", []string{"ns1.urmail.cn"})
	ctx := context.Background()

	cycle, err := p.RenderCycle(ctx)
	if err != nil {
		t.Fatalf("RenderCycle failed: %v", err)
	}
	if cycle == nil {
		t.Fatal("RenderCycle returned nil cycle")
	}
	if len(cycle.ApplyParams.SpamPolicies) != 1 {
		t.Fatalf("expected 1 spam policy in ApplyParams, got %d", len(cycle.ApplyParams.SpamPolicies))
	}
	if cycle.ApplyParams.SpamPolicies[0].SenderDomain != "blocked.test" {
		t.Errorf("unexpected spam policy sender domain: %s", cycle.ApplyParams.SpamPolicies[0].SenderDomain)
	}
	if !reflect.DeepEqual(cycle.ApplyParams.SpamPolicies, cycle.Snapshot.DovecotNode.SpamPolicies) {
		t.Fatal("ApplyParams.SpamPolicies does not match Snapshot.DovecotNode.SpamPolicies")
	}
	if p.CurrentCycle() != cycle {
		t.Fatal("CurrentCycle does not match returned cycle")
	}
	if p.CurrentSnapshot() != cycle.Snapshot {
		t.Fatal("CurrentSnapshot does not match cycle.Snapshot")
	}
	sieveFile, ok := cycle.Files["/etc/dovecot/urmail-spam/spam-before.sieve"]
	if !ok {
		t.Fatal("missing spam-before.sieve in rendered cycle files")
	}
	if !strings.Contains(sieveFile, "blocked.test") || !strings.Contains(sieveFile, "alice@example.com") {
		t.Fatalf("sieveFile does not contain expected spam policy rules: %s", sieveFile)
	}
}

func TestReconcileAndWaitRequiresDomainAndApplier(t *testing.T) {
	cfg := &config.Config{
		MyHostname:    "box.urmail.cn",
		PrimaryDomain: "urmail.cn",
		StorageRoot:   "/var/mail",
		DatabaseURL:   "host=127.0.0.1 dbname=urmail user=urmail_dovecot",
		ListenAddr:    "127.0.0.1:8080",
	}
	querier := &mockQuerier{
		domains: []store.PlatformListDomainsRow{
			{Fqdn: "example.com", Status: "active"},
			{Fqdn: "tenant-a.com", Status: "active"},
		},
		spamPolicies: []store.PlatformListSpamDeliveryPoliciesRow{
			{MailboxAddress: "user@example.com", Scope: "mailbox", SenderDomain: "spam.test", List: "deny"},
		},
	}
	p := reconcile.NewPipeline(cfg, querier, "192.0.2.1", []string{"ns1.urmail.cn"})
	ctx := context.Background()

	// 1. Without applier configured -> returns RECONCILE_APPLIER_NOT_CONFIGURED
	_, err := p.ReconcileAndWait(ctx, "tenant-a.com")
	if err == nil || err.Error() != "RECONCILE_APPLIER_NOT_CONFIGURED" {
		t.Fatalf("expected RECONCILE_APPLIER_NOT_CONFIGURED, got %v", err)
	}

	var appliedParams []reconcile.ApplyParams
	p.WithApplier(func(_ context.Context, params reconcile.ApplyParams) (string, error) {
		appliedParams = append(appliedParams, params)
		return "test-generation-001", nil
	})

	// 2. Domain not in snapshot -> returns ONBOARDING_DOMAIN_NOT_RENDERED
	_, err = p.ReconcileAndWait(ctx, "unrendered-domain.com")
	if err == nil || err.Error() != "ONBOARDING_DOMAIN_NOT_RENDERED" {
		t.Fatalf("expected ONBOARDING_DOMAIN_NOT_RENDERED, got %v", err)
	}
	if len(appliedParams) != 0 {
		t.Fatalf("applier was called when domain was not rendered")
	}

	// 3. Domain present -> successful reconcile and wait
	gen, err := p.ReconcileAndWait(ctx, "tenant-a.com")
	if err != nil {
		t.Fatalf("ReconcileAndWait failed: %v", err)
	}
	if gen != "test-generation-001" {
		t.Fatalf("unexpected generation: %s", gen)
	}
	if len(appliedParams) != 1 {
		t.Fatalf("expected 1 apply call, got %d", len(appliedParams))
	}
	if len(appliedParams[0].SpamPolicies) != 1 || appliedParams[0].SpamPolicies[0].SenderDomain != "spam.test" {
		t.Fatalf("SpamPolicies was not passed to applier: %+v", appliedParams[0].SpamPolicies)
	}
}
