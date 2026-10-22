package reconcile_test

import (
	"context"
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
	go p.StartWorker(ctx, 20*time.Millisecond, func(files map[string]string) error {
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
