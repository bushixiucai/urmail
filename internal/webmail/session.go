package webmail

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var vaultCapacityRejections = promauto.NewCounter(prometheus.CounterOpts{
	Name: "urmail_webmail_vault_capacity_rejections_total",
	Help: "Session or schedule credentials rejected because a process or per-mailbox vault capacity limit was reached.",
})

// Session is a mailbox principal, independent of panel administrative roles.
type Session struct {
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
	password string
	session  Session
}
type scheduleEntry struct {
	password string
	expires  time.Time
}

// MemoryVault bounds abandoned credentials by time, mailbox and process capacity.
type MemoryVault struct {
	mu         sync.Mutex
	byID       map[string]vaultEntry
	bySchedID  map[int64]scheduleEntry
	now        func() time.Time
	maxEntries int
	maxMailbox int
}

// NewMemoryVault creates a bounded process-local credential store.
func NewMemoryVault() *MemoryVault {
	return &MemoryVault{byID: make(map[string]vaultEntry), bySchedID: make(map[int64]scheduleEntry), now: time.Now, maxEntries: 1000, maxMailbox: 5}
}

// Put is retained for test adapters; production uses PutSession with the actual expiry.
func (v *MemoryVault) Put(token, password string) {
	if v != nil {
		_ = v.PutSession(Session{TokenHash: token, ExpiresAt: v.now().Add(30 * time.Minute)}, password)
	}
}

// PutSession refuses capacity exhaustion without evicting an active credential.
func (v *MemoryVault) PutSession(session Session, password string) error {
	if v == nil || session.TokenHash == "" || password == "" {
		return errors.New("invalid credential vault entry")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked()
	if !v.now().Before(session.ExpiresAt) {
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
	v.byID[session.TokenHash] = vaultEntry{password: password, session: session}
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

// Delete removes the credential associated with one session token.
func (v *MemoryVault) Delete(token string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.byID, token)
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
	now := v.now()
	for token, e := range v.byID {
		if !now.Before(e.session.ExpiresAt) {
			delete(v.byID, token)
		}
	}
	for id, e := range v.bySchedID {
		if !now.Before(e.expires) {
			delete(v.bySchedID, id)
		}
	}
}

// Run periodically discards expired or database-revoked credentials, even if unused.
func (v *MemoryVault) Run(ctx context.Context, lookup func(context.Context, string) (Session, error)) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer func() { v.mu.Lock(); clear(v.byID); clear(v.bySchedID); v.mu.Unlock() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v.Sweep(ctx, lookup)
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
	v.bySchedID[id] = scheduleEntry{password: password, expires: v.now().Add(time.Minute)}
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

// Sweep performs one bounded revocation pass without retaining the mutex over I/O.
func (v *MemoryVault) Sweep(ctx context.Context, lookup func(context.Context, string) (Session, error)) {
	v.mu.Lock()
	v.pruneLocked()
	tokens := make([]string, 0, len(v.byID))
	for token, e := range v.byID {
		if e.session.MailboxID > 0 {
			tokens = append(tokens, token)
		}
	}
	v.mu.Unlock()
	if lookup == nil {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, token := range tokens {
		if _, err := lookup(checkCtx, token); err != nil {
			v.Delete(token)
		}
	}
}
