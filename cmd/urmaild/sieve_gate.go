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

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/mail/sievegate"
	"github.com/bushixiucai/urmail/internal/store"
)

// The socket is traversable by the mail UID, but each request is authenticated
// with SO_PEERCRED. The agent's private /run/urmail directory is never widened.
func startSieveGate(ctx context.Context, pool *db.Pool, logger *zap.Logger) (func() bool, error) {
	account, err := user.Lookup("mail")
	if err != nil {
		return nil, fmt.Errorf("resolve Dovecot mail identity: %w", err)
	}
	uid, err := strconv.ParseInt(account.Uid, 10, 64)
	if err != nil {
		return nil, err
	}
	if uid < 0 || uid > math.MaxUint32 {
		return nil, errors.New("dovecot mail UID exceeds the credential range")
	}
	mailUID := uint32(uid)
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
	q := store.New(pool)
	go func() {
		err := sievegate.Serve(ctx, listener, mailUID, q.PlatformSieveOutboundAllowed, func(_ string, err error) { logger.Warn("personal Sieve outbound denied; inbound kept", zap.Error(err)) })
		ready.Store(false)
		if err != nil && ctx.Err() == nil {
			logger.Error("personal Sieve entitlement listener stopped; outbound fails closed", zap.Error(err))
		}
	}()
	return ready.Load, nil
}
