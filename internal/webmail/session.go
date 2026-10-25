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

// MemoryVault is a process-local CredentialVault.
type MemoryVault struct {
	mu   sync.Mutex
	byID map[string]string
}

// NewMemoryVault returns an empty vault.
func NewMemoryVault() *MemoryVault {
	return &MemoryVault{byID: make(map[string]string)}
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
