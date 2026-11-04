package handlers

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// loginLimiter is an in-memory sliding-window rate limiter for login
// attempts, keyed by client IP + email. It bounds online password
// guessing per identity without external state. A single-process limiter
// is sufficient while urmaild runs as one instance; when the deployment
// scales out this must move to the existing PostgreSQL authority.
type loginLimiter struct {
	mu          sync.Mutex
	maxEntries  int
	nextEntryID uint64
	entries     map[string]*limiterEntry
	head        *limiterEntry // most recently used (MRU) dummy node
	tail        *limiterEntry // least recently used (LRU) dummy node
	nowFunc     func() time.Time
}

type limiterEntry struct {
	id         uint64
	key        string
	failures   []time.Time
	pending    int
	prev, next *limiterEntry
}

const (
	loginMaxFailures   = 5
	loginMaxIPFailures = 50
	loginMaxEntries    = 10000
	loginWindow        = 15 * time.Minute
)

// LimiterBudgets defines failure budgets and sliding window duration.
type LimiterBudgets struct {
	IdentityMaxFailures int
	IPMaxFailures       int
	Window              time.Duration
}

// DefaultLoginBudgets specifies standard login failure limits.
var DefaultLoginBudgets = LimiterBudgets{
	IdentityMaxFailures: loginMaxFailures,
	IPMaxFailures:       loginMaxIPFailures,
	Window:              loginWindow,
}

// ErrRateLimited is returned when an attempt is rejected due to exhausted budgets or capacity.
var ErrRateLimited = errors.New("auth rate limited")

// Reservation manages the lifecycle of an atomic rate limit attempt.
type Reservation interface {
	CompleteSuccess()
	RecordFailure()
	Discard()
}

type limiterReservation struct {
	limiter     *loginLimiter
	identityKey string
	idEntryID   uint64
	ipKey       string
	ipEntryID   uint64
	mu          sync.Mutex
	settled     bool
}

var loginAttempts = newLoginLimiter()

var mfaDisableAttempts = newLoginLimiter()

const mfaDisableMaxFailures = 5

func mfaDisableKey(userID int64) string {
	return "mfa-disable|" + strconv.FormatInt(userID, 10)
}

func newLoginLimiter() *loginLimiter {
	return newLoginLimiterWithCapacity(loginMaxEntries)
}

func newLoginLimiterWithCapacity(maxEntries int) *loginLimiter {
	l := &loginLimiter{
		maxEntries: maxEntries,
		entries:    make(map[string]*limiterEntry),
		head:       &limiterEntry{},
		tail:       &limiterEntry{},
		nowFunc:    time.Now,
	}
	l.head.next = l.tail
	l.tail.prev = l.head
	return l
}

func (l *loginLimiter) removeNode(e *limiterEntry) {
	if e.prev != nil && e.next != nil {
		e.prev.next = e.next
		e.next.prev = e.prev
		e.prev = nil
		e.next = nil
	}
}

func (l *loginLimiter) pushFront(e *limiterEntry) {
	e.next = l.head.next
	e.prev = l.head
	l.head.next.prev = e
	l.head.next = e
}

func (l *loginLimiter) moveToFront(e *limiterEntry) {
	l.removeNode(e)
	l.pushFront(e)
}

func (l *loginLimiter) deleteEntry(key string) {
	if e, ok := l.entries[key]; ok {
		l.removeNode(e)
		delete(l.entries, key)
	}
}

func (l *loginLimiter) isEntryEmpty(e *limiterEntry) bool {
	return len(e.failures) == 0 && e.pending <= 0
}

func (l *loginLimiter) pruneEntry(e *limiterEntry, now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	kept := e.failures[:0]
	for _, ts := range e.failures {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	e.failures = kept
}

// ensureCapacity guarantees that the limiter can accommodate neededSlots new entries.
// It reclaims space strictly by pruning expired/empty entries from tail to head.
// It NEVER evicts an entry with pending > 0 or unexpired failures.
// If after pruning, available capacity is still insufficient, it returns false (fail-closed).
func (l *loginLimiter) ensureCapacity(neededSlots int, now time.Time, window time.Duration) bool {
	if neededSlots <= 0 {
		return true
	}
	if len(l.entries)+neededSlots <= l.maxEntries {
		return true
	}

	curr := l.tail.prev
	for curr != l.head {
		nextCurr := curr.prev
		l.pruneEntry(curr, now, window)
		if l.isEntryEmpty(curr) {
			l.deleteEntry(curr.key)
			if len(l.entries)+neededSlots <= l.maxEntries {
				return true
			}
		}
		curr = nextCurr
	}

	return len(l.entries)+neededSlots <= l.maxEntries
}

// BeginAttempt starts an atomic rate limit reservation for identityKey and ipKey.
// If either budget is exceeded (failures + active pending >= budget), ErrRateLimited is returned.
// If the table is at capacity and cannot accommodate both keys without evicting active entries,
// ErrRateLimited is returned fail-closed without any partial mutation.
// Otherwise, an active Reservation is returned and must be settled via CompleteSuccess,
// RecordFailure, or Discard.
func (l *loginLimiter) BeginAttempt(identityKey, ipKey string, budgets LimiterBudgets) (Reservation, error) {
	if budgets.IdentityMaxFailures <= 0 {
		budgets.IdentityMaxFailures = loginMaxFailures
	}
	if budgets.IPMaxFailures <= 0 {
		budgets.IPMaxFailures = loginMaxIPFailures
	}
	if budgets.Window <= 0 {
		budgets.Window = loginWindow
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.nowFunc()

	// 1. Check identityKey if it already exists
	var idEntry *limiterEntry
	if identityKey != "" {
		if e, ok := l.entries[identityKey]; ok {
			l.pruneEntry(e, now, budgets.Window)
			if l.isEntryEmpty(e) {
				l.deleteEntry(identityKey)
			} else {
				idEntry = e
				l.moveToFront(e)
			}
		}
		if idEntry != nil {
			if len(idEntry.failures)+idEntry.pending >= budgets.IdentityMaxFailures {
				return nil, ErrRateLimited
			}
		}
	}

	// 2. Check ipKey if it already exists
	var ipEntry *limiterEntry
	if ipKey != "" && ipKey != identityKey {
		if e, ok := l.entries[ipKey]; ok {
			l.pruneEntry(e, now, budgets.Window)
			if l.isEntryEmpty(e) {
				l.deleteEntry(ipKey)
			} else {
				ipEntry = e
				l.moveToFront(e)
			}
		}
		if ipEntry != nil {
			if len(ipEntry.failures)+ipEntry.pending >= budgets.IPMaxFailures {
				return nil, ErrRateLimited
			}
		}
	}

	// 3. Count how many new slots are needed for both keys
	neededSlots := 0
	if identityKey != "" && idEntry == nil {
		neededSlots++
	}
	if ipKey != "" && ipKey != identityKey && ipEntry == nil {
		neededSlots++
	}

	// 4. Ensure overall capacity exists atomically BEFORE any state mutation
	if neededSlots > 0 {
		if !l.ensureCapacity(neededSlots, now, budgets.Window) {
			// Fail-closed: insufficient capacity and cannot evict active entries.
			// No partial mutations are performed.
			return nil, ErrRateLimited
		}
	}

	// 5. Capacity guaranteed: allocate missing entries and increment pending
	var idID uint64
	if identityKey != "" {
		if idEntry == nil {
			l.nextEntryID++
			idEntry = &limiterEntry{id: l.nextEntryID, key: identityKey}
			l.entries[identityKey] = idEntry
			l.pushFront(idEntry)
		}
		idEntry.pending++
		idID = idEntry.id
	}
	var ipID uint64
	if ipKey != "" && ipKey != identityKey {
		if ipEntry == nil {
			l.nextEntryID++
			ipEntry = &limiterEntry{id: l.nextEntryID, key: ipKey}
			l.entries[ipKey] = ipEntry
			l.pushFront(ipEntry)
		}
		ipEntry.pending++
		ipID = ipEntry.id
	}

	return &limiterReservation{
		limiter:     l,
		identityKey: identityKey,
		idEntryID:   idID,
		ipKey:       ipKey,
		ipEntryID:   ipID,
	}, nil
}

func (r *limiterReservation) RecordFailure() {
	r.mu.Lock()
	if r.settled {
		r.mu.Unlock()
		return
	}
	r.settled = true
	r.mu.Unlock()

	r.limiter.recordFailure(r.identityKey, r.idEntryID, r.ipKey, r.ipEntryID)
}

func (r *limiterReservation) CompleteSuccess() {
	r.mu.Lock()
	if r.settled {
		r.mu.Unlock()
		return
	}
	r.settled = true
	r.mu.Unlock()

	r.limiter.completeSuccess(r.identityKey, r.idEntryID, r.ipKey, r.ipEntryID)
}

func (r *limiterReservation) Discard() {
	r.mu.Lock()
	if r.settled {
		r.mu.Unlock()
		return
	}
	r.settled = true
	r.mu.Unlock()

	r.limiter.discard(r.identityKey, r.idEntryID, r.ipKey, r.ipEntryID)
}

func (l *loginLimiter) recordFailure(identityKey string, idEntryID uint64, ipKey string, ipEntryID uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.nowFunc()

	recordKey := func(key string, expectedID uint64) {
		if key == "" {
			return
		}
		e, ok := l.entries[key]
		if !ok {
			if !l.ensureCapacity(1, now, loginWindow) {
				return
			}
			l.nextEntryID++
			e = &limiterEntry{id: l.nextEntryID, key: key}
			l.entries[key] = e
			l.pushFront(e)
		} else {
			if expectedID != 0 && e.id != expectedID {
				// ABA guard: entry was deleted and re-created after reservation was issued
				return
			}
			l.moveToFront(e)
		}
		if e.pending > 0 {
			e.pending--
		}
		e.failures = append(e.failures, now)
	}

	recordKey(identityKey, idEntryID)
	if ipKey != "" && ipKey != identityKey {
		recordKey(ipKey, ipEntryID)
	}
}

func (l *loginLimiter) completeSuccess(identityKey string, idEntryID uint64, ipKey string, ipEntryID uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 1. identityKey: pending decrements, failure history is CLEARED.
	if identityKey != "" {
		if e, ok := l.entries[identityKey]; ok {
			if idEntryID == 0 || e.id == idEntryID {
				if e.pending > 0 {
					e.pending--
				}
				e.failures = nil
				if l.isEntryEmpty(e) {
					l.deleteEntry(identityKey)
				} else {
					l.moveToFront(e)
				}
			}
		}
	}

	// 2. ipKey: pending decrements, BUT failure history is NOT CLEARED.
	// 严禁清空共享 ipKey 的失败窗口或计数！
	if ipKey != "" && ipKey != identityKey {
		if e, ok := l.entries[ipKey]; ok {
			if ipEntryID == 0 || e.id == ipEntryID {
				if e.pending > 0 {
					e.pending--
				}
				if l.isEntryEmpty(e) {
					l.deleteEntry(ipKey)
				} else {
					l.moveToFront(e)
				}
			}
		}
	}
}

func (l *loginLimiter) discard(identityKey string, idEntryID uint64, ipKey string, ipEntryID uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	releaseKey := func(key string, expectedID uint64) {
		if key == "" {
			return
		}
		if e, ok := l.entries[key]; ok {
			if expectedID != 0 && e.id != expectedID {
				// ABA guard: entry was deleted and re-created after reservation was issued
				return
			}
			if e.pending > 0 {
				e.pending--
			}
			if l.isEntryEmpty(e) {
				l.deleteEntry(key)
			} else {
				l.moveToFront(e)
			}
		}
	}

	releaseKey(identityKey, idEntryID)
	if ipKey != "" && ipKey != identityKey {
		releaseKey(ipKey, ipEntryID)
	}
}

// allow reports whether a new attempt for key is permitted under loginMaxFailures.
func (l *loginLimiter) allow(key string) bool {
	return l.allowN(key, loginMaxFailures)
}

// allowN is allow with an explicit failure budget.
func (l *loginLimiter) allowN(key string, budget int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[key]; ok {
		l.pruneEntry(e, l.nowFunc(), loginWindow)
		if l.isEntryEmpty(e) {
			l.deleteEntry(key)
			return true
		}
		l.moveToFront(e)
		return len(e.failures)+e.pending < budget
	}
	return true
}

// fail records a failed attempt for key.
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFunc()
	e, ok := l.entries[key]
	if !ok {
		if !l.ensureCapacity(1, now, loginWindow) {
			return
		}
		l.nextEntryID++
		e = &limiterEntry{id: l.nextEntryID, key: key}
		l.entries[key] = e
		l.pushFront(e)
	} else {
		l.moveToFront(e)
	}
	e.failures = append(e.failures, now)
}

// reset clears the failure history for key.
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[key]; ok {
		e.failures = nil
		if l.isEntryEmpty(e) {
			l.deleteEntry(key)
		}
	}
}

// ipKeyFromLimiterKey turns "ip|email" into "ip|ip"; the login key already
// starts with the client address.
func ipKeyFromLimiterKey(limiterKey string) string {
	if i := strings.IndexByte(limiterKey, '|'); i >= 0 {
		return "ip|" + limiterKey[:i]
	}
	return "ip|" + limiterKey
}
