package reconcile_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/reconcile"
)

func TestPipelineTriggerAndDebounce(t *testing.T) {
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

	var renderCount int32
	go p.StartWorker(ctx, 20*time.Millisecond, func(_ map[string]string) error {
		atomic.AddInt32(&renderCount, 1)
		return nil
	})

	// Trigger multiple bursts
	p.Trigger()
	p.Trigger()
	p.Trigger()

	// Wait for debounce and execution
	time.Sleep(60 * time.Millisecond)

	count := atomic.LoadInt32(&renderCount)
	if count != 1 {
		t.Errorf("expected 1 debounced render, got %d", count)
	}

	// Trigger second round
	p.Trigger()
	time.Sleep(60 * time.Millisecond)

	count = atomic.LoadInt32(&renderCount)
	if count != 2 {
		t.Errorf("expected 2 total renders, got %d", count)
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
