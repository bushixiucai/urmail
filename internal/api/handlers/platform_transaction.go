package handlers

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/store"
)

// platformMutationQueries reuses the audited request transaction. The fallback
// supports direct handler tests; production routes always install the boundary.
func platformMutationQueries(c *gin.Context, pool *db.Pool) (*store.Queries, func() error, func(), error) {
	if value, ok := middleware.QueriesFromCtx(c); ok {
		if q, valid := value.(*store.Queries); valid {
			return q, func() error { return nil }, func() {}, nil
		}
	}
	if pool == nil {
		return nil, nil, nil, errors.New("platform transaction unavailable")
	}
	tx, err := pool.PlatformTx(c.Request.Context())
	if err != nil {
		return nil, nil, nil, err
	}
	return store.New(tx), func() error { return tx.Commit(c.Request.Context()) }, func() { _ = tx.Rollback(context.Background()) }, nil
}
