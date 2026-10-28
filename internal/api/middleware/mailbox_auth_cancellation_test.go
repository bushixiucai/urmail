package middleware

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type cancelMailboxLookup struct{ err error }

func (s cancelMailboxLookup) GetMailboxSession(context.Context, string) (webmail.Session, error) {
	return webmail.Session{}, s.err
}
func TestCanceledMailboxLookupDoesNotEraseCredential(t *testing.T) {
	for _, lookupErr := range []error{context.Canceled, context.DeadlineExceeded, errors.New("database temporarily unavailable"), pgx.ErrNoRows} {
		t.Run(lookupErr.Error(), func(t *testing.T) {
			vault := webmail.NewMemoryVault()
			hash := mailboxprincipal.HashToken("test-native-bearer")
			vault.Put(hash, "test-vault-secret")
			router := gin.New()
			router.GET("/", RequireMailboxAuth(cancelMailboxLookup{lookupErr}, vault), func(c *gin.Context) { c.Status(200) })
			req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
			req.Header.Set("Authorization", "Bearer test-native-bearer")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			_, retained := vault.Get(hash)
			if errors.Is(lookupErr, pgx.ErrNoRows) {
				if rec.Code != 401 || retained {
					t.Fatal("definitively revoked credential retained")
				}
			} else if rec.Code != 503 || !retained {
				t.Fatalf("temporary lookup failure erased secret or authorized: status=%d retained=%v", rec.Code, retained)
			}
		})
	}
}
