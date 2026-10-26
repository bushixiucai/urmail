package reconcile_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/reconcile"
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
