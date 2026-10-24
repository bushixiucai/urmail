package agent

import (
	"context"

	"github.com/bushixiucai/urmail/internal/agentd"
)

// ActivateSieve asks the agent to compile the persisted mailbox policy.
func (c *Client) ActivateSieve(ctx context.Context, tenantID, mailboxID int64, digest string) error {
	_, err := c.Do(ctx, agentd.OpSieveActivate, agentd.SieveActivateParams{TenantID: tenantID, MailboxID: mailboxID, Digest: digest})
	return err
}
