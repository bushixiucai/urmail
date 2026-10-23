package agentd

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/bushixiucai/urmail/internal/mail/validation"
	"github.com/bushixiucai/urmail/internal/store"
)

// MailAccess describes a registered mailbox's current permission to write mail.
type MailAccess struct {
	Address string
	Allowed bool
}

type accessSnapshot func(context.Context) ([]MailAccess, time.Time, error)

// WatchMailAccess disconnects sessions when PostgreSQL revokes write access.
// The one-second recovery poll also observes administrative changes; scheduled
// subscription boundaries are checked immediately rather than waiting for it.
func WatchMailAccess(ctx context.Context, snapshot accessSnapshot, run CommandRunner, logger *slog.Logger) {
	previous := make(map[string]bool)
	pending := make(map[string]bool)
	delay := time.Duration(0)
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		queryCtx, cancel := context.WithTimeout(ctx, time.Second)
		rows, next, err := snapshot(queryCtx)
		cancel()
		delay = time.Second
		if err != nil {
			// A lost database cannot authorize continued writes by already connected users.
			for address := range previous {
				pending[address] = true
			}
			logger.Error("mail access refresh failed")
		} else {
			current := make(map[string]bool, len(rows))
			for _, row := range rows {
				if !safeKickAddress(row.Address) {
					logger.Error("unsafe registered mail access identity")
					continue
				}
				current[row.Address] = row.Allowed
				wasAllowed, known := previous[row.Address]
				if !row.Allowed && (!known || wasAllowed) {
					pending[row.Address] = true
				}
			}
			for address := range previous {
				if _, exists := current[address]; !exists {
					pending[address] = true
				}
			}
			previous = current
			if !next.IsZero() {
				if until := time.Until(next); until > 0 && until < delay {
					delay = until
				}
			}
		}
		// Every argument comes from the trusted database and is checked as an exact
		// non-glob address. No client-supplied username or command reaches this path.
		for address := range pending {
			kickCtx, stop := context.WithTimeout(ctx, time.Second)
			kickErr := run(kickCtx, "/usr/bin/doveadm", "kick", "--", address)
			stop()
			var exitErr *exec.ExitError
			if kickErr == nil || (errors.As(kickErr, &exitErr) && exitErr.ExitCode() == 68) {
				delete(pending, address)
			} else {
				logger.Error("mail access session disconnect failed")
			}
		}
	}
}

func safeKickAddress(address string) bool {
	parts := strings.Split(address, "@")
	return len(parts) == 2 && len(parts[0]) > 0 && len(parts[0]) <= 64 &&
		validation.LocalPartRegex.MatchString(parts[0]) && !strings.ContainsAny(parts[0], "*?\\/") &&
		validation.IsValidHostname(parts[1])
}

// StartMailAccessWatcher observes the same platform database used by the
// generated SQL userdb; no secondary entitlement store is introduced.
func (m *ApplyManager) StartMailAccessWatcher(ctx context.Context, logger *slog.Logger) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	q := store.New(m.pool.Pool)
	go func() {
		defer close(done)
		WatchMailAccess(ctx, func(queryCtx context.Context) ([]MailAccess, time.Time, error) {
			rows, err := q.PlatformMailboxAccessStates(queryCtx)
			if err != nil {
				return nil, time.Time{}, err
			}
			boundary, err := q.PlatformNextMailAccessBoundary(queryCtx)
			if err != nil {
				return nil, time.Time{}, err
			}
			states := make([]MailAccess, 0, len(rows))
			for _, row := range rows {
				states = append(states, MailAccess{Address: row.Address, Allowed: row.Allowed})
			}
			var next time.Time
			if boundary.Valid {
				next = boundary.Time
			}
			return states, next, nil
		}, m.run, logger)
	}()
	return func() { cancel(); <-done }
}
