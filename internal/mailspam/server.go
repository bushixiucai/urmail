// Listener lifecycle for the spam verdict milter.
//
// The server binds a loopback TCP endpoint (default 127.0.0.1:11339) that the
// generated Postfix smtpd_milters chain references AFTER the OpenDKIM/OpenDMARC
// milters. Postfix carries `milter_default_action = accept` in the generated
// main.cf, so when this process is down or restarting, mail is ACCEPTED and
// delivered unflagged: that is the documented temporary-degrade policy, and
// the operational alert below exists so the degrade is never silent.

package mailspam

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/bushixiucai/urmail/internal/telemetry"
)

// DefaultAddr is the loopback endpoint referenced by the generated Postfix
// configuration (internal/mail/postfix/maincf.go). Changing one requires
// changing the other.
const DefaultAddr = "127.0.0.1:11339"

// AlertKind is the bounded operational alert kind fired when the milter is
// unavailable while Postfix references it. It maps to warning severity in
// internal/telemetry (mail keeps flowing; visibility is the point).
const AlertKind = "spam_milter_unavailable"

// Server accepts milter connections and classifies messages through h.
type Server struct {
	// Addr is the listen address; empty selects DefaultAddr.
	Addr string
	// Handler classifies one message.
	Handler VerdictHandler
	// Config tunes the wire layer (deny actions default OFF).
	Config MilterConfig
	// IdleTimeout overrides the per-read deadline when set.
	IdleTimeout time.Duration

	mu       sync.Mutex
	listener net.Listener
	running  bool
}

// ListenAndServe binds and serves until ctx is canceled or the listener
// fails. A bind failure publishes the unavailable alert before returning:
// the generated Postfix configuration references this endpoint, so a failed
// bind means the deployment is silently degraded.
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr := s.Addr
	if addr == "" {
		addr = DefaultAddr
	}
	cfg := s.Config.withDefaults()
	if s.IdleTimeout > 0 {
		cfg.IdleTimeout = s.IdleTimeout
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		publishUnavailable(true)
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.running = true
	s.mu.Unlock()
	publishUnavailable(false)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			publishUnavailable(true)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go func() {
			_ = serveConn(ctx, conn, cfg, s.Handler)
		}()
	}
}

// Close stops a running listener.
func (s *Server) Close() error {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.running = false
	s.mu.Unlock()
	if ln == nil {
		return nil
	}
	publishUnavailable(true)
	return ln.Close()
}

// Running reports whether the listener is currently accepting.
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// publishUnavailable drives both the prometheus gauge and the shared
// operational alert kind. Firing means: Postfix accepts mail unflagged
// (milter_default_action = accept) and the closed loop is degraded.
func publishUnavailable(firing bool) {
	setMilterAvailable(!firing)
	telemetry.PublishAlertSeverity(AlertKind, firing, telemetry.SeverityWarning)
}
