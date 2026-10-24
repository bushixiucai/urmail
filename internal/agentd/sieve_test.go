package agentd

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSieveActivationRejectsUntrustedPathsAndCommands(t *testing.T) {
	manager := &ApplyManager{}
	for _, raw := range []string{`{}`, `{"tenant_id":-1,"mailbox_id":2,"digest":"abc"}`, `{"tenant_id":1,"mailbox_id":2,"digest":"abc","path":"/etc/passwd"}`, `{"tenant_id":1,"mailbox_id":2,"command":"id"}`} {
		if _, _, err := manager.ActivateSieve(json.RawMessage(raw)); !errors.Is(err, ErrRejected) {
			t.Fatalf("unsafe activation accepted %s: %v", raw, err)
		}
	}
}
