package db

import (
	"sync"

	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/store"
)

var (
	hookMu        sync.RWMutex
	isolationHook isolation.Hook
	querierHook   isolation.QuerierHook = isolation.WrapQuerier
)

// SetIsolationHook installs the S3 scope-assertion decorator on db.Queries and
// syncs the store.WithTx boundary so transaction-derived Queries preserve the
// same decorator. Passing nil restores direct pass-through (used only by tests).
func SetIsolationHook(hook isolation.Hook) {
	hookMu.Lock()
	isolationHook = hook
	hookMu.Unlock()
	if hook == nil {
		store.SetTxBoundaryHook(nil)
		return
	}
	store.SetTxBoundaryHook(func(tx store.DBTX, q *store.Queries) *store.Queries {
		return hook(tx, q)
	})
}

func currentIsolationHook() isolation.Hook {
	hookMu.RLock()
	h := isolationHook
	hookMu.RUnlock()
	return h
}

// SetQuerierHook overrides the S3 Querier decorator used by db.Querier.
// Passing nil restores the default isolation.WrapQuerier decorator.
func SetQuerierHook(hook isolation.QuerierHook) {
	hookMu.Lock()
	if hook == nil {
		querierHook = isolation.WrapQuerier
	} else {
		querierHook = hook
	}
	hookMu.Unlock()
}

func currentQuerierHook() isolation.QuerierHook {
	hookMu.RLock()
	h := querierHook
	hookMu.RUnlock()
	return h
}

// Queries is the S2/S3 single assembly point for the query surface (v3.2 §2.1).
// Every caller that used to construct store.New(...) directly must go
// through here so the S3 scope decorator (tenant/platform/system/pre-auth
// assertion keyed on internal/db/isolation/method_scopes.json) has exactly
// one seam to occupy.
//
// Contract: tx must be non-nil; a nil seam would panic deep inside the
// generated code with an opaque nil-pointer dereference, so the assembly
// point fails fast instead (QA pre-review ⑤).
func Queries(tx store.DBTX) *store.Queries {
	if tx == nil {
		panic("db.Queries: nil DBTX — the query surface has exactly one assembly point and it requires a live handle")
	}
	q := store.New(tx)
	if hook := currentIsolationHook(); hook != nil {
		if wrapped := hook(tx, q); wrapped != nil {
			q = wrapped
		}
		q.AttachBoundary(func(innerTx store.DBTX, innerQ *store.Queries) *store.Queries {
			return hook(innerTx, innerQ)
		})
	}
	return q
}

// Querier returns the S3 scope-enforced store.Querier surface (702 methods)
// backed by db.Queries(tx) and wrapped by isolation.WrapQuerier.
func Querier(tx store.DBTX) store.Querier {
	q := Queries(tx)
	if hook := currentQuerierHook(); hook != nil {
		return hook(tx, q)
	}
	return isolation.NewScopedQuerier(q)
}
