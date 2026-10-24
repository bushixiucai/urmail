package agentd

import (
	"encoding/json"
	"testing"

	"github.com/bushixiucai/urmail/internal/config"
)

func TestQuarantineRechecksAllMutationAndAllowsDiagnostics(t *testing.T) {
	r := NewRegistry()
	quarantined := true
	r.quarantined = func() bool { return quarantined }
	called := 0
	op := func(json.RawMessage) (string, string, error) { called++; return "ok", "", nil }
	for _, name := range []string{OpConfigApply, OpMailboxExpunge, OpCertificateIssue, OpCertificateRenew, OpMailLogRead, OpPostfixQueue, OpConfigStatus, OpHostObserve, "future.mutation"} {
		r.Register(name, op)
		resp := r.Execute(Request{Version: ProtocolVersion, Op: name})
		if quarantineReadOnly(name) {
			if !resp.OK {
				t.Fatal(resp)
			}
		} else if resp.OK || resp.Error.Code != CodeRejected {
			t.Fatal(resp)
		}
	}
	if called != 4 {
		t.Fatal(called)
	}
	quarantined = false
	if !r.Execute(Request{Version: ProtocolVersion, Op: OpConfigApply}).OK {
		t.Fatal("release was not observed")
	}
}

func TestProductionRegistryAlsoHonorsConfiguredQuarantine(t *testing.T) {
	manager := &ApplyManager{cfg: &config.Config{Quarantined: true}}
	registry := NewProductionRegistry(manager)
	response := registry.Execute(Request{Version: ProtocolVersion, Op: OpConfigApply, Params: []byte(`{}`)})
	if response.OK || response.Error == nil || response.Error.Code != CodeRejected {
		t.Fatal("configured quarantine did not stop privileged mutation")
	}
}
