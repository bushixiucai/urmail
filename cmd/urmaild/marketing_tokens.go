package main

import (
	"github.com/bushixiucai/urmail/internal/config"
	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

// unsubscribeTokenVerifier mirrors the consumer interface in
// internal/api/router.go. Declaring the return type as the INTERFACE (not
// *domainMail.SuppressionManager) is deliberate: a nil concrete pointer
// assigned to an interface is non-nil, which previously defeated the
// handler's nil guard and made the public unsubscribe endpoint panic when no
// signing key was configured (M5 review 3.13). Returning an untyped nil keeps
// the fail-closed 503 behavior.
func marketingTokenManager(cfg *config.Config) interface {
	VerifyUnsubscribeToken(string) (int64, string, bool)
} {
	if cfg == nil || cfg.MarketingUnsubscribeHMACKey == "" {
		return nil
	}
	manager, err := domainMail.NewSuppressionManager([]byte(cfg.MarketingUnsubscribeHMACKey))
	if err != nil {
		return nil
	}
	return manager
}
