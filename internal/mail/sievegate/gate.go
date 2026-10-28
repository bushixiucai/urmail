// Package sievegate checks live outbound entitlement before personal Sieve runs.
package sievegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/validation"
)

// SocketPath shares the traversable policy runtime directory, never the privileged agent directory.
const SocketPath = "/run/urmail-policy/sieve-gate.sock"

// ErrDenied reports an explicit or fail-closed outbound restriction.
var ErrDenied = errors.New("personal sieve outbound entitlement unavailable")

// Checker evaluates the current resolved mailbox entitlement.
type Checker func(context.Context, string) (bool, error)
type request struct {
	Address string `json:"address"`
}
type response struct {
	Allowed bool `json:"allowed"`
}

// RunHelper accepts no mailbox argument. USER is supplied by Pigeonhole, whose
// execute extension is enabled only for the administrator-owned global script.
func RunHelper(args []string, getenv func(string) string) error {
	if len(args) != 0 {
		return errors.New("sieve helper accepts no arguments")
	}
	address := getenv("USER")
	if !validAddress(address) {
		return errors.New("invalid sieve interpreter principal")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return Check(ctx, address)
}
func validAddress(address string) bool {
	return len(address) <= 254 && strings.TrimSpace(address) == address && validation.IsValidEmail(address)
}

// Check obtains an explicit live decision over the authenticated local socket.
func Check(ctx context.Context, address string) error { return checkPath(ctx, SocketPath, address) }
func checkPath(ctx context.Context, path, address string) error {
	if !validAddress(address) {
		return ErrDenied
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	if err = json.NewEncoder(conn).Encode(request{Address: address}); err != nil {
		return err
	}
	var reply response
	if err = json.NewDecoder(io.LimitReader(conn, 1024)).Decode(&reply); err != nil {
		return err
	}
	if !reply.Allowed {
		return ErrDenied
	}
	return nil
}

// Serve authenticates the kernel peer before parsing the bounded request. The
// listener lifecycle and root-owned socket permissions belong to the daemon.
func Serve(ctx context.Context, listener *net.UnixListener, mailUID uint32, check Checker, observe func(string, error)) error {
	go func() { <-ctx.Done(); _ = listener.Close() }()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		// The bounded check is deliberately serial: a local sender cannot create an
		// unbounded number of database transactions or handler goroutines.
		serveOne(ctx, conn, mailUID, check, observe)
	}
}
func serveOne(parent context.Context, conn *net.UnixConn, uid uint32, check Checker, observe func(string, error)) {
	defer func() { _ = conn.Close() }()
	report := func(address string, err error) {
		if observe != nil {
			observe(address, err)
		}
	}
	if err := authorize(conn, uid); err != nil {
		report("", err)
		return
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		report("", err)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(conn, 1024))
	decoder.DisallowUnknownFields()
	var req request
	if err := decoder.Decode(&req); err != nil {
		report("", err)
		return
	}
	if !validAddress(req.Address) {
		report("", ErrDenied)
		return
	}
	allowed, err := check(ctx, req.Address)
	if err != nil || !allowed {
		if err == nil {
			err = ErrDenied
		}
		report(req.Address, err)
		allowed = false
	}
	if err = json.NewEncoder(conn).Encode(response{Allowed: allowed}); err != nil {
		report(req.Address, fmt.Errorf("write sieve gate result: %w", err))
	}
}
