package handlers

import (
	"encoding/json"

	"github.com/bushixiucai/urmail/internal/domain/mailboxsettings"
)

// mergeMailboxPreferences restores every stored key that a legacy client
// omitted from its full-object PUT. Preferences are documented as a whole
// object, but older clients cannot know keys added after their release:
// without this merge each legacy save would silently reset newer
// preferences (for example allow_all_remote_images) back to their default.
// Keys present in the request always win; merged documents are revalidated
// through the same bounded Preferences contract.
func mergeMailboxPreferences(current, previous json.RawMessage) (json.RawMessage, error) {
	var cur, old map[string]json.RawMessage
	if json.Unmarshal(current, &cur) != nil || cur == nil {
		return nil, mailboxsettings.ErrPreferences
	}
	_ = json.Unmarshal(previous, &old)
	for key, value := range old {
		if _, exists := cur[key]; !exists {
			cur[key] = value
		}
	}
	merged, err := json.Marshal(cur)
	if err != nil {
		return nil, mailboxsettings.ErrPreferences
	}
	return mailboxsettings.Preferences(merged)
}
