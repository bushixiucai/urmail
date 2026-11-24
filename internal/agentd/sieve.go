package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/db/isolation"
	"github.com/bushixiucai/urmail/internal/sieve"
	"github.com/bushixiucai/urmail/internal/store"
)

// SieveActivateParams names a persisted mailbox and its expected script.
type SieveActivateParams struct {
	TenantID  int64  `json:"tenant_id"`
	MailboxID int64  `json:"mailbox_id"`
	Digest    string `json:"digest"`
}

// ActivateSieve resolves the exact persisted mailbox and script. It compiles in
// root-private staging before publishing one atomic source file; rejected or
// uncompilable input never touches the currently active script.
func (m *ApplyManager) ActivateSieve(raw json.RawMessage) (string, string, error) {
	var params SieveActivateParams
	if strictParams(raw, &params) != nil || params.TenantID <= 0 || params.MailboxID <= 0 || len(params.Digest) != 64 {
		return "", "", ErrRejected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Trusted platform entry: the sieve activation statements are platform-scoped.
	ctx = isolation.WithScope(ctx, isolation.ScopeContext{Scope: isolation.ScopePlatform})
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.pool.PlatformTx(ctx)
	if err != nil {
		return "", "", ErrFailed
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	target, err := db.Queries(tx).PlatformGetSieveActivationTarget(ctx, store.PlatformGetSieveActivationTargetParams{TenantID: params.TenantID, ID: params.MailboxID})
	if err != nil {
		return "", "", fmt.Errorf("%w: read activation target: %v", ErrRejected, err)
	}
	if target.SieveMode != "managed" || !target.SieveScript.Valid || !safeKickAddress(target.Address) || sieve.Digest(target.SieveScript.String) != params.Digest {
		return "", "", fmt.Errorf("%w: activation target mismatch (mode=%q valid=%t digest_match=%t)", ErrRejected, target.SieveMode, target.SieveScript.Valid, sieve.Digest(target.SieveScript.String) == params.Digest)
	}
	rules, err := sieve.Decode(target.SieveScript.String)
	if err != nil {
		return "", "", fmt.Errorf("%w: decode stored sieve: %v", ErrRejected, err)
	}
	// Recompile the typed policy so the accepted historical missing-copy
	// declaration is repaired without changing its intended behavior.
	canonical, err := sieve.Compile(rules)
	if err != nil {
		return "", "", fmt.Errorf("%w: compile canonical sieve: %v", ErrRejected, err)
	}
	stage, err := os.MkdirTemp(m.root, "sieve-*")
	if err != nil {
		return "", "", ErrFailed
	}
	defer func() { _ = os.RemoveAll(stage) }()
	source := filepath.Join(stage, "script.sieve")
	if err = os.WriteFile(source, []byte(canonical), 0600); err != nil {
		return "", "", ErrFailed
	}
	if runErr := m.run(ctx, "/usr/bin/sievec", source, filepath.Join(stage, "script.svbin")); runErr != nil {
		return "", "", fmt.Errorf("%w: sievec: %v", ErrRejected, runErr)
	}
	// Publishing only the validated source avoids a torn source/binary pair.
	// LMTP can compile its private in-memory representation without write access.
	root := m.target(m.cfg.StorageRoot)
	destination := filepath.Join(root, "sieve", target.Address+".sieve")
	uid, gid := -1, -1
	if os.Geteuid() == 0 {
		group, lookupErr := user.LookupGroup("mail")
		if lookupErr != nil {
			return "", "", ErrFailed
		}
		gid, err = strconv.Atoi(group.Gid)
		if err != nil {
			return "", "", ErrFailed
		}
		uid = 0
	}
	if _, err = writeFileAtomicOwned(destination, []byte(canonical), 0640, []string{root}, uid, gid); err != nil {
		return "", "", fmt.Errorf("publish sieve: %w", err)
	}
	// This transaction only locks the authoritative source. Activation is the
	// successful atomic filesystem publication, not a database commit.
	_ = tx.Rollback(context.Background())
	return params.Digest, "", nil
}
