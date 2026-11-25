package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultCertRenewInterval matches the legacy daily urmail-cert-renew.timer cadence.
const DefaultCertRenewInterval = 24 * time.Hour

// DefaultCertRenewMaxJitter spreads daily renewal checks across a randomized window.
const DefaultCertRenewMaxJitter = time.Hour

// CertScheduler runs daily ACME certificate renewal with randomized jitter
// inside urmaild-agent without relying on systemd timers.
type CertScheduler struct {
	Manager    *ApplyManager
	Interval   time.Duration
	MaxJitter  time.Duration
	Clock      func() time.Time
	RandInt64N func(int64) int64
	RenewFn    func(context.Context) error
	Logger     *slog.Logger
}

func (s *CertScheduler) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return DefaultCertRenewInterval
}

func (s *CertScheduler) jitter() time.Duration {
	maxJitter := s.MaxJitter
	if maxJitter < 0 {
		return 0
	}
	if maxJitter == 0 && s.MaxJitter == 0 && s.Interval == 0 {
		maxJitter = DefaultCertRenewMaxJitter
	}
	if maxJitter <= 0 {
		return 0
	}
	n := int64(maxJitter)
	if s.RandInt64N != nil {
		return time.Duration(s.RandInt64N(n))
	}
	return time.Duration(rand.Int64N(n)) // #nosec G404 -- non-cryptographic jitter for renewal scheduling.
}

// Start launches the daily certificate renewal loop and returns a stop function.
func (s *CertScheduler) Start(ctx context.Context) func() {
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	interval := s.interval()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if delay := s.jitter(); delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-loopCtx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
				_ = s.RunOnce(loopCtx)
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// RunOnce executes a single certificate renewal pass.
func (s *CertScheduler) RunOnce(ctx context.Context) error {
	if s.RenewFn != nil {
		return s.RenewFn(ctx)
	}
	if s.Manager == nil || !s.Manager.cfg.BackgroundWorkersAllowed() {
		return nil
	}
	_ = ctx
	_, _, err := s.Manager.RenewCertificates(json.RawMessage(`{}`))
	if err != nil && s.Logger != nil {
		s.Logger.Warn("scheduled certificate renewal failed", "error", err)
	}
	return err
}

// reloadComposeCertificates notifies Postfix and Dovecot containers of
// updated TLS material in RenderedConfigDir without invoking systemd.
func (m *ApplyManager) reloadComposeCertificates(domain string) error {
	renderDir := filepath.Clean(strings.TrimSpace(m.cfg.RenderedConfigDir))
	stamp := []byte(fmt.Sprintf("%s %s\n", domain, time.Now().UTC().Format(time.RFC3339Nano)))
	for _, svc := range []string{"postfix", "dovecot"} {
		dir := filepath.Join(renderDir, svc)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("prepare %s reload stamp dir: %w", svc, err)
		}
		if _, err := writeFileAtomic(filepath.Join(dir, ".cert-reload-stamp"), stamp, 0o644, []string{renderDir}); err != nil {
			return fmt.Errorf("write %s cert reload stamp: %w", svc, err)
		}
	}
	return nil
}
