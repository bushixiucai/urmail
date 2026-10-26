package webmail

import (
	"sync"
	"time"
)

// Session is the mailbox principal resolved from a short-lived bearer token.
// It is independent of any panel user or administrative role.
type Session struct {
	TokenHash string
	TenantID  int64
	MailboxID int64
	Address   string
	ExpiresAt time.Time
}

// CredentialVault retains the mailbox password in process memory so the IMAP
// proxy can dial Dovecot without persisting secrets. Entries are keyed by the
// same token hash stored in mailbox_sessions.
type CredentialVault interface {
	Put(tokenHash, password string)
	Get(tokenHash string) (string, bool)
	Delete(tokenHash string)
}

// ScheduleSealer retains SMTP passwords for pending scheduled sends, keyed by
// mailbox_scheduled_sends.id. Process-local only; PG smtp_secret (enc:v1) is
// the durable seal the worker falls back to after restart.
type ScheduleSealer interface {
	PutSchedule(id int64, password string)
	GetSchedule(id int64) (string, bool)
	DeleteSchedule(id int64)
}

// MemoryVault is a process-local CredentialVault and ScheduleSealer.
type MemoryVault struct {
	mu        sync.Mutex
	byID      map[string]string
	bySchedID map[int64]string
}

// NewMemoryVault returns an empty vault.
func NewMemoryVault() *MemoryVault {
	return &MemoryVault{byID: make(map[string]string), bySchedID: make(map[int64]string)}
}

// Put stores or replaces the password for tokenHash.
func (v *MemoryVault) Put(tokenHash, password string) {
	if v == nil || tokenHash == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.byID[tokenHash] = password
}

// Get returns the password if present.
func (v *MemoryVault) Get(tokenHash string) (string, bool) {
	if v == nil {
		return "", false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	password, ok := v.byID[tokenHash]
	return password, ok
}

// Delete removes the password for tokenHash.
func (v *MemoryVault) Delete(tokenHash string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.byID, tokenHash)
}

// PutSchedule seals a password for a scheduled send row id.
func (v *MemoryVault) PutSchedule(id int64, password string) {
	if v == nil || id <= 0 || password == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.bySchedID[id] = password
}

// GetSchedule returns the sealed password for a schedule id.
func (v *MemoryVault) GetSchedule(id int64) (string, bool) {
	if v == nil || id <= 0 {
		return "", false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	password, ok := v.bySchedID[id]
	return password, ok
}

// DeleteSchedule clears a schedule seal after send/cancel/fail.
func (v *MemoryVault) DeleteSchedule(id int64) {
	if v == nil || id <= 0 {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.bySchedID, id)
}
