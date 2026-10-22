package reconcile

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/mail"
)

// Pipeline triggers configuration re-rendering and service reloads when domains or mailboxes change.
type Pipeline struct {
	cfg        *config.Config
	querier    StoreQuerier
	publicIPv4 string
	nameservers []string

	triggerCh chan struct{}
	mu        sync.Mutex
	running   bool
}

// NewPipeline creates a reconcile trigger pipeline.
func NewPipeline(cfg *config.Config, q StoreQuerier, publicIPv4 string, nameservers []string) *Pipeline {
	return &Pipeline{
		cfg:         cfg,
		querier:     q,
		publicIPv4:  publicIPv4,
		nameservers: nameservers,
		triggerCh:   make(chan struct{}, 1),
	}
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
			if err != nil {
				// Log or handle error in caller
				continue
			}

			if onRendered != nil {
				_ = onRendered(files)
			}
		}
	}
}
