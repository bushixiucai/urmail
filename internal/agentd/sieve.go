package agentd

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

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
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.pool.PlatformTx(ctx)
	if err != nil {
		return "", "", ErrFailed
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	target, err := store.New(tx).PlatformGetSieveActivationTarget(ctx, store.PlatformGetSieveActivationTargetParams{TenantID: params.TenantID, ID: params.MailboxID})
	if err != nil || target.SieveMode != "managed" || !target.SieveScript.Valid || !safeKickAddress(target.Address) || sieve.Digest(target.SieveScript.String) != params.Digest {
		return "", "", ErrRejected
	}
	rules, err := sieve.Decode(target.SieveScript.String)
	if err != nil {
		return "", "", ErrRejected
	}
	// Recompile the typed policy so the accepted historical missing-copy
	// declaration is repaired without changing its intended behavior.
	canonical, err := sieve.Compile(rules)
	if err != nil {
		return "", "", ErrRejected
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
	if m.run(ctx, "/usr/bin/sievec", source, filepath.Join(stage, "script.svbin")) != nil {
		return "", "", ErrRejected
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
		return "", "", ErrFailed
	}
	// This transaction only locks the authoritative source. Activation is the
	// successful atomic filesystem publication, not a database commit.
	_ = tx.Rollback(context.Background())
	return params.Digest, "", nil
}
