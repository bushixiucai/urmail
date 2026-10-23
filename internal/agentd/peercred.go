package agentd

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// Peer authentication policy.
//
// The agent is reachable only through a unix socket. On Linux every accepted
// connection is checked with SO_PEERCRED before a single request byte is
// interpreted: the peer's effective UID must be either
//
//   - the agent's own effective UID. In production the agent runs as root, so
//     this lets root (which already has unrestricted access) in; under
//     "go test" it lets the invoking developer's UID in; or
//   - the mailinabox service account that urmaild runs as.
//
// Every other local user is rejected. Non-Linux builds skip the kernel check
// (see peercred_other.go); they exist for development and tests only, because
// the production agent is Linux-only.
type peerPolicy struct {
	allowed map[uint32]struct{}
}

// newPeerPolicy builds a policy that allows exactly the given UIDs.
func newPeerPolicy(uids ...uint32) *peerPolicy {
	policy := &peerPolicy{allowed: make(map[uint32]struct{}, len(uids))}
	for _, uid := range uids {
		policy.allowed[uid] = struct{}{}
	}
	return policy
}

// allows reports whether a peer with the given effective UID may connect.
func (p *peerPolicy) allows(uid uint32) bool {
	if p == nil {
		return false
	}
	_, ok := p.allowed[uid]
	return ok
}

// loadPeerPolicy builds the policy used by the server: the current process UID
// plus the mailinabox account when it exists.
//
// A missing mailinabox account is returned as an error so the caller can log a
// clear warning, but the agent's own UID stays allowed: the daemon remains
// reachable by root (and by the test user) while the account is repaired.
func loadPeerPolicy() (*peerPolicy, error) {
	current := uint32(os.Geteuid()) // #nosec G115 -- Linux/Darwin UID values use uint32.

	account, err := user.Lookup("mailinabox")
	if err != nil {
		return newPeerPolicy(current), fmt.Errorf("look up mailinabox account: %w", err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return newPeerPolicy(current), fmt.Errorf("parse mailinabox uid %q: %w", account.Uid, err)
	}
	return newPeerPolicy(current, uint32(uid)), nil
}
