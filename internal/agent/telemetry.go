package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bushixiucai/urmail/internal/agentd"
)

// QueueSnapshot requests the fixed read-only Postfix envelope snapshot.
func (c *Client) QueueSnapshot(ctx context.Context) (agentd.QueueSnapshot, error) {
	var snapshot agentd.QueueSnapshot
	response, err := c.Do(ctx, agentd.OpPostfixQueue, struct{}{})
	if err != nil {
		return snapshot, err
	}
	if err = json.Unmarshal([]byte(response.Stdout), &snapshot); err != nil {
		return snapshot, errors.New("invalid typed queue snapshot response")
	}
	return snapshot, nil
}
