package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/agentd"
	"github.com/bushixiucai/urmail/internal/store"
)

// Agent is the existing typed privileged socket contract.
type Agent interface {
	Do(context.Context, string, any) (*agentd.Response, error)
}

// Resolver only exposes DNS reads required by a confirmed mail configuration.
type Resolver interface {
	LookupMX(context.Context, string) ([]*net.MX, error)
	LookupTXT(context.Context, string) ([]string, error)
	LookupCNAME(context.Context, string) (string, error)
	LookupNS(context.Context, string) ([]*net.NS, error)
}

// WorkerOptions injects real generation application and protocol probes through
// the existing root agent. A successful render alone cannot mark a task ready.
type WorkerOptions struct {
	Service     *Service
	Agent       Agent
	Apply       func(context.Context, string) (string, error)
	Resolver    Resolver
	Nameservers []string
	Owner       string
}

// Worker owns no durable queue; every transition is fenced in PostgreSQL.
type Worker struct{ opts WorkerOptions }

// NewWorker configures an independent public resolver so a platform-managed
// local zone cannot falsely prove public propagation before NS delegation.
func NewWorker(opts WorkerOptions) *Worker {
	if opts.Resolver == nil {
		opts.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dial net.Dialer
			return dial.DialContext(ctx, network, "1.1.1.1:53")
		}}
	}
	return &Worker{opts: opts}
}

// Once claims one paid/trial/explicit-free domain and advances one durable step.
func (w *Worker) Once(ctx context.Context) (bool, error) {
	s := w.opts.Service
	q := store.New(s.Pool.Pool)
	if _, err := q.PlatformExpireOnboarding(ctx); err != nil {
		return false, err
	}
	task, err := q.PlatformClaimOnboarding(ctx, text(w.opts.Owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lost atomic.Bool
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-workCtx.Done():
				return
			case <-ticker.C:
				n, e := q.PlatformRenewOnboardingLease(workCtx, store.PlatformRenewOnboardingLeaseParams{ID: task.ID, Fence: task.Fence, LeasedBy: task.LeasedBy})
				if e != nil || n != 1 {
					lost.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	started := time.Now()
	step, generation, complete, err := w.step(workCtx, task)
	observeWork("onboarding", task.Step, started, err)
	if lost.Load() {
		return true, errFence
	}
	if complete && err == nil {
		return true, nil
	}
	status, code, backoff := "pending", "", float64(0)
	if err != nil {
		code = err.Error()
		if len(code) > 80 || strings.ContainsAny(code, " \r\n/") {
			code = "ONBOARDING_DEPENDENCY_UNAVAILABLE"
		}
		step = task.Step
		generation = task.DesiredGeneration.String
		backoff = 30
		for i := int32(1); i < task.Attempts && backoff < 3600; i++ {
			backoff = min(backoff*2, 3600)
		}
		if task.Attempts >= task.MaxAttempts {
			status = "failed"
		}
	}
	tx, txErr := s.Pool.PlatformTx(ctx)
	if txErr != nil {
		return true, txErr
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tq := store.New(tx)
	n, txErr := tq.PlatformAdvanceOnboarding(ctx, store.PlatformAdvanceOnboardingParams{ID: task.ID, Fence: task.Fence, LeasedBy: task.LeasedBy, Step: step, Status: status, LastErrorCode: text(code), DesiredGeneration: text(generation), BackoffSeconds: backoff})
	if txErr != nil || n != 1 {
		return true, errFence
	}
	if txErr = dnsAudit(ctx, tq, task.TenantID, "onboarding."+status, strconv.FormatInt(task.DomainID.Int64, 10), map[string]any{"task_id": task.ID, "step": step, "error_code": code}); txErr != nil {
		return true, txErr
	}
	if txErr = tx.Commit(ctx); txErr != nil {
		return true, txErr
	}
	return true, err
}
func (w *Worker) step(ctx context.Context, task store.OnboardingTasks) (string, string, bool, error) {
	s := w.opts.Service
	q := store.New(s.Pool.Pool)
	d, err := q.PlatformDNSDomain(ctx, store.PlatformDNSDomainParams{TenantID: task.TenantID, ID: task.DomainID.Int64})
	if err != nil {
		return task.Step, "", false, errors.New("ONBOARDING_DOMAIN_UNAVAILABLE")
	}
	step, generation := task.Step, task.DesiredGeneration.String
	if step == "domain_verification" || (step == "dns_propagation" && generation == "") {
		step = "bootstrap_config"
	}
	switch step {
	case "bootstrap_config":
		if w.opts.Apply == nil {
			return step, generation, false, errors.New("ONBOARDING_AGENT_UNAVAILABLE")
		}
		if err = q.PlatformPrepareOnboardingTenant(ctx, task.TenantID); err != nil {
			return step, generation, false, err
		}
		generation, err = w.opts.Apply(ctx, d.Fqdn)
		if err != nil || generation == "" {
			return step, generation, false, errors.New("ONBOARDING_BOOTSTRAP_NOT_APPLIED")
		}
		return "dns_propagation", generation, false, nil
	case "dns_propagation":
		if err = w.verifyDNS(ctx, d); err != nil {
			return step, generation, false, err
		}
		return "certificate_issue", generation, false, nil
	case "certificate_issue":
		if w.opts.Agent == nil {
			return step, generation, false, errors.New("ONBOARDING_AGENT_UNAVAILABLE")
		}
		trusted, err := w.certificate(ctx)
		if err != nil {
			return step, generation, false, err
		}
		if !trusted {
			if _, err = w.opts.Agent.Do(ctx, agentd.OpCertificateIssue, agentd.CertificateParams{Domain: s.Hostname}); err != nil {
				return step, generation, false, errors.New("ONBOARDING_CERTIFICATE_UNAVAILABLE")
			}
			trusted, err = w.certificate(ctx)
			if err != nil || !trusted {
				return step, generation, false, errors.New("ONBOARDING_CERTIFICATE_UNTRUSTED")
			}
		}
		return "mailbox_provision", generation, false, nil
	case "mailbox_provision":
		return "configuration_apply", generation, false, nil
	case "configuration_apply":
		generation, err = w.opts.Apply(ctx, d.Fqdn)
		if err != nil || generation == "" {
			return step, generation, false, errors.New("ONBOARDING_GENERATION_UNAPPLIED")
		}
		return "protocol_readiness", generation, false, nil
	case "protocol_readiness":
		if err = w.verifyDNS(ctx, d); err != nil {
			return step, generation, false, err
		}
		trusted, e := w.certificate(ctx)
		if e != nil || !trusted {
			return step, generation, false, errors.New("ONBOARDING_CERTIFICATE_UNTRUSTED")
		}
		applied, e := w.opts.Apply(ctx, d.Fqdn)
		if e != nil || applied == "" {
			return step, generation, false, errors.New("ONBOARDING_PROTOCOL_NOT_READY")
		}
		response, e := w.opts.Agent.Do(ctx, agentd.OpConfigStatus, struct{}{})
		if e != nil {
			return step, generation, false, errors.New("ONBOARDING_PROTOCOL_NOT_READY")
		}
		var status agentd.ApplyStatus
		if json.Unmarshal([]byte(response.Stdout), &status) != nil || !status.Ready || status.Degraded || status.Desired != applied || status.Applied != applied {
			return step, generation, false, errors.New("ONBOARDING_GENERATION_MISMATCH")
		}
		tx, e := s.Pool.PlatformTx(ctx)
		if e != nil {
			return step, generation, false, e
		}
		defer func() { _ = tx.Rollback(ctx) }()
		tq := store.New(tx)
		if _, e = tq.PlatformFenceOnboarding(ctx, store.PlatformFenceOnboardingParams{ID: task.ID, Fence: task.Fence, LeasedBy: task.LeasedBy}); e != nil {
			return step, generation, false, errFence
		}
		n, e := tq.PlatformMarkDomainReady(ctx, store.PlatformMarkDomainReadyParams{TenantID: task.TenantID, ID: task.DomainID.Int64, ID_2: task.ID, Fence: task.Fence, LeasedBy: task.LeasedBy})
		if e != nil || n != 1 {
			return step, generation, false, errFence
		}
		allowed, e := tq.PlatformInboundAllowed(ctx, "postmaster@"+d.Fqdn)
		if e != nil || !allowed {
			return step, generation, false, errors.New("ONBOARDING_ENTITLEMENT_NOT_READY")
		}
		n, e = tq.PlatformAdvanceOnboarding(ctx, store.PlatformAdvanceOnboardingParams{ID: task.ID, Fence: task.Fence, LeasedBy: task.LeasedBy, Step: "ready", Status: "completed", DesiredGeneration: text(applied)})
		if e != nil || n != 1 {
			return step, generation, false, errFence
		}
		if e = dnsAudit(ctx, tq, task.TenantID, "onboarding.ready", strconv.FormatInt(task.DomainID.Int64, 10), map[string]any{"task_id": task.ID, "generation": applied, "mail_hostname": s.Hostname}); e != nil {
			return step, generation, false, e
		}
		return "ready", applied, true, tx.Commit(ctx)
	case "ready":
		return step, generation, false, errors.New("ONBOARDING_READY_REQUIRES_VALIDATION")
	default:
		return step, generation, false, errors.New("ONBOARDING_STEP_UNKNOWN")
	}
}
func (w *Worker) certificate(ctx context.Context) (bool, error) {
	if w.opts.Agent == nil {
		return false, errors.New("ONBOARDING_AGENT_UNAVAILABLE")
	}
	response, err := w.opts.Agent.Do(ctx, agentd.OpCertificateStatus, agentd.CertificateParams{Domain: w.opts.Service.Hostname})
	if err != nil {
		return false, errors.New("ONBOARDING_CERTIFICATE_UNAVAILABLE")
	}
	var status agentd.CertificateStatus
	if json.Unmarshal([]byte(response.Stdout), &status) != nil || status.Domain != w.opts.Service.Hostname {
		return false, errors.New("ONBOARDING_CERTIFICATE_INVALID")
	}
	return status.Trusted && status.ExpiresAt.After(time.Now().Add(time.Hour)), nil
}
func (w *Worker) verifyDNS(ctx context.Context, d store.PlatformDNSDomainRow) error {
	_, desired, err := DomainDigest(d, w.opts.Service.Hostname)
	if err != nil {
		return err
	}
	if d.Mode == "managed" {
		if len(w.opts.Nameservers) == 0 {
			return errors.New("ONBOARDING_NS_CONFIG_UNAVAILABLE")
		}
		if !d.DnsExplicit {
			return errors.New("ONBOARDING_MANAGED_DNS_CONFIRMATION_REQUIRED")
		}
		nameservers, e := w.opts.Resolver.LookupNS(ctx, d.Fqdn)
		if e != nil {
			return errors.New("ONBOARDING_NS_NOT_PROPAGATED")
		}
		seen := map[string]bool{}
		for _, ns := range nameservers {
			seen[strings.TrimSuffix(strings.ToLower(ns.Host), ".")] = true
		}
		for _, ns := range w.opts.Nameservers {
			if !seen[strings.TrimSuffix(strings.ToLower(ns), ".")] {
				return errors.New("ONBOARDING_NS_NOT_PROPAGATED")
			}
		}
	}
	for _, r := range desired {
		switch r.Type {
		case "MX":
			rows, e := w.opts.Resolver.LookupMX(ctx, r.Name)
			if e != nil || len(rows) != 1 || !strings.EqualFold(strings.TrimSuffix(rows[0].Host, "."), strings.TrimSuffix(r.Value, ".")) || int(rows[0].Pref) != r.Priority {
				return errors.New("ONBOARDING_MX_NOT_PROPAGATED")
			}
		case "CNAME":
			target, e := w.opts.Resolver.LookupCNAME(ctx, r.Name)
			if e != nil || !strings.EqualFold(strings.TrimSuffix(target, "."), strings.TrimSuffix(r.Value, ".")) {
				return errors.New("ONBOARDING_AUTOCONFIG_NOT_PROPAGATED")
			}
		case "TXT":
			values, e := w.opts.Resolver.LookupTXT(ctx, r.Name)
			if e != nil {
				return errors.New("ONBOARDING_TXT_NOT_PROPAGATED")
			}
			matched := false
			count := 0
			for _, value := range values {
				if strings.HasPrefix(strings.ToLower(r.Value), "v=dmarc1") && strings.HasPrefix(strings.ToLower(value), "v=dmarc1") {
					count++
				}
				if strings.HasPrefix(strings.ToLower(r.Value), "v=spf1") {
					if strings.HasPrefix(strings.ToLower(value), "v=spf1") {
						count++
						for _, term := range strings.Fields(value) {
							if term == "mx" || term == "+mx" {
								matched = true
							}
						}
					}
				} else if value == r.Value || (strings.HasPrefix(strings.ToLower(r.Value), "v=dmarc1") && validDMARC(value)) {
					matched = true
				}
			}
			if !matched || count > 1 {
				return errors.New("ONBOARDING_TXT_NOT_PROPAGATED")
			}
		}
	}
	return nil
}
