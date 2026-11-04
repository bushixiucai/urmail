package reconcile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail"
)

// Cycle represents an atomic, complete reconciliation cycle.
// The DB snapshot, rendered files, and ApplyParams are inextricably bound together.
type Cycle struct {
	Snapshot    *mail.ConfigContext
	Files       map[string]string
	ApplyParams ApplyParams
}

// Pipeline triggers configuration re-rendering and service reloads when domains or mailboxes change.
type Pipeline struct {
	cfg         *config.Config
	querier     StoreQuerier
	publicIPv4  string
	nameservers []string
	log         *zap.Logger
	ensureCerts func(context.Context) error
	applier     func(context.Context, ApplyParams) (string, error)

	triggerCh   chan struct{}
	mu          sync.Mutex
	reconcileMu sync.Mutex
	running     bool

	// Render failure bookkeeping: render errors are logged AND aggregated so
	// callers/monitoring can observe that reconcile is failing instead of
	// the error being silently dropped (M3 review P2).
	failCount     int64
	lastError     error
	lastRender    time.Time
	lastContext   *mail.ConfigContext
	lastCycle     *Cycle
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

// WithApplier configures the privileged agent apply callback.
func (p *Pipeline) WithApplier(fn func(context.Context, ApplyParams) (string, error)) *Pipeline {
	p.applier = fn
	return p
}

// HasApplier reports whether an apply callback is configured.
func (p *Pipeline) HasApplier() bool {
	return p.applier != nil
}

// Trigger queues a reconcile execution. It is non-blocking (coalesces rapid events).
func (p *Pipeline) Trigger() {
	select {
	case p.triggerCh <- struct{}{}:
	default:
		// Already queued
	}
}

// RenderCycle executes one synchronous reconcile cycle: querying DB for the snapshot,
// rendering all configuration files, and constructing ApplyParams as an indivisible unit.
func (p *Pipeline) RenderCycle(ctx context.Context) (c *Cycle, err error) {
	start := time.Now()
	defer func() { observeRender(start, err) }()
	mailCtx, err := AssembleConfigContext(ctx, p.cfg, p.querier, p.publicIPv4, p.nameservers)
	if err != nil {
		return nil, fmt.Errorf("assemble config context: %w", err)
	}

	files, err := mail.RenderAll(*mailCtx)
	if err != nil {
		return nil, fmt.Errorf("render all configs: %w", err)
	}

	cycle := &Cycle{
		Snapshot:    mailCtx,
		Files:       files,
		ApplyParams: BuildApplyParams(mailCtx),
	}

	p.mu.Lock()
	p.lastContext = mailCtx
	p.lastCycle = cycle
	p.mu.Unlock()

	return cycle, nil
}

// RenderOnce executes one synchronous reconcile cycle, assembling DB snapshot and rendering configs.
func (p *Pipeline) RenderOnce(ctx context.Context) (files map[string]string, err error) {
	cycle, err := p.RenderCycle(ctx)
	if err != nil {
		return nil, err
	}
	return cycle.Files, nil
}

// ReconcileAndWait executes a complete, synchronized reconciliation cycle for the given domain.
// It verifies that the domain is included in the newly assembled snapshot, renders configuration,
// pushes the full ApplyParams (including SpamPolicies) through the configured applier,
// and returns the applied generation.
func (p *Pipeline) ReconcileAndWait(ctx context.Context, domain string) (string, error) {
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()

	cycle, err := p.RenderCycle(ctx)
	if err != nil {
		p.recordFailure(err)
		return "", err
	}

	if domain != "" {
		found := false
		for _, d := range cycle.Snapshot.PostfixSnap.Domains {
			if d.FQDN == domain {
				found = true
				break
			}
		}
		if !found {
			err = errors.New("ONBOARDING_DOMAIN_NOT_RENDERED")
			p.recordFailure(err)
			return "", err
		}
	}

	if p.applier == nil {
		err = errors.New("RECONCILE_APPLIER_NOT_CONFIGURED")
		p.recordFailure(err)
		return "", err
	}

	generation, err := p.applier(ctx, cycle.ApplyParams)
	if err != nil {
		p.recordFailure(err)
		return "", err
	}

	if p.ensureCerts != nil {
		if ensureErr := p.ensureCerts(ctx); ensureErr != nil {
			p.log.Warn("certificate ensure after bootstrap failed", zap.Error(ensureErr))
		}
	}

	p.recordSuccess()
	return generation, nil
}

func (p *Pipeline) recordFailure(err error) {
	p.mu.Lock()
	p.lastRender = time.Now()
	p.failCount++
	p.totalFailures++
	p.lastError = err
	failures := p.failCount
	p.mu.Unlock()
	observeApply(err, failures)
	p.log.Error("reconcile generation failed", zap.Error(err), zap.Int64("consecutive_failures", failures))
}

func (p *Pipeline) recordSuccess() {
	p.mu.Lock()
	p.lastRender = time.Now()
	p.failCount = 0
	p.lastError = nil
	p.mu.Unlock()
	observeApply(nil, 0)
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
	if p.lastCycle != nil {
		return p.lastCycle.Snapshot
	}
	return p.lastContext
}

// CurrentCycle returns the last completely rendered reconciliation cycle.
func (p *Pipeline) CurrentCycle() *Cycle {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastCycle
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

		err := func() error {
			p.reconcileMu.Lock()
			defer p.reconcileMu.Unlock()

			cycle, cycleErr := p.RenderCycle(ctx)
			if cycleErr != nil {
				return cycleErr
			}
			if p.applier != nil {
				if _, applyErr := p.applier(ctx, cycle.ApplyParams); applyErr != nil {
					return applyErr
				}
			}
			if onRendered != nil {
				if cbErr := onRendered(cycle.Files); cbErr != nil {
					return cbErr
				}
			}
			if p.ensureCerts != nil {
				if ensureErr := p.ensureCerts(ctx); ensureErr != nil {
					p.log.Warn("certificate ensure after bootstrap failed", zap.Error(ensureErr))
				}
			}
			return nil
		}()

		if err != nil {
			p.recordFailure(err)
			// A small deterministic jitter avoids synchronized fleet-wide retries.
			jitter := time.Duration(time.Now().UnixNano() % int64(delay/4+1))
			retry = time.After(delay + jitter)
			delay *= 2
			if delay > time.Minute {
				delay = time.Minute
			}
		} else {
			p.recordSuccess()
			delay = time.Second
		}
	}
}
