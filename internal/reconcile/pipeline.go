package reconcile

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail"
	"go.uber.org/zap"
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
	failCount  int64
	lastError  error
	lastRender time.Time
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

// WithCertificateEnsurer runs before each render so newly verified domains
// have a certificate before nginx server blocks are emitted. Errors are
// logged and the render proceeds with whatever certificates exist.
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
func (p *Pipeline) RenderOnce(ctx context.Context) (map[string]string, error) {
	if p.ensureCerts != nil {
		if err := p.ensureCerts(ctx); err != nil {
			p.log.Warn("domain certificate ensure failed", zap.Error(err))
		}
	}
	mailCtx, err := AssembleConfigContext(ctx, p.cfg, p.querier, p.publicIPv4, p.nameservers)
	if err != nil {
		return nil, fmt.Errorf("assemble config context: %w", err)
	}

	files, err := mail.RenderAll(*mailCtx)
	if err != nil {
		return nil, fmt.Errorf("render all configs: %w", err)
	}

	return files, nil
}

// Stats reports the render-failure aggregate: how many consecutive/total
// failures happened and the most recent error.
func (p *Pipeline) Stats() (failures int64, lastErr error, lastRender time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failCount, p.lastError, p.lastRender
}

// StartWorker listens on the trigger channel with debouncing and executes reconcile cycles.
func (p *Pipeline) StartWorker(ctx context.Context, debounce time.Duration, onRendered func(files map[string]string) error) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.triggerCh:
			// Debounce rapid bursts
			if debounce > 0 {
				select {
				case <-time.After(debounce):
				case <-ctx.Done():
					return
				}
			}

			files, err := p.RenderOnce(ctx)
			p.mu.Lock()
			p.lastRender = time.Now()
			p.mu.Unlock()
			if err != nil {
				// Aggregate and log; never silently drop a render failure.
				p.mu.Lock()
				p.failCount++
				p.lastError = err
				count := p.failCount
				p.mu.Unlock()
				p.log.Error("reconcile render failed",
					zap.Error(err),
					zap.Int64("consecutive_failures", count))
				continue
			}

			if onRendered != nil {
				if err := onRendered(files); err != nil {
					p.mu.Lock()
					p.failCount++
					p.lastError = fmt.Errorf("apply rendered files: %w", err)
					count := p.failCount
					p.mu.Unlock()
					p.log.Error("reconcile apply failed",
						zap.Error(err),
						zap.Int64("consecutive_failures", count))
				}
			}
		}
	}
}
