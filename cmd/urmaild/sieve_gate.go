package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/config"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/mail/sievegate"
)

// The socket is traversable by the mail UID, but each request is authenticated
// with SO_PEERCRED. The agent's private /run/urmail directory is never widened.
func startSieveGate(ctx context.Context, pool *db.Pool, cfg *config.Config, logger *zap.Logger) (func() bool, error) {
	// The configured mail identity is the same account the agent renders for
	// dovecot and the storage tree; config.Load already defaults it to mail.
	mailUID, err := resolveMailUID(cfg.MailUser)
	if err != nil {
		return nil, err
	}
	directory := filepath.Dir(sievegate.SocketPath)
	if err = os.MkdirAll(directory, 0755); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0005 != 0005 {
		return nil, errors.New("sieve gate runtime directory is not safely traversable")
	}
	if previous, e := os.Lstat(sievegate.SocketPath); e == nil {
		if previous.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("sieve gate path is not a socket")
		}
		dialer := net.Dialer{Timeout: 200 * time.Millisecond}
		probe, dialErr := dialer.DialContext(ctx, "unix", sievegate.SocketPath)
		if dialErr == nil {
			_ = probe.Close()
			return nil, errors.New("sieve gate listener is already active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("existing sieve gate socket is not demonstrably stale: %w", dialErr)
		}
		if err = os.Remove(sievegate.SocketPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: sievegate.SocketPath, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(sievegate.SocketPath, 0666); err != nil {
		_ = listener.Close()
		return nil, err
	}
	ready := &atomic.Bool{}
	ready.Store(true)
	q := db.Querier(pool)
	go func() {
		err := sievegate.Serve(ctx, listener, mailUID, func(callCtx context.Context, mailbox string) (bool, error) {
			platCtx := isolation.WithScope(callCtx, isolation.ScopeContext{Scope: isolation.ScopePlatform})
			return q.PlatformSieveOutboundAllowed(platCtx, mailbox)
		}, func(_ string, err error) { logger.Warn("personal Sieve outbound denied; inbound kept", zap.Error(err)) })
		ready.Store(false)
		if err != nil && ctx.Err() == nil {
			logger.Error("personal Sieve entitlement listener stopped; outbound fails closed", zap.Error(err))
		}
	}()
	return ready.Load, nil
}

// resolveMailUID turns the configured account name into the peer uid the gate
// accepts. Names, never magic numbers: an installation whose image carries
// only a numeric owner cannot be configured here by guessing.
func resolveMailUID(name string) (uint32, error) {
	account, err := user.Lookup(name)
	if err != nil {
		return 0, fmt.Errorf("resolve mail identity %q: %w", name, err)
	}
	uid, err := strconv.ParseInt(account.Uid, 10, 64)
	if err != nil {
		return 0, err
	}
	if uid < 0 || uid > math.MaxUint32 {
		return 0, errors.New("mail UID exceeds the credential range")
	}
	return uint32(uid), nil
}
