package db

import (
	"github.com/bushixiucai/urmail/internal/store"
)

// Queries is the S2 single assembly point for the query surface (v3.2 §2.1).
// Every caller that used to construct store.New(...) directly must go
// through here so the S3 scope decorator (tenant/platform/system/pre-auth
// assertion keyed on scripts/sqlite/method_scopes.json) has exactly one seam
// to occupy.
//
// Contract: tx must be non-nil; a nil seam would panic deep inside the
// generated code with an opaque nil-pointer dereference, so the assembly
// point fails fast instead (QA pre-review ⑤). The S3 decorator wraps the
// returned value here and asserts the caller's scope context.
func Queries(tx store.DBTX) *store.Queries {
	if tx == nil {
		panic("db.Queries: nil DBTX — the query surface has exactly one assembly point and it requires a live handle")
	}
	return store.New(tx)
}
