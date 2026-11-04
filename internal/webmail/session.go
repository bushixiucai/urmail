package webmail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var vaultCapacityRejections = promauto.NewCounter(prometheus.CounterOpts{
	Name: "urmail_webmail_vault_capacity_rejections_total",
	Help: "Session or schedule credentials rejected because a process or per-mailbox vault capacity limit was reached.",
})

// SessionStatus represents the outcome of verifying a session against the authoritative store.
type SessionStatus int

const (
	// Valid means the session is confirmed valid in the database.
	Valid SessionStatus = iota
	// RevokedOrInvalid means the session was explicitly revoked, deleted,
	// or the user was disabled (database confirmed sql.ErrNoRows or pgx.ErrNoRows).
	RevokedOrInvalid
	// InfrastructureUnavailable means transient infrastructure failure
	// (connection lost, pool exhausted, timeout, PG restarting, etc.).
	InfrastructureUnavailable
)

// Aliases for explicit reference matching the architectural remediation plan.
const (
	SessionValid                     = Valid
	SessionRevokedOrInvalid          = RevokedOrInvalid
	SessionInfrastructureUnavailable = InfrastructureUnavailable
)

func (s SessionStatus) String() string {
	switch s {
	case Valid:
		return "Valid"
	case RevokedOrInvalid:
		return "RevokedOrInvalid"
	case InfrastructureUnavailable:
		return "InfrastructureUnavailable"
	default:
		return fmt.Sprintf("SessionStatus(%d)", int(s))
	}
}

// ErrRevoked indicates that a session has been revoked.
var ErrRevoked = errors.New("session revoked")

// ClassifySessionLookup explicitly classifies a session query result into:
// - Valid (session is active and valid)
// - RevokedOrInvalid (session explicitly revoked, deleted, or expired, confirmed sql.ErrNoRows / pgx.ErrNoRows)
// - InfrastructureUnavailable (connection lost, pool exhausted, timeout context.DeadlineExceeded, PG restarting, etc.)
func ClassifySessionLookup(sess Session, err error, now time.Time) SessionStatus {
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) ||
			errors.Is(err, pgx.ErrNoRows) ||
			errors.Is(err, ErrRevoked) ||
			strings.EqualFold(err.Error(), "revoked") ||
			strings.EqualFold(err.Error(), "session revoked") {
			return RevokedOrInvalid
		}
		return InfrastructureUnavailable
	}
	if sess.TokenHash == "" {
		return RevokedOrInvalid
	}
	if !sess.ExpiresAt.IsZero() && !now.IsZero() && !now.Before(sess.ExpiresAt) {
		return RevokedOrInvalid
	}
	return Valid
}

// Session is a mailbox principal, independent of panel administrative roles.
type Session struct {
	OriginHost      string
	TokenHash       string
	TenantID        int64
	MailboxID       int64
	Address         string
	PasswordVersion string
	ExpiresAt       time.Time
}

// CredentialVault stores only process-local credentials.
type CredentialVault interface {
	Put(string, string)
	Get(string) (string, bool)
	Delete(string)
}

// SessionVault binds password retention to the authenticated session lifetime.
type SessionVault interface {
	PutSession(Session, string) error
	DeleteMailbox(int64, int64)
}

// ScheduleSealer is an optional short-lived cache; encrypted PG state is authoritative.
type ScheduleSealer interface {
	PutSchedule(int64, string)
	GetSchedule(int64) (string, bool)
	DeleteSchedule(int64)
}

type vaultEntry struct {
	password   string
	session    Session
	generation uint64
}

type scheduleEntry struct {
	password string
	expires  time.Time
}

// MemoryVault bounds abandoned credentials by time, mailbox and process capacity.
type MemoryVault struct {
	mu                     sync.Mutex
	byID                   map[string]vaultEntry
	bySchedID              map[int64]scheduleEntry
	now                    func() time.Time
	maxEntries             int
	maxMailbox             int
	nextGen                uint64
	itemTimeout            time.Duration
	sweepInterval          time.Duration
	minBackoff             time.Duration
	maxBackoff             time.Duration
	maxConsecutiveFailures int
}

// NewMemoryVault creates a bounded process-local credential store.
func NewMemoryVault() *MemoryVault {
	return &MemoryVault{
		byID:                   make(map[string]vaultEntry),
		bySchedID:              make(map[int64]scheduleEntry),
		now:                    time.Now,
		maxEntries:             1000,
		maxMailbox:             5,
		itemTimeout:            2 * time.Second,
		sweepInterval:          time.Minute,
		minBackoff:             50 * time.Millisecond,
		maxBackoff:             500 * time.Millisecond,
		maxConsecutiveFailures: 3,
	}
}

// Put is retained for test adapters; production uses PutSession with the actual expiry.
func (v *MemoryVault) Put(token, password string) {
	if v != nil {
		now := v.now
		if now == nil {
			now = time.Now
		}
		_ = v.PutSession(Session{TokenHash: token, ExpiresAt: now().Add(30 * time.Minute)}, password)
	}
}

// PutSession refuses capacity exhaustion without evicting an active credential.
// It assigns an incrementing entryGeneration to defeat ABA races with concurrent sweeps.
func (v *MemoryVault) PutSession(session Session, password string) error {
	if v == nil || session.TokenHash == "" || password == "" {
		return errors.New("invalid credential vault entry")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked()
	now := v.now
	if now == nil {
		now = time.Now
	}
	if !now().Before(session.ExpiresAt) {
		return errors.New("expired credential vault entry")
	}
	count := 0
	for key, e := range v.byID {
		if key != session.TokenHash && e.session.TenantID == session.TenantID && e.session.MailboxID == session.MailboxID {
			count++
		}
	}
	if _, exists := v.byID[session.TokenHash]; !exists && (len(v.byID) >= v.maxEntries || count >= v.maxMailbox) {
		vaultCapacityRejections.Inc()
		return errors.New("credential vault capacity exhausted")
	}
	v.nextGen++
	v.byID[session.TokenHash] = vaultEntry{
		password:   password,
		session:    session,
		generation: v.nextGen,
	}
	return nil
}

// Get returns a credential only while its retention period remains valid.
func (v *MemoryVault) Get(token string) (string, bool) {
	if v == nil {
		return "", false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked()
	e, ok := v.byID[token]
	return e.password, ok
}

// Generation returns the generation counter for the session token.
func (v *MemoryVault) Generation(token string) (uint64, bool) {
	if v == nil {
		return 0, false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.byID[token]
	return e.generation, ok
}

// Delete removes the credential associated with one session token unconditionally.
func (v *MemoryVault) Delete(token string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.byID, token)
}

// DeleteIfGeneration removes the credential only if its generation matches, avoiding ABA races.
func (v *MemoryVault) DeleteIfGeneration(token string, generation uint64) bool {
	if v == nil {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if e, ok := v.byID[token]; ok {
		if e.generation == generation {
			delete(v.byID, token)
			return true
		}
	}
	return false
}

// DeleteMailbox invalidates all retained sessions of the exact tenant mailbox.
func (v *MemoryVault) DeleteMailbox(tenant, mailbox int64) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for token, e := range v.byID {
		if e.session.TenantID == tenant && e.session.MailboxID == mailbox {
			delete(v.byID, token)
		}
	}
}

func (v *MemoryVault) pruneLocked() {
	now := v.now
	if now == nil {
		now = time.Now
	}
	cur := now()
	for token, e := range v.byID {
		if !cur.Before(e.session.ExpiresAt) {
			delete(v.byID, token)
		}
	}
	for id, e := range v.bySchedID {
		if !cur.Before(e.expires) {
			delete(v.bySchedID, id)
		}
	}
}

// SetItemTimeout configures the per-entry verification timeout for Sweep.
func (v *MemoryVault) SetItemTimeout(d time.Duration) {
	if v != nil {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.itemTimeout = d
	}
}

// SetSweepInterval configures the normal sweep interval for Run.
func (v *MemoryVault) SetSweepInterval(d time.Duration) {
	if v != nil {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.sweepInterval = d
	}
}

// SetBackoff configures the polling backoff parameters.
func (v *MemoryVault) SetBackoff(minBackoff, maxBackoff time.Duration, maxFailures int) {
	if v != nil {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.minBackoff = minBackoff
		v.maxBackoff = maxBackoff
		v.maxConsecutiveFailures = maxFailures
	}
}

// SetNow overrides the current time provider.
func (v *MemoryVault) SetNow(now func() time.Time) {
	if v != nil {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.now = now
	}
}

// Run periodically discards expired or database-revoked credentials, even if unused.
// It incorporates exponential backoff when infrastructure failures are encountered.
func (v *MemoryVault) Run(ctx context.Context, lookup func(context.Context, string) (Session, error)) {
	interval := v.sweepInterval
	if interval <= 0 {
		interval = time.Minute
	}
	defer func() {
		v.mu.Lock()
		clear(v.byID)
		clear(v.bySchedID)
		v.mu.Unlock()
	}()

	runBackoff := time.Duration(0)
	for {
		waitDuration := interval
		if runBackoff > 0 {
			waitDuration = runBackoff
		}
		timer := time.NewTimer(waitDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			report := v.Sweep(ctx, lookup)
			if report.InfrastructureErrors > 0 || report.Aborted {
				// Infrastructure is degraded/restarting; back off next run
				if runBackoff == 0 {
					runBackoff = 5 * time.Second
				} else {
					runBackoff *= 2
					if runBackoff > 2*time.Minute {
						runBackoff = 2 * time.Minute
					}
				}
			} else {
				runBackoff = 0
			}
		}
	}
}

// PutSchedule retains an optional schedule cache entry for at most one minute.
func (v *MemoryVault) PutSchedule(id int64, password string) {
	if v == nil || id <= 0 || password == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked()
	if len(v.bySchedID) >= v.maxEntries {
		vaultCapacityRejections.Inc()
		return
	}
	now := v.now
	if now == nil {
		now = time.Now
	}
	v.bySchedID[id] = scheduleEntry{password: password, expires: now().Add(time.Minute)}
}

// GetSchedule returns a schedule credential while its cache entry is valid.
func (v *MemoryVault) GetSchedule(id int64) (string, bool) {
	if v == nil {
		return "", false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked()
	e, ok := v.bySchedID[id]
	return e.password, ok
}

// DeleteSchedule removes a terminated schedule cache entry.
func (v *MemoryVault) DeleteSchedule(id int64) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.bySchedID, id)
}

type sweepTarget struct {
	token           string
	generation      uint64
	passwordVersion string
}

// SweepReport summarizes the outcome of a revocation sweep pass.
type SweepReport struct {
	TotalChecked         int
	ValidCount           int
	RevokedCount         int
	InfrastructureErrors int
	Aborted              bool
}

// Sweep performs one bounded revocation pass without retaining the mutex over I/O.
// Each entry has an independent fine-grained context timeout (e.g. 2-3s).
// Transient infrastructure failures trigger polling backoff and fail-closed retention.
// Entries are deleted only if generation matches, avoiding ABA races.
func (v *MemoryVault) Sweep(ctx context.Context, lookup func(context.Context, string) (Session, error)) SweepReport {
	var report SweepReport
	if v == nil {
		return report
	}
	v.mu.Lock()
	v.pruneLocked()
	targets := make([]sweepTarget, 0, len(v.byID))
	for token, e := range v.byID {
		if e.session.MailboxID > 0 {
			targets = append(targets, sweepTarget{
				token:           token,
				generation:      e.generation,
				passwordVersion: e.session.PasswordVersion,
			})
		}
	}
	v.mu.Unlock()
	if lookup == nil {
		return report
	}

	timeout := v.itemTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	minBackoff := v.minBackoff
	if minBackoff <= 0 {
		minBackoff = 50 * time.Millisecond
	}
	maxBackoff := v.maxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 500 * time.Millisecond
	}
	maxConsecutiveFailures := v.maxConsecutiveFailures
	if maxConsecutiveFailures <= 0 {
		maxConsecutiveFailures = 3
	}

	consecutiveFailures := 0
	currentBackoff := minBackoff

	nowFn := v.now
	if nowFn == nil {
		nowFn = time.Now
	}

	for _, target := range targets {
		select {
		case <-ctx.Done():
			report.Aborted = true
			return report
		default:
		}

		itemCtx, itemCancel := context.WithTimeout(ctx, timeout)
		sess, err := lookup(itemCtx, target.token)
		itemCancel()

		report.TotalChecked++
		status := ClassifySessionLookup(sess, err, nowFn())
		if status == Valid && target.passwordVersion != "" && sess.PasswordVersion != "" && sess.PasswordVersion != target.passwordVersion {
			status = RevokedOrInvalid
		}

		switch status {
		case Valid:
			consecutiveFailures = 0
			currentBackoff = minBackoff
			report.ValidCount++

		case RevokedOrInvalid:
			consecutiveFailures = 0
			currentBackoff = minBackoff
			report.RevokedCount++
			v.DeleteIfGeneration(target.token, target.generation)

		case InfrastructureUnavailable:
			consecutiveFailures++
			report.InfrastructureErrors++
			// Fail-closed policy: credentials remain safe in vault until TTL. Never clear!
			if consecutiveFailures >= maxConsecutiveFailures {
				// Abort this sweep pass to avoid avalanche on degraded infrastructure.
				report.Aborted = true
				return report
			}
			// Polling backoff between items
			t := time.NewTimer(currentBackoff)
			select {
			case <-ctx.Done():
				t.Stop()
				report.Aborted = true
				return report
			case <-t.C:
			}
			currentBackoff *= 2
			if currentBackoff > maxBackoff {
				currentBackoff = maxBackoff
			}
		}
	}
	return report
}
