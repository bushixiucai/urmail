package handlers

import (
	"context"
	"errors"
	"strings"
	"unicode"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/domain/passwordwork"
	"github.com/bushixiucai/urmail/internal/mailboxprincipal"
	"github.com/bushixiucai/urmail/internal/store"
)

var (
	errMailboxPasswordPolicy   = errors.New("mailbox password policy rejected")
	errMailboxPasswordCurrent  = errors.New("current mailbox password rejected")
	errMailboxPasswordConflict = errors.New("mailbox password changed concurrently")
	errMailboxPasswordRate     = errors.New("mailbox password attempts limited")
)

type mailboxPasswordIdentity struct{ TenantID, MailboxID int64 }

// validMailboxPassword does not trim or normalize a user's secret.
func validMailboxPassword(oldPassword, newPassword string) bool {
	if !passwordwork.ValidVerification(oldPassword) || passwordwork.ValidateNew(newPassword) != nil || oldPassword == newPassword {
		return false
	}
	return !strings.ContainsFunc(newPassword, unicode.IsControl)
}

// changeMailboxPassword trusts only the existing mailbox session and a fresh
// old-password proof. Service login signatures never substitute for that proof.
func changeMailboxPassword(ctx context.Context, q *store.Queries, input mailboxBridgeRequest) (mailboxPasswordIdentity, error) {
	identity := mailboxPasswordIdentity{}
	if len(input.Address) == 0 || len(input.Address) > 320 || !validMailboxPassword(input.OldPassword, input.NewPassword) {
		return identity, errMailboxPasswordPolicy
	}
	current, err := q.PlatformMailboxPasswordForSession(ctx, store.PlatformMailboxPasswordForSessionParams{SessionHash: mailboxprincipal.HashToken(input.Session), Address: input.Address})
	if err != nil {
		return identity, err
	}
	identity = mailboxPasswordIdentity{TenantID: current.TenantID, MailboxID: current.MailboxID}
	failures, err := q.PlatformMailboxPasswordFailures(ctx, store.PlatformMailboxPasswordFailuresParams{TenantID: identity.TenantID, MailboxID: identity.MailboxID})
	if err != nil {
		return identity, err
	}
	if failures >= 10 {
		return identity, errMailboxPasswordRate
	}
	matches, verifyErr := domainMail.VerifyPasswordContext(ctx, input.OldPassword, current.PasswordHash)
	if verifyErr != nil {
		return identity, verifyErr
	}
	if !matches {
		return identity, errMailboxPasswordCurrent
	}
	hash, err := domainMail.HashPasswordContext(ctx, input.NewPassword)
	if err != nil {
		return identity, err
	}
	changed, err := q.PlatformChangeMailboxPasswordCAS(ctx, store.PlatformChangeMailboxPasswordCASParams{TenantID: identity.TenantID, MailboxID: identity.MailboxID, Address: current.Address, ExpectedHash: current.PasswordHash, ExpectedVersion: current.PasswordVersion, PasswordHash: hash})
	if err != nil {
		return identity, err
	}
	if changed != 1 {
		return identity, errMailboxPasswordConflict
	}
	if err = q.PlatformRevokeMailboxCredentials(ctx, store.PlatformRevokeMailboxCredentialsParams{TenantID: identity.TenantID, MailboxID: identity.MailboxID}); err != nil {
		return identity, err
	}
	return identity, nil
}
