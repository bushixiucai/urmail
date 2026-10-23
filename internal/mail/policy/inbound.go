package policy

import "context"

// InboundStore resolves the effective subscription of a recipient domain.
type InboundStore interface {
	PlatformInboundAllowed(context.Context, string) (bool, error)
}

// DBInboundChecker gates local recipients before SMTP accepts message data.
// External recipients remain subject to Postfix's normal relay restrictions.
type DBInboundChecker struct{ Store InboundStore }

// CheckRecipient reports whether a local domain may accept mail.
func (c *DBInboundChecker) CheckRecipient(ctx context.Context, address string) (bool, error) {
	return c.Store.PlatformInboundAllowed(ctx, address)
}
