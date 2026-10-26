package reconcile

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail"
)

// Pipeline triggers configuration re-rendering and service reloads when domains or mailboxes change.
type Pipeline struct {
	cfg         *config.Config
	querier     StoreQuerier
	publicIPv4  string
	nameservers []string
	log         *zap.Logger
	ensureCerts func(context.Context) error

	triggerCh chan struct{}
	mu        sync.Mutex
	running   bool

	// Render failure bookkeeping: render errors are logged AND aggregated so
	// callers/monitoring can observe that reconcile is failing instead of
	// the error being silently dropped (M3 review P2).
	failCount     int64
	lastError     error
	lastRender    time.Time
	lastContext   *mail.ConfigContext
	totalFailures int64
}

// NewPipeline creates a reconcile trigger pipeline.
func NewPipeline(cfg *config.Config, q StoreQuerier, publicIPv4 string, nameservers []string) *Pipeline {
	return &Pipeline{
		cfg:         cfg,
		querier:     q,
		publicIPv4:  publicIPv4,
		nameservers: nameservers,
		log:         zap.NewNop(),
		triggerCh:   make(chan struct{}, 1),
	}
}

// WithLogger sets the structured logger used for render failures. Passing nil
// keeps the no-op logger.
func (p *Pipeline) WithLogger(l *zap.Logger) *Pipeline {
	if l != nil {
		p.log = l
	}
	return p
}

// WithCertificateEnsurer runs only after a successful configuration apply.
// Bootstrap registration must precede any external certificate issuance.
func (p *Pipeline) WithCertificateEnsurer(fn func(context.Context) error) *Pipeline {
	p.ensureCerts = fn
	return p
}

// Trigger queues a reconcile execution. It is non-blocking (coalesces rapid events).
func (p *Pipeline) Trigger() {
	select {
	case p.triggerCh <- struct{}{}:
	default:
		// Already queued
	}
}

// RenderOnce executes one synchronous reconcile cycle, assembling DB snapshot and rendering configs.
func (p *Pipeline) RenderOnce(ctx context.Context) (files map[string]string, err error) {
	start := time.Now()
	defer func() { observeRender(start, err) }()
	mailCtx, err := AssembleConfigContext(ctx, p.cfg, p.querier, p.publicIPv4, p.nameservers)
	if err != nil {
		return nil, fmt.Errorf("assemble config context: %w", err)
	}

	p.mu.Lock()
	p.lastContext = mailCtx
	p.mu.Unlock()
	files, err = mail.RenderAll(*mailCtx)
	if err != nil {
		return nil, fmt.Errorf("render all configs: %w", err)
	}

	return files, nil
}

// Stats reports the render-failure aggregate: how many consecutive/total
// failures happened and the most recent error.
func (p *Pipeline) Stats() (failures int64, lastRender time.Time, lastErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failCount, p.lastRender, p.lastError
}

// CurrentSnapshot returns the last completely assembled domain snapshot.
func (p *Pipeline) CurrentSnapshot() *mail.ConfigContext {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastContext
}

// StartWorker retries failed generations without requiring another user write.
func (p *Pipeline) StartWorker(ctx context.Context, debounce time.Duration, onRendered func(map[string]string) error) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.running = false; p.mu.Unlock() }()
	var retry <-chan time.Time
	delay := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.triggerCh:
		case <-retry:
		}
		retry = nil
		if debounce > 0 {
			timer := time.NewTimer(debounce)
		debounceWait:
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-p.triggerCh:
					// Render once after the latest change has settled.
					timer.Reset(debounce)
				case <-timer.C:
					break debounceWait
				}
			}
		}
		files, err := p.RenderOnce(ctx)
		if err == nil && onRendered != nil {
			err = onRendered(files)
		}
		if err == nil && p.ensureCerts != nil {
			if ensureErr := p.ensureCerts(ctx); ensureErr != nil {
				p.log.Warn("certificate ensure after bootstrap failed", zap.Error(ensureErr))
			}
		}
		p.mu.Lock()
		p.lastRender = time.Now()
		if err != nil {
			p.failCount++
			p.totalFailures++
			p.lastError = err
		} else {
			p.failCount = 0
			p.lastError = nil
		}
		failures := p.failCount
		p.mu.Unlock()
		observeApply(err, failures)
		if err != nil {
			p.log.Error("reconcile generation failed", zap.Error(err), zap.Int64("consecutive_failures", failures))
			// A small deterministic jitter avoids synchronized fleet-wide retries.
			jitter := time.Duration(time.Now().UnixNano() % int64(delay/4+1))
			retry = time.After(delay + jitter)
			delay *= 2
			if delay > time.Minute {
				delay = time.Minute
			}
		} else {
			delay = time.Second
		}
	}
}
