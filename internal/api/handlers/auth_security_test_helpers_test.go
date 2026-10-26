package handlers

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/bushixiucai/urmail/internal/secrets"
	"github.com/bushixiucai/urmail/internal/store"
)

func (f *fakeAuthStore) PlatformRotatePanelPassword(ctx context.Context, p store.PlatformRotatePanelPasswordParams) (int64, error) {
	u, ok := f.users[p.UserID]
	if !ok || u.PasswordHash != p.ExpectedHash || u.CredentialVersion != p.ExpectedVersion {
		return 0, pgx.ErrNoRows
	}
	u.PasswordHash = p.PasswordHash
	u.CredentialVersion++
	_ = f.DeleteOtherUserSessions(ctx, store.DeleteOtherUserSessionsParams{UserID: p.UserID, ID: p.KeepSession})
	if current, ok := f.sessions[p.KeepSession]; ok {
		current.CredentialVersion = u.CredentialVersion
		f.sessions[p.KeepSession] = current
	}
	return u.CredentialVersion, nil
}
func (f *fakeAuthStore) PlatformChangePanelMFA(ctx context.Context, p store.PlatformChangePanelMFAParams) (int64, error) {
	u, ok := f.users[p.UserID]
	if !ok || u.CredentialVersion != p.ExpectedVersion {
		return 0, pgx.ErrNoRows
	}
	u.MfaSecret = p.Secret
	u.MfaEnabled = p.Enabled
	u.MfaRecoveryCodes = p.Codes
	u.CredentialVersion++
	_ = f.DeleteOtherUserSessions(ctx, store.DeleteOtherUserSessionsParams{UserID: p.UserID, ID: p.KeepSession})
	if current, ok := f.sessions[p.KeepSession]; ok {
		current.CredentialVersion = u.CredentialVersion
		f.sessions[p.KeepSession] = current
	}
	return u.CredentialVersion, nil
}
func (f *fakeAuthStore) PlatformRehashPanelPassword(_ context.Context, p store.PlatformRehashPanelPasswordParams) (int64, error) {
	u, ok := f.users[p.UserID]
	if !ok || u.PasswordHash != p.ExpectedHash {
		return 0, nil
	}
	u.PasswordHash = p.PasswordHash
	return 1, nil
}

func authFixtureSecrets() SecretsBox {
	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		panic(err)
	}
	return box
}
