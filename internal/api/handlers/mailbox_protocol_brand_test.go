package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/webmail"
)

type protocolBrandAuth struct{ fakeMailboxAuth }

func (protocolBrandAuth) BrandTenantMatches(_ context.Context, tenant, brand int64) bool {
	return tenant == 7 && brand == 5
}

type protocolOriginSessions struct{ fakeMailboxSessions }

func (s *protocolOriginSessions) Create(ctx context.Context, hash, address, expected string, remember bool) (webmail.Session, error) {
	session, err := s.fakeMailboxSessions.Create(ctx, hash, address, expected, remember)
	session.OriginHost = webmail.OriginHost(ctx)
	s.byHash[hash] = session
	return session, err
}

func TestBasicProtocolPreservesBrandBoundaryAndCleansEphemeralSession(t *testing.T) {
	hash, err := domainMail.HashPassword("brand-protocol-fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"PROPFIND", http.MethodPost} {
		for _, brand := range []int64{5, 6} {
			t.Run(method+string(rune('0'+brand)), func(t *testing.T) {
				sessions := &protocolOriginSessions{}
				vault := webmail.NewMemoryVault()
				auth := protocolBrandAuth{fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "protocol@example.test", hash: hash}}
				h := &MailboxNativeHandler{Auth: auth, Sessions: sessions, Vault: vault}
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(middleware.ContextBrandID, brand)
					c.Request = c.Request.WithContext(webmail.WithOriginHost(c.Request.Context(), middleware.OriginHost(c.Request.Host)))
				})
				router.Handle(method, "/protocol", h.ProtocolAuth(), func(c *gin.Context) { c.Status(http.StatusMultiStatus) })
				req := httptest.NewRequestWithContext(t.Context(), method, "https://brand.example.test/protocol", nil)
				req.RemoteAddr = "127.0.0.1:12345"
				req.SetBasicAuth("protocol@example.test", "brand-protocol-fixture-secret")
				req.Header.Set("X-Forwarded-Host", "other.example.test")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				want := http.StatusMultiStatus
				if brand != 5 {
					want = http.StatusForbidden
				}
				if rec.Code != want {
					t.Fatalf("brand boundary: got %d want %d: %s", rec.Code, want, rec.Body.String())
				}
				if len(sessions.byHash) != 0 {
					t.Fatal("Basic protocol session retained after request")
				}
			})
		}
	}
}
