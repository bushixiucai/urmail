package middleware_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

type guardedTransactionStream struct {
	reader     io.Reader
	beforeRead func()
	closed     bool
	reads      int
}

func (s *guardedTransactionStream) Read(p []byte) (int, error) {
	s.reads++
	s.beforeRead()
	return s.reader.Read(p)
}
func (s *guardedTransactionStream) Close() error { s.closed = true; return nil }

func TestDeferredStreamRequiresRealTransactionCommit(t *testing.T) {
	app, platform, tenant, user := transactionV3Fixture(t)
	for _, cancelCommit := range []bool{false, true} {
		engine := gin.New()
		engine.Use(func(c *gin.Context) { c.Set(middleware.ContextTenantID, tenant); c.Set(middleware.ContextUserID, user) })
		engine.Use(middleware.RequireTenantTx(app, zap.NewNop()))
		var source *guardedTransactionStream
		engine.GET("/download", func(c *gin.Context) {
			ctx, cancel := context.WithCancel(c.Request.Context())
			t.Cleanup(cancel)
			c.Request = c.Request.WithContext(ctx)
			raw, _ := middleware.QueriesFromCtx(c)
			q := raw.(*store.Queries)
			if _, err := q.UpsertTenantSetting(c, store.UpsertTenantSettingParams{ScopeID: tenant, Key: "locale", Value: []byte(`"en"`)}); err != nil {
				t.Fatal(err)
			}
			source = &guardedTransactionStream{reader: bytes.NewReader(make([]byte, 9<<20)), beforeRead: func() {
				var present bool
				if err := platform.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM settings WHERE scope='tenant' AND scope_id=$1 AND key='locale')", tenant).Scan(&present); err != nil || !present {
					t.Error("stream read before durable commit")
				}
			}}
			if err := middleware.DeferStream(c, source, "application/octet-stream", 9<<20); err != nil {
				t.Fatal(err)
			}
			if source.reads != 0 {
				t.Error("stream read inside transaction")
			}
			if cancelCommit {
				cancel()
			}
		})
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/download", nil))
		if !source.closed {
			t.Fatal("transaction did not close stream")
		}
		if cancelCommit {
			if recorder.Code != 500 || source.reads != 0 {
				t.Fatalf("failed commit released stream: status=%d reads=%d", recorder.Code, source.reads)
			}
		} else if recorder.Code != 200 || recorder.Body.Len() != 9<<20 {
			t.Fatalf("large committed stream failed: status=%d size=%d", recorder.Code, recorder.Body.Len())
		}
	}
}
