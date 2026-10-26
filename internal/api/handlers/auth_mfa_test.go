package handlers

// Tests for the MFA (TOTP) second factor: the pending-token store, the
// two-legged login flow, recovery code consumption and enable/disable.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp/totp"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/store"
)

type fakeAuthStore struct {
	users       map[int64]*store.GetUserByIDRow
	byEmail     map[string]int64
	sessions    map[string]store.Sessions
	usedCodes   map[int64]map[string]bool
	nextID      int64
	currentUser int64 // the user the fake auth middleware authenticates as
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{
		users:     make(map[int64]*store.GetUserByIDRow),
		byEmail:   make(map[string]int64),
		sessions:  make(map[string]store.Sessions),
		usedCodes: make(map[int64]map[string]bool),
	}
}

func (f *fakeAuthStore) addUser(t *testing.T, email string, mfaEnabled bool, secret string, recoveryHashes []string) int64 {
	t.Helper()
	f.nextID++
	hash, err := mail.HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	id := f.nextID
	var codes []byte
	if recoveryHashes != nil {
		codes, _ = json.Marshal(recoveryHashes)
	} else {
		codes = []byte("[]")
	}
	f.users[id] = &store.GetUserByIDRow{
		ID:               id,
		Email:            email,
		PasswordHash:     hash,
		Locale:           "zh-CN",
		Status:           "active",
		MfaSecret:        pgtype.Text{String: secret, Valid: secret != ""},
		MfaRecoveryCodes: codes,
		MfaEnabled:       mfaEnabled,
	}
	f.byEmail[email] = id
	return id
}

func (f *fakeAuthStore) row(id int64) store.GetUserByEmailRow {
	u := f.users[id]
	return store.GetUserByEmailRow{
		ID: u.ID, Email: u.Email, PasswordHash: u.PasswordHash,
		Locale: u.Locale, Status: u.Status,
		MfaSecret: u.MfaSecret, MfaRecoveryCodes: u.MfaRecoveryCodes,
		MfaEnabled: u.MfaEnabled,
	}
}

func (f *fakeAuthStore) GetUserByEmail(_ context.Context, email string) (store.GetUserByEmailRow, error) {
	id, ok := f.byEmail[email]
	if !ok {
		return store.GetUserByEmailRow{}, pgx.ErrNoRows
	}
	return f.row(id), nil
}

func (f *fakeAuthStore) GetUserByID(_ context.Context, id int64) (store.GetUserByIDRow, error) {
	u, ok := f.users[id]
	if !ok {
		return store.GetUserByIDRow{}, pgx.ErrNoRows
	}
	return *u, nil
}

func (f *fakeAuthStore) GetUserTenants(_ context.Context, _ int64) ([]store.GetUserTenantsRow, error) {
	return nil, nil
}

func (f *fakeAuthStore) CheckPlatformAdmin(_ context.Context, _ int64) (bool, error) {
	return false, nil
}

func (f *fakeAuthStore) CreateSession(_ context.Context, arg store.CreateSessionParams) (store.Sessions, error) {
	s := store.Sessions{
		ID: arg.ID, UserID: arg.UserID, TenantID: arg.TenantID,
		CsrfToken: arg.CsrfToken, UserAgent: arg.UserAgent, ExpiresAt: arg.ExpiresAt,
	}
	f.sessions[arg.ID] = s
	return s, nil
}

func (f *fakeAuthStore) MarkSessionLegacyOnly(_ context.Context, _ string) error { return nil }

func (f *fakeAuthStore) MarkSessionMFAVerified(_ context.Context, _ string) error { return nil }

func (f *fakeAuthStore) DeleteSession(_ context.Context, id string) error {
	delete(f.sessions, id)
	return nil
}

func (f *fakeAuthStore) UpdateSessionTenant(_ context.Context, _ store.UpdateSessionTenantParams) error {
	return nil
}

func (f *fakeAuthStore) SetMFASecret(_ context.Context, arg store.SetMFASecretParams) error {
	f.users[arg.ID].MfaSecret = arg.MfaSecret
	return nil
}

func (f *fakeAuthStore) EnableUserMFA(_ context.Context, arg store.EnableUserMFAParams) error {
	f.users[arg.ID].MfaEnabled = true
	f.users[arg.ID].MfaRecoveryCodes = arg.MfaRecoveryCodes
	return nil
}

func (f *fakeAuthStore) DisableUserMFA(_ context.Context, id int64) error {
	u := f.users[id]
	u.MfaEnabled = false
	u.MfaSecret = pgtype.Text{}
	u.MfaRecoveryCodes = []byte("[]")
	return nil
}

func (f *fakeAuthStore) UpdateUserPassword(_ context.Context, arg store.UpdateUserPasswordParams) error {
	if u, ok := f.users[arg.ID]; ok {
		u.PasswordHash = arg.PasswordHash
	}
	return nil
}

func (f *fakeAuthStore) DeleteOtherUserSessions(_ context.Context, arg store.DeleteOtherUserSessionsParams) error {
	for id, session := range f.sessions {
		if session.UserID == arg.UserID && id != arg.ID {
			delete(f.sessions, id)
		}
	}
	return nil
}

func (f *fakeAuthStore) ListSessionsForUser(_ context.Context, userID int64) ([]store.ListSessionsForUserRow, error) {
	out := []store.ListSessionsForUserRow{}
	for id, session := range f.sessions {
		if session.UserID == userID {
			out = append(out, store.ListSessionsForUserRow{ID: id, UserID: userID})
		}
	}
	return out, nil
}

func (f *fakeAuthStore) DeleteUserSession(_ context.Context, arg store.DeleteUserSessionParams) (int64, error) {
	if session, ok := f.sessions[arg.ID]; ok && session.UserID == arg.UserID {
		delete(f.sessions, arg.ID)
		return 1, nil
	}
	return 0, nil
}

func (f *fakeAuthStore) ConsumeRecoveryCode(_ context.Context, arg store.ConsumeRecoveryCodeParams) (int64, error) {
	u, ok := f.users[arg.ID]
	if !ok {
		return 0, nil
	}
	var hashes []string
	if err := json.Unmarshal(u.MfaRecoveryCodes, &hashes); err != nil {
		return 0, nil
	}
	kept := hashes[:0]
	found := false
	for _, h := range hashes {
		if h == arg.Column2 && !f.usedCodes[arg.ID][h] {
			found = true
			if f.usedCodes[arg.ID] == nil {
				f.usedCodes[arg.ID] = make(map[string]bool)
			}
			f.usedCodes[arg.ID][h] = true
			continue
		}
		kept = append(kept, h)
	}
	if !found {
		return 0, nil
	}
	out, _ := json.Marshal(kept)
	u.MfaRecoveryCodes = out
	return 1, nil
}

func newMFATestRouter(st *fakeAuthStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAuthHandler(st)
	r := gin.New()

	pub := r.Group("/api/v1/auth")
	pub.POST("/login", h.Login)
	pub.POST("/mfa/verify", h.MFAVerify)

	authed := r.Group("/api/v1")
	authed.Use(func(c *gin.Context) {
		c.Set(middleware.ContextUserID, st.currentUser)
		c.Set(middleware.ContextSession, store.Sessions{ID: "current", UserID: st.currentUser})
		c.Next()
	})
	authed.POST("/auth/mfa/setup", h.MFASetup)
	authed.POST("/auth/mfa/enable", h.MFAEnable)
	authed.POST("/auth/mfa/disable", h.MFADisable)
	authed.POST("/auth/password", h.ChangePassword)
	return r
}

func postJSON(t *testing.T, r *gin.Engine, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func resetMFAState() {
	loginAttempts = newLoginLimiter()
	mfaDisableAttempts = newLoginLimiter()
	pendingMFATokens = newMFATokenStore()
}

func TestMFATokenStoreSingleUseAndTTL(t *testing.T) {
	resetMFAState()
	s := newMFATokenStore()

	tok, err := s.issue(7, "k")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, ok := s.check(tok); !ok {
		t.Fatal("fresh token must validate")
	}
	p, ok := s.take(tok)
	if !ok || p.UserID != 7 {
		t.Fatalf("take should return the pending login, got %v %v", p, ok)
	}
	// Single use: the second take must fail.
	if _, ok := s.take(tok); ok {
		t.Fatal("a consumed mfa_token must not be usable again")
	}

	// TTL: an expired token is invalid on both check and take.
	tok2, _ := s.issue(8, "k")
	s.mu.Lock()
	e := s.entries[tok2]
	e.ExpiresAt = time.Now().Add(-time.Second)
	s.entries[tok2] = e
	s.mu.Unlock()
	if _, ok := s.check(tok2); ok {
		t.Fatal("expired token must fail check")
	}
	if _, ok := s.take(tok2); ok {
		t.Fatal("expired token must fail take")
	}
}

func TestLoginMFASecondLegWithTOTP(t *testing.T) {
	resetMFAState()
	st := newFakeAuthStore()
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "urMail.cn", AccountName: "mfa@example.com"})
	if err != nil {
		t.Fatalf("generate totp key: %v", err)
	}
	st.addUser(t, "mfa@example.com", true, key.Secret(), nil)
	r := newMFATestRouter(st)

	// First leg: correct password, MFA required, no session created.
	code, body := postJSON(t, r, "/api/v1/auth/login", `{"email":"mfa@example.com","password":"correct-horse"}`)
	if code != http.StatusOK {
		t.Fatalf("login status = %d body %v", code, body)
	}
	if body["mfa_required"] != true {
		t.Fatalf("expected mfa_required=true, got %v", body)
	}
	mfaToken, _ := body["mfa_token"].(string)
	if mfaToken == "" {
		t.Fatal("expected a mfa_token")
	}
	if _, ok := body["token"]; ok {
		t.Fatal("no session token may be issued before the second factor")
	}
	if len(st.sessions) != 0 {
		t.Fatal("no session row may be created before the second factor")
	}

	// Wrong code: rejected and counted against the limiter.
	code, body = postJSON(t, r, "/api/v1/auth/mfa/verify", `{"mfa_token":"`+mfaToken+`","code":"000000"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong code status = %d body %v", code, body)
	}

	// Correct code from the TOTP secret: session issued.
	now := time.Now()
	totpCode, err := totp.GenerateCode(key.Secret(), now)
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	code, body = postJSON(t, r, "/api/v1/auth/mfa/verify", `{"mfa_token":"`+mfaToken+`","code":"`+totpCode+`"}`)
	if code != http.StatusOK {
		t.Fatalf("verify status = %d body %v", code, body)
	}
	if body["token"] == "" {
		t.Fatal("expected a session token after verify")
	}
	if len(st.sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(st.sessions))
	}

	// The mfa_token is single use.
	code, _ = postJSON(t, r, "/api/v1/auth/mfa/verify", `{"mfa_token":"`+mfaToken+`","code":"`+totpCode+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("reused mfa_token status = %d, want 400", code)
	}
}

func TestMFAVerifyWithRecoveryCode(t *testing.T) {
	resetMFAState()
	st := newFakeAuthStore()
	recovery := hashRecoveryCode("recovery-code-1")
	st.addUser(t, "rec@example.com", true, "", []string{recovery})
	r := newMFATestRouter(st)

	_, body := postJSON(t, r, "/api/v1/auth/login", `{"email":"rec@example.com","password":"correct-horse"}`)
	mfaToken, _ := body["mfa_token"].(string)

	code, body := postJSON(t, r, "/api/v1/auth/mfa/verify", `{"mfa_token":"`+mfaToken+`","code":"recovery-code-1"}`)
	if code != http.StatusOK {
		t.Fatalf("recovery verify status = %d body %v", code, body)
	}
	if body["token"] == "" {
		t.Fatal("expected a session token")
	}

	// The recovery code is burned: a second login cannot reuse it.
	_, body = postJSON(t, r, "/api/v1/auth/login", `{"email":"rec@example.com","password":"correct-horse"}`)
	mfaToken2, _ := body["mfa_token"].(string)
	code, _ = postJSON(t, r, "/api/v1/auth/mfa/verify", `{"mfa_token":"`+mfaToken2+`","code":"recovery-code-1"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("reused recovery code status = %d, want 401", code)
	}
}

func TestMFASetupEnableDisableFlow(t *testing.T) {
	resetMFAState()
	st := newFakeAuthStore()
	uid := st.addUser(t, "setup@example.com", false, "", nil)
	st.currentUser = uid
	r := newMFATestRouter(st)

	// Plain login still works and creates a session directly.
	code, body := postJSON(t, r, "/api/v1/auth/login", `{"email":"setup@example.com","password":"correct-horse"}`)
	if code != http.StatusOK || body["token"] == "" {
		t.Fatalf("plain login status = %d body %v", code, body)
	}

	// Setup: stores a secret without enabling MFA.
	code, body = postJSON(t, r, "/api/v1/auth/mfa/setup", `{}`)
	if code != http.StatusOK {
		t.Fatalf("setup status = %d body %v", code, body)
	}
	secret, _ := body["secret"].(string)
	if secret == "" {
		t.Fatal("expected a secret from setup")
	}
	if _, ok := body["otpauth_url"]; !ok {
		t.Fatal("expected an otpauth_url from setup")
	}
	if st.users[uid].MfaEnabled {
		t.Fatal("setup must not enable MFA")
	}

	// Enable with a wrong code fails; with the right code returns recovery codes.
	code, _ = postJSON(t, r, "/api/v1/auth/mfa/enable", `{"code":"000000"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("enable with wrong code status = %d, want 401", code)
	}
	goodCode, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	code, body = postJSON(t, r, "/api/v1/auth/mfa/enable", `{"code":"`+goodCode+`"}`)
	if code != http.StatusOK {
		t.Fatalf("enable status = %d body %v", code, body)
	}
	rawCodes, _ := json.Marshal(body["recovery_codes"])
	var recoveryCodes []string
	if err := json.Unmarshal(rawCodes, &recoveryCodes); err != nil || len(recoveryCodes) != mfaRecoveryCodeCount {
		t.Fatalf("expected %d recovery codes, got %v (%v)", mfaRecoveryCodeCount, body["recovery_codes"], err)
	}
	if !st.users[uid].MfaEnabled {
		t.Fatal("MFA must be enabled after enable")
	}

	// Login now requires the second leg.
	code, body = postJSON(t, r, "/api/v1/auth/login", `{"email":"setup@example.com","password":"correct-horse"}`)
	if code != http.StatusOK || body["mfa_required"] != true {
		t.Fatalf("login after enable should require MFA, got %d %v", code, body)
	}

	// Disable with a wrong code fails; with a recovery code it succeeds and clears state.
	code, _ = postJSON(t, r, "/api/v1/auth/mfa/disable", `{"code":"nope"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("disable with bad code status = %d, want 401", code)
	}
	code, body = postJSON(t, r, "/api/v1/auth/mfa/disable", `{"code":"`+recoveryCodes[0]+`"}`)
	if code != http.StatusOK {
		t.Fatalf("disable with recovery code status = %d body %v", code, body)
	}
	if st.users[uid].MfaEnabled || st.users[uid].MfaSecret.Valid {
		t.Fatal("disable must clear mfa_enabled and the secret")
	}
}

func TestLoginVerifyFailureCountedAgainstLimiter(t *testing.T) {
	resetMFAState()
	st := newFakeAuthStore()
	key, _ := totp.Generate(totp.GenerateOpts{Issuer: "urMail.cn", AccountName: "lim@example.com"})
	st.addUser(t, "lim@example.com", true, key.Secret(), nil)
	r := newMFATestRouter(st)

	// Each wrong code on the second leg counts against the same limiter key
	// as the first leg. The pending token only survives mfaMaxVerifyAttempts
	// wrong codes, so a fresh token is taken per round.
	var last int
	for i := 0; i < loginMaxFailures+2; i++ {
		code, body := postJSON(t, r, "/api/v1/auth/login", `{"email":"lim@example.com","password":"correct-horse"}`)
		if code == http.StatusTooManyRequests {
			last = code
			break
		}
		if last, _ = postJSON(t, r, "/api/v1/auth/mfa/verify", `{"mfa_token":"`+body["mfa_token"].(string)+`","code":"000000"}`); last == http.StatusTooManyRequests {
			break
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after %d failures the limiter must answer 429, got %d", loginMaxFailures, last)
	}
}

// AUTH-01 regression: the MFA-disable endpoint must bound online guessing of
// the six-digit TOTP per user instead of returning 401 forever.
func TestMFADisableLimitsCodeGuessing(t *testing.T) {
	resetMFAState()
	st := newFakeAuthStore()
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "urMail.cn", AccountName: "victim@example.com"})
	if err != nil {
		t.Fatalf("generate totp key: %v", err)
	}
	userID := st.addUser(t, "victim@example.com", true, key.Secret(), nil)
	st.currentUser = userID
	r := newMFATestRouter(st)

	for i := 0; i < mfaDisableMaxFailures; i++ {
		code, body := postJSON(t, r, "/api/v1/auth/mfa/disable", `{"code":"000000"}`)
		if code != http.StatusUnauthorized {
			t.Fatalf("wrong code #%d status = %d body %v", i+1, code, body)
		}
	}

	// Budget exhausted: the next attempt is rate limited, not another 401.
	code, body := postJSON(t, r, "/api/v1/auth/mfa/disable", `{"code":"000000"}`)
	if code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after budget exhausted, got %d body %v", code, body)
	}
	if st.users[userID].MfaEnabled != true {
		t.Fatal("MFA must remain enabled while locked out")
	}

	// The budget is the gate: after the window state is cleared, the valid
	// code disables MFA again.
	mfaDisableAttempts.reset(mfaDisableKey(userID))
	valid, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	code, body = postJSON(t, r, "/api/v1/auth/mfa/disable", `{"code":"`+valid+`"}`)
	if code != http.StatusOK {
		t.Fatalf("valid code after budget reset status = %d body %v", code, body)
	}
	if body["mfa_enabled"] != false {
		t.Fatalf("expected mfa_enabled=false, got %v", body)
	}
	if st.users[userID].MfaEnabled != false {
		t.Fatal("MFA should be disabled after a valid code")
	}
}

func TestRecoveryCodeHashingIsNormalised(t *testing.T) {
	code := generateDeterministicCode(t)
	if hashRecoveryCode(code) != hashRecoveryCode(strings.ToUpper(code)) {
		t.Fatal("hash must be case-insensitive")
	}
	if hashRecoveryCode(code) != hashRecoveryCode(strings.ReplaceAll(code, "-", "")) {
		t.Fatal("hash must ignore dashes")
	}
}

func generateDeterministicCode(t *testing.T) string {
	t.Helper()
	codes, err := generateRecoveryCodes(1)
	if err != nil {
		t.Fatalf("generate recovery codes: %v", err)
	}
	return codes[0]
}

var _ = bytes.MinRead // keep bytes import if unused by future edits

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	resetMFAState()
	st := newFakeAuthStore()
	id := st.addUser(t, "pw@example.test", false, "", nil)
	st.currentUser = id
	st.sessions["current"] = store.Sessions{ID: "current", UserID: id}
	st.sessions["other"] = store.Sessions{ID: "other", UserID: id}

	r := newMFATestRouter(st)
	code, _ := postJSON(t, r, "/api/v1/auth/password",
		`{"current_password":"correct-horse","new_password":"new-password-12345"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if _, ok := st.sessions["other"]; ok {
		t.Fatal("other session was not revoked")
	}
	if _, ok := st.sessions["current"]; !ok {
		t.Fatal("current session must be kept")
	}
	user, _ := st.GetUserByID(context.Background(), id)
	if !mail.VerifyPassword("new-password-12345", user.PasswordHash) {
		t.Fatal("new password was not stored")
	}
	if mail.VerifyPassword("correct-horse", user.PasswordHash) {
		t.Fatal("old password still works")
	}
}

func TestChangePasswordRejectsWrongCurrentPassword(t *testing.T) {
	resetMFAState()
	st := newFakeAuthStore()
	id := st.addUser(t, "pw2@example.test", false, "", nil)
	st.currentUser = id
	r := newMFATestRouter(st)
	code, _ := postJSON(t, r, "/api/v1/auth/password",
		`{"current_password":"wrong","new_password":"new-password-12345"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}
