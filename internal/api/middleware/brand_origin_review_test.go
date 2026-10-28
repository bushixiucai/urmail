package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type originPanelLookup struct{ session store.Sessions }

func (s originPanelLookup) GetSessionByID(context.Context, string) (store.Sessions, error) {
	return s.session, nil
}

type originMailboxLookup struct{ session webmail.Session }

func (s originMailboxLookup) GetMailboxSession(context.Context, string) (webmail.Session, error) {
	return s.session, nil
}

func TestLegacyAndBoundSessionsUseActualCanonicalHost(t *testing.T) {
	for _, kind := range []string{"panel", "mailbox"} {
		for _, tc := range []struct {
			name, host, origin string
			brand              bool
			want               int
		}{
			{"legacy-canonical", "panel.example.test", "", false, 204},
			{"legacy-default-port", "panel.example.test:443", "", false, 204},
			{"legacy-other-port", "panel.example.test:444", "", false, 401},
			{"legacy-forged-forwarded", "other.example.test", "", false, 401},
			{"legacy-brand", "panel.example.test", "", true, 401},
			{"bound-same", "panel.example.test:444", "panel.example.test:444", false, 204},
			{"bound-other", "other.example.test", "panel.example.test", false, 401},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				router := gin.New()
				router.Use(BrandHostBoundary(nil, "panel.example.test"))
				router.Use(func(c *gin.Context) {
					if tc.brand {
						c.Set(ContextBrandID, int64(5))
					}
				})
				var auth gin.HandlerFunc
				if kind == "panel" {
					auth = RequireAuth(originPanelLookup{store.Sessions{UserID: 7, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, OriginHost: tc.origin}})
				} else {
					auth = RequireMailboxAuth(originMailboxLookup{webmail.Session{Address: "owned@example.test", ExpiresAt: time.Now().Add(time.Hour), OriginHost: tc.origin}})
				}
				router.GET("/private", auth, func(c *gin.Context) { c.Status(204) })
				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+tc.host+"/private", nil)
				req.Header.Set("Authorization", "Bearer synthetic-review-token")
				req.Header.Set("X-Forwarded-Host", "panel.example.test")
				req.Header.Set("Forwarded", "host=panel.example.test;proto=https")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Fatalf("actual host policy: %d want %d: %s", rec.Code, tc.want, rec.Body.String())
				}
			})
		}
	}
}
