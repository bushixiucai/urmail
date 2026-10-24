package mail_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bushixiucai/urmail/internal/domain/mail"
)

func TestWebmailSSOToken_Success(t *testing.T) {
	secret := []byte("super-secret-key-for-roundcube-sso")
	tenantID := "tenant-alpha-001"
	email := "alice@example.com"

	token, err := mail.GenerateWebmailSSOToken(secret, tenantID, email, 5*time.Minute)
	if err != nil {
		t.Fatalf("GenerateWebmailSSOToken failed: %v", err)
	}

	if token == "" {
		t.Fatal("generated token is empty")
	}

	claims, err := mail.ValidateWebmailSSOToken(secret, tenantID, token)
	if err != nil {
		t.Fatalf("ValidateWebmailSSOToken failed: %v", err)
	}

	if claims.TenantID != tenantID {
		t.Errorf("claims.TenantID = %q, want %q", claims.TenantID, tenantID)
	}
	if claims.Email != email {
		t.Errorf("claims.Email = %q, want %q", claims.Email, email)
	}
	if claims.ExpiresAt <= claims.IssuedAt {
		t.Errorf("ExpiresAt (%d) should be > IssuedAt (%d)", claims.ExpiresAt, claims.IssuedAt)
	}
	if claims.Nonce == "" {
		t.Error("claims.Nonce should not be empty")
	}
}

func TestWebmailSSOToken_WithUser(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	tenantID := "tenant-beta"
	userID := "user-12345"
	email := "bob@beta.org"

	token, err := mail.GenerateWebmailSSOTokenWithUser(secret, tenantID, userID, email, 10*time.Minute)
	if err != nil {
		t.Fatalf("GenerateWebmailSSOTokenWithUser failed: %v", err)
	}

	claims, err := mail.ValidateWebmailToken(secret, tenantID, token)
	if err != nil {
		t.Fatalf("ValidateWebmailToken failed: %v", err)
	}
	if claims.UserID != userID {
		t.Errorf("claims.UserID = %q, want %q", claims.UserID, userID)
	}
}

func TestWebmailSSOToken_Expired(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	tenantID := "tenant-test"
	email := "charlie@test.com"

	// Fast token with 1 second ttl
	tokenShort, err := mail.GenerateWebmailSSOTokenWithUser(secret, tenantID, "user-id", email, 1*time.Second)
	if err != nil {
		t.Fatalf("GenerateWebmailSSOTokenWithUser failed: %v", err)
	}

	time.Sleep(1500 * time.Millisecond)

	_, err = mail.ValidateWebmailSSOToken(secret, tenantID, tokenShort)
	if err == nil {
		t.Fatal("expected expired token error, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expired error, got %v", err)
	}
}

func TestWebmailSSOToken_TenantMismatch(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	tenantAlpha := "tenant-alpha"
	tenantBeta := "tenant-beta"
	email := "user@alpha.com"

	token, err := mail.GenerateWebmailSSOToken(secret, tenantAlpha, email, 5*time.Minute)
	if err != nil {
		t.Fatalf("GenerateWebmailSSOToken failed: %v", err)
	}

	// Try to validate under tenantBeta
	_, err = mail.ValidateWebmailSSOToken(secret, tenantBeta, token)
	if err == nil {
		t.Fatal("expected tenant mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("expected tenant mismatch error, got %v", err)
	}
}

func TestWebmailSSOToken_Tampered(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	tenantID := "tenant-secure"
	email := "victim@secure.com"

	token, err := mail.GenerateWebmailSSOToken(secret, tenantID, email, 5*time.Minute)
	if err != nil {
		t.Fatalf("GenerateWebmailSSOToken failed: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	// 1. Tampered payload
	tamperedPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"tenant_id":"tenant-secure","email":"attacker@secure.com"}`))
	tamperedToken := tamperedPayload + "." + parts[1]
	_, err = mail.ValidateWebmailSSOToken(secret, tenantID, tamperedToken)
	if err == nil {
		t.Fatal("expected signature mismatch for tampered payload, got nil")
	}

	// 2. Tampered signature
	badSigToken := parts[0] + ".AAAA" + parts[1][4:]
	_, err = mail.ValidateWebmailSSOToken(secret, tenantID, badSigToken)
	if err == nil {
		t.Fatal("expected signature mismatch for tampered signature, got nil")
	}

	// 3. Different secret
	_, err = mail.ValidateWebmailSSOToken([]byte("different-secret"), tenantID, token)
	if err == nil {
		t.Fatal("expected signature mismatch for wrong secret, got nil")
	}
}

func TestWebmailSSOToken_InputValidation(t *testing.T) {
	secret := []byte("secret")

	// Missing secret
	if _, err := mail.GenerateWebmailSSOToken(nil, "tenant", "a@b.com", time.Minute); err == nil {
		t.Error("expected error for empty secret")
	}
	// Missing tenant
	if _, err := mail.GenerateWebmailSSOToken(secret, "", "a@b.com", time.Minute); err == nil {
		t.Error("expected error for empty tenant")
	}
	// Missing email
	if _, err := mail.GenerateWebmailSSOToken(secret, "tenant", "", time.Minute); err == nil {
		t.Error("expected error for empty email")
	}
	// Excessively long credentials must not be minted.
	if _, err := mail.GenerateWebmailSSOToken(secret, "tenant", "a@b.com", mail.MaxWebmailTokenTTL+time.Second); !errors.Is(err, mail.ErrWebmailInvalidTTL) {
		t.Errorf("expected ErrWebmailInvalidTTL, got %v", err)
	}

	// Validation invalid inputs
	if _, err := mail.ValidateWebmailSSOToken(nil, "tenant", "token"); err == nil {
		t.Error("expected error for empty secret on validate")
	}
	if _, err := mail.ValidateWebmailSSOToken(secret, "tenant", ""); err == nil {
		t.Error("expected error for empty token on validate")
	}
	if _, err := mail.ValidateWebmailSSOToken(secret, "tenant", "invalid-no-dot"); err == nil {
		t.Error("expected error for token with no dot")
	}
	if _, err := mail.ValidateWebmailSSOToken(secret, "tenant", "bad-payload.bad-sig"); err == nil {
		t.Error("expected error for invalid base64 token")
	}
}

func mustSignWebmailPayload(t *testing.T, secret []byte, payload string) string {
	t.Helper()
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestWebmailSSOToken_JTIScopeMailboxClaims(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	tenantID := "tenant-jti"
	email := "jti@example.com"

	token, err := mail.GenerateWebmailSSOToken(secret, tenantID, email, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := mail.ValidateWebmailSSOToken(secret, tenantID, token)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if claims.JTI == "" {
		t.Error("claims.JTI must not be empty")
	}
	if claims.Scope != mail.DefaultWebmailTokenScope {
		t.Errorf("claims.Scope = %q, want %q", claims.Scope, mail.DefaultWebmailTokenScope)
	}
	if claims.Mailbox != email {
		t.Errorf("claims.Mailbox = %q, want %q", claims.Mailbox, email)
	}
}

func TestWebmailSSOToken_RejectsMissingJTI(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	now := time.Now().Unix()
	payload := fmt.Sprintf(`{"tenant_id":"t1","email":"a@b.com","iat":%d,"exp":%d,"nonce":"nn"}`, now, now+60)
	token := mustSignWebmailPayload(t, secret, payload)
	_, err := mail.ValidateWebmailSSOToken(secret, "t1", token)
	if err == nil {
		t.Fatal("expected rejection for token without jti")
	}
	if !strings.Contains(err.Error(), "jti") {
		t.Errorf("expected jti error, got %v", err)
	}
}

func TestWebmailSSOToken_RejectsMailboxEmailMismatch(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	now := time.Now().Unix()
	payload := fmt.Sprintf(`{"tenant_id":"t1","email":"a@b.com","iat":%d,"exp":%d,"nonce":"nn","jti":"j1","scope":"webmail","mailbox":"victim@b.com"}`, now, now+60)
	token := mustSignWebmailPayload(t, secret, payload)
	_, err := mail.ValidateWebmailSSOToken(secret, "t1", token)
	if err == nil {
		t.Fatal("expected rejection for mailbox != email")
	}
	if !strings.Contains(err.Error(), "mailbox") {
		t.Errorf("expected mailbox mismatch error, got %v", err)
	}
}

func TestWebmailSSOToken_RevokeByJTI(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	tenantID := "tenant-revoke"
	email := "revoke@example.com"

	token, err := mail.GenerateWebmailSSOToken(secret, tenantID, email, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := mail.ValidateWebmailSSOToken(secret, tenantID, token)
	if err != nil {
		t.Fatal(err)
	}

	mail.RevokeWebmailToken(claims.JTI, time.Now().Add(time.Hour))
	if !mail.IsWebmailTokenRevoked(claims.JTI) {
		t.Fatal("expected jti to be revoked")
	}
	_, err = mail.ValidateWebmailSSOToken(secret, tenantID, token)
	if !errors.Is(err, mail.ErrWebmailTokenRevoked) {
		t.Fatalf("expected ErrWebmailTokenRevoked, got %v", err)
	}

	// Revocation is jti-scoped: another token still validates.
	token2, err := mail.GenerateWebmailSSOToken(secret, tenantID, email, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mail.ValidateWebmailSSOToken(secret, tenantID, token2); err != nil {
		t.Fatalf("unrelated token must still validate: %v", err)
	}
}

func TestWebmailSSOToken_ScopeValidation(t *testing.T) {
	secret := []byte("roundcube-sso-secret")
	tenantID := "tenant-scope"
	email := "scope@example.com"

	token, err := mail.GenerateWebmailSSOToken(secret, tenantID, email, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mail.ValidateWebmailSSOTokenWithScope(secret, tenantID, mail.DefaultWebmailTokenScope, token); err != nil {
		t.Fatalf("expected scope match to pass, got %v", err)
	}
	_, err = mail.ValidateWebmailSSOTokenWithScope(secret, tenantID, "admin", token)
	if !errors.Is(err, mail.ErrWebmailScopeMismatch) {
		t.Fatalf("expected ErrWebmailScopeMismatch, got %v", err)
	}
}
