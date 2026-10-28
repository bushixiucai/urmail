package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bushixiucai/urmail/internal/agentd"
	recall "github.com/bushixiucai/urmail/internal/domain/recall"
)

// PreviewRecall obtains a fixed, role-authorized local-copy manifest from the agent.
func (c *Client) PreviewRecall(ctx context.Context, tenantID, entryID, actorID int64) (recall.Snapshot, error) {
	response, err := c.Do(ctx, agentd.OpMailboxRecallPreview, agentd.RecallPreviewParams{TenantID: tenantID, EntryID: entryID, ActorID: actorID})
	if err != nil {
		return recall.Snapshot{}, err
	}
	var snapshot recall.Snapshot
	if err = json.Unmarshal([]byte(response.Stdout), &snapshot); err != nil {
		return recall.Snapshot{}, err
	}
	if snapshot.TenantID != tenantID || snapshot.EntryID != entryID || snapshot.ActorID != actorID {
		return recall.Snapshot{}, fmt.Errorf("recall preview identity mismatch")
	}
	if _, err = snapshot.Canonical(); err != nil {
		return recall.Snapshot{}, err
	}
	return snapshot, nil
}

// ExpungeReviewedRecall binds the mutation to one confirmed, persisted attempt.
func (c *Client) ExpungeReviewedRecall(ctx context.Context, tenantID, recallID, epoch int64, token string) error {
	_, err := c.Do(ctx, agentd.OpMailboxExpunge, agentd.RecallParams{TenantID: tenantID, RecallID: recallID, AttemptEpoch: epoch, PreviewToken: token})
	return err
}
