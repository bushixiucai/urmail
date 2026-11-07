package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/domain/mail"
)

// credentialOnlyVault implements CredentialVault but not SessionVault. The
// protocol path must fail closed instead of falling back to the expiry-free
// Put: a stored password that outlives its session is a credential leak.
type credentialOnlyVault struct {
	putCalls int
}

func (v *credentialOnlyVault) Put(token, password string) { v.putCalls++ }
func (v *credentialOnlyVault) Get(token string) (string, bool) {
	return "", false
}
func (v *credentialOnlyVault) Delete(token string) {}

func TestProtocolAuthFailsClosedWithoutSessionVault(t *testing.T) {
	hash, err := mail.HashPassword("failclosed-vault-secret")
	if err != nil {
		t.Fatal(err)
	}
	vault := &credentialOnlyVault{}
	auth := protocolBrandAuth{fakeMailboxAuth{tenantID: 7, mailboxID: 9, address: "protocol@example.test", hash: hash}}
	h := &MailboxNativeHandler{Enabled: true, Auth: auth, Sessions: &protocolOriginSessions{}, Vault: vault}
	router := gin.New()
	router.POST("/protocol", h.ProtocolAuth(), func(c *gin.Context) { c.Status(http.StatusMultiStatus) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://brand.example.test/protocol", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.SetBasicAuth("protocol@example.test", "failclosed-vault-secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-SessionVault vault: got %d want 503: %s", rec.Code, rec.Body.String())
	}
	if vault.putCalls != 0 {
		t.Fatalf("password stored in expiry-free vault %d times", vault.putCalls)
	}
}
