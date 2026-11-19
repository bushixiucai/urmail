package db

import (
	"github.com/bushixiucai/urmail/internal/store"
)

// Queries is the S2 single assembly point for the query surface (v3.2 §2.1).
// Every caller that used to construct store.New(...) directly must go
// through here so the S3 scope decorator (tenant/platform assertion keyed on
// scripts/sqlite/method_scopes.json) has exactly one seam to occupy.
func Queries(tx store.DBTX) *store.Queries {
	return store.New(tx)
}
