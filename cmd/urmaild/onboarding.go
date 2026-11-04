package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/agent"
	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/onboarding"
	"github.com/bushixiucai/urmail/internal/reconcile"
)

// startDomainOnboarding returns API preparation dependencies even while the
// restore gate prevents all background writes and privileged operations.
func startDomainOnboarding(ctx context.Context, cfg *config.Config, pool *db.Pool, secrets onboarding.SecretBox, pipeline *reconcile.Pipeline, logger *zap.Logger) *onboarding.Service {
	hostname := cfg.MyHostname
	if hostname == "" {
		hostname = "box." + cfg.PrimaryDomain
	}
	var trigger func()
	if pipeline != nil {
		trigger = pipeline.Trigger
	}
	service := &onboarding.Service{Pool: pool, Secrets: secrets, Hostname: hostname, AfterChange: trigger}
	if !cfg.BackgroundWorkersAllowed() {
		return service
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		logger.Error("onboarding worker identity unavailable")
		return service
	}
	owner := hex.EncodeToString(random[:])
	client := agent.NewClient(cfg.AgentSocket)
	if pipeline != nil && !pipeline.HasApplier() {
		pipeline.WithApplier(func(callCtx context.Context, params reconcile.ApplyParams) (string, error) {
			response, err := client.Do(callCtx, agentd.OpConfigApply, params)
			if err != nil {
				return "", err
			}
			return response.Stdout, nil
		})
	}
	apply := newOnboardingApply(pipeline)
	worker := onboarding.NewWorker(onboarding.WorkerOptions{Service: service, Agent: client, Apply: apply, Nameservers: cfg.Nameservers, Owner: owner})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := service.DNSOnce(ctx, owner); err != nil {
					logger.Warn("DNS plan requires reconciliation")
				}
				if _, err := worker.Once(ctx); err != nil {
					logger.Warn("domain onboarding dependency not ready")
				}
			}
		}
	}()
	return service
}

func newOnboardingApply(pipeline *reconcile.Pipeline) func(context.Context, string) (string, error) {
	return func(callCtx context.Context, domain string) (string, error) {
		if pipeline == nil {
			return "", errors.New("ONBOARDING_PIPELINE_UNAVAILABLE")
		}
		return pipeline.ReconcileAndWait(callCtx, domain)
	}
}
