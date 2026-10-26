package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bushixiucai/urmail/internal/domain/mail"
	"github.com/bushixiucai/urmail/internal/domain/panelpassword"
	"github.com/bushixiucai/urmail/internal/store"
)

// waitForCredentialLock observes PostgreSQL's lock graph rather than assuming
// a goroutine has reached its SQL statement after an arbitrary delay.
func waitForCredentialLock(ctx context.Context, t *testing.T, pool *pgxpool.Pool, waiter, blocker uint32) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, "SELECT $1::integer = ANY(pg_blocking_pids($2::integer))", blocker, waiter).Scan(&blocked); err != nil {
			t.Fatal("observe credential lock", err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("credential statement did not wait on the expected row lock", ctx.Err())
		case <-tick.C:
		}
	}
}

func TestPanelLateLoginWaitsForPasswordRotationAndRejectsOldProof(t *testing.T) {
	pool, _ := authIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	q := store.New(pool.Pool)
	oldHash, err := mail.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := panelpassword.Hash("replacement-password")
	if err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('blocked-login@example.test',$1) RETURNING id", oldHash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	args := store.CreateSessionParams{ID: "current", UserID: uid, ExpectedHash: oldHash, ExpectedVersion: 0, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}
	if _, err = q.CreateSession(ctx, args); err != nil {
		t.Fatal(err)
	}
	rotation, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rotation.Rollback(context.Background()) }()
	var blocker uint32
	if err = rotation.QueryRow(ctx, "SELECT pg_backend_pid() FROM users WHERE id=$1 FOR UPDATE", uid).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	waiter, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	args.ID = "blocked-old-login"
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, createErr := store.New(waiter).CreateSession(ctx, args)
		result <- createErr
	}()
	defer func() {
		cancel()
		_ = rotation.Rollback(context.Background())
		<-done
		waiter.Release()
	}()
	waitForCredentialLock(ctx, t, pool.Pool, waiter.Conn().PgConn().PID(), blocker)
	version, err := store.New(rotation).PlatformRotatePanelPassword(ctx, store.PlatformRotatePanelPasswordParams{UserID: uid, ExpectedHash: oldHash, ExpectedVersion: 0, PasswordHash: newHash, KeepSession: "current"})
	if err != nil || version != 1 {
		t.Fatal("rotate while old login waits", version, err)
	}
	if err = rotation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("blocked old proof created a session after rotation", err)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE id=$1", args.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("late login left a persistent session", count, err)
	}
	kept, err := q.GetSessionByID(ctx, "current")
	if err != nil || kept.CredentialVersion != 1 {
		t.Fatal("rotation did not preserve current session at new version", err)
	}
}

func TestPanelRehashWaitsForPasswordRotationWithoutOverwritingNewPassword(t *testing.T) {
	pool, _ := authIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	q := store.New(pool.Pool)
	oldHash, err := mail.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	upgradedHash, err := panelpassword.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := panelpassword.Hash("replacement-password")
	if err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('blocked-rehash@example.test',$1) RETURNING id", oldHash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateSession(ctx, store.CreateSessionParams{ID: "current", UserID: uid, ExpectedHash: oldHash, ExpectedVersion: 0, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	rotation, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rotation.Rollback(context.Background()) }()
	var blocker uint32
	if err = rotation.QueryRow(ctx, "SELECT pg_backend_pid() FROM users WHERE id=$1 FOR UPDATE", uid).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	waiter, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type rehashResult struct {
		rows int64
		err  error
	}
	result := make(chan rehashResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		n, rehashErr := store.New(waiter).PlatformRehashPanelPassword(ctx, store.PlatformRehashPanelPasswordParams{UserID: uid, ExpectedHash: oldHash, PasswordHash: upgradedHash})
		result <- rehashResult{rows: n, err: rehashErr}
	}()
	defer func() {
		cancel()
		_ = rotation.Rollback(context.Background())
		<-done
		waiter.Release()
	}()
	waitForCredentialLock(ctx, t, pool.Pool, waiter.Conn().PgConn().PID(), blocker)
	version, err := store.New(rotation).PlatformRotatePanelPassword(ctx, store.PlatformRotatePanelPasswordParams{UserID: uid, ExpectedHash: oldHash, ExpectedVersion: 0, PasswordHash: newHash, KeepSession: "current"})
	if err != nil || version != 1 {
		t.Fatal("rotate while rehash waits", version, err)
	}
	if err = rotation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil || got.rows != 0 {
		t.Fatal("stale rehash replaced rotated hash", got.rows, got.err)
	}
	user, err := q.GetUserByID(ctx, uid)
	if err != nil || user.PasswordHash != newHash || user.CredentialVersion != 1 {
		t.Fatal("stale rehash changed new password or credential version", err)
	}
	kept, err := q.GetSessionByID(ctx, "current")
	if err != nil || kept.CredentialVersion != 1 {
		t.Fatal("stale rehash invalidated current session", err)
	}
}

func TestPanelCredentialRotationIsAtomicAndRejectsLateLogin(t *testing.T) {
	pool, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(pool.Pool)
	oldHash, err := mail.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := panelpassword.Hash("replacement-password")
	if err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('rotation@example.test',$1) RETURNING id", oldHash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	args := store.CreateSessionParams{ID: "current", UserID: uid, ExpectedHash: oldHash, ExpectedVersion: 0, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}
	if _, err = q.CreateSession(ctx, args); err != nil {
		t.Fatal(err)
	}
	args.ID = "other"
	if _, err = q.CreateSession(ctx, args); err != nil {
		t.Fatal(err)
	}
	// Fail the final audit statement to prove the password and deletions roll back.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_credential_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='auth.credential.rotate' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_credential_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION fail_credential_audit()`); err != nil {
		t.Fatal(err)
	}
	rotate := store.PlatformRotatePanelPasswordParams{UserID: uid, ExpectedHash: oldHash, ExpectedVersion: 0, PasswordHash: newHash, KeepSession: "current"}
	if _, err = q.PlatformRotatePanelPassword(ctx, rotate); err == nil {
		t.Fatal("fault did not reject rotation")
	}
	current, err := q.GetUserByID(ctx, uid)
	if err != nil || current.PasswordHash != oldHash || current.CredentialVersion != 0 {
		t.Fatal("partial password commit", err)
	}
	if _, err = q.GetSessionByID(ctx, "other"); err != nil {
		t.Fatal("partial session revocation", err)
	}
	if _, err = pool.Exec(ctx, "DROP TRIGGER reject_credential_audit ON audit_logs; DROP FUNCTION fail_credential_audit()"); err != nil {
		t.Fatal(err)
	}
	generation, err := q.PlatformRotatePanelPassword(ctx, rotate)
	if err != nil || generation != 1 {
		t.Fatal("rotation", generation, err)
	}
	if _, err = q.GetSessionByID(ctx, "other"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("old session survived", err)
	}
	kept, err := q.GetSessionByID(ctx, "current")
	if err != nil || kept.CredentialVersion != 1 {
		t.Fatal("current session missing", err)
	}
	args.ID = "late-old-login"
	if _, err = q.CreateSession(ctx, args); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("late old proof created session", err)
	}
	args.ID = "new-login"
	args.ExpectedHash = newHash
	args.ExpectedVersion = 1
	if _, err = q.CreateSession(ctx, args); err != nil {
		t.Fatal(err)
	}
	// A stale version may not change the password even if its hash is current.
	rotate.ExpectedHash = newHash
	rotate.PasswordHash = oldHash
	if _, err = q.PlatformRotatePanelPassword(ctx, rotate); err == nil {
		t.Fatal("stale credential generation rotated password")
	}
}

func TestMFAChangesRevokeSessionsAndRejectNewPlaintext(t *testing.T) {
	pool, _ := authIntegrationDatabase(t)
	ctx := context.Background()
	q := store.New(pool.Pool)
	hash, err := mail.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('mfa-generation@example.test',$1) RETURNING id", hash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE users SET mfa_secret='PLAINTEXT' WHERE id=$1", uid); err == nil {
		t.Fatal("new plaintext seed accepted")
	}
	seed, err := authFixtureSecrets().EncryptString("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	args := store.CreateSessionParams{ID: "current", UserID: uid, ExpectedHash: hash, ExpectedVersion: 0, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}
	if _, err = q.CreateSession(ctx, args); err != nil {
		t.Fatal(err)
	}
	args.ID = "other"
	if _, err = q.CreateSession(ctx, args); err != nil {
		t.Fatal(err)
	}
	gen, err := q.PlatformChangePanelMFA(ctx, store.PlatformChangePanelMFAParams{UserID: uid, ExpectedVersion: 0, Secret: pgtype.Text{String: seed, Valid: true}, Enabled: true, Codes: []byte("[]"), KeepSession: "current"})
	if err != nil || gen != 1 {
		t.Fatal(gen, err)
	}
	if _, err = q.GetSessionByID(ctx, "other"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("MFA change retained other session", err)
	}
	args.ID = "stale-mfa-login"
	if _, err = q.CreateSession(ctx, args); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("old MFA proof accepted", err)
	}
	// Hash migration is compare-and-swap and does not revoke credential identity.
	upgraded, err := panelpassword.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if n, e := q.PlatformRehashPanelPassword(ctx, store.PlatformRehashPanelPasswordParams{UserID: uid, ExpectedHash: hash, PasswordHash: upgraded}); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	user, err := q.GetUserByID(ctx, uid)
	if err != nil || user.CredentialVersion != 1 {
		t.Fatal("rehash changed generation", err)
	}
	if _, err = q.GetSessionByID(ctx, "current"); err != nil {
		t.Fatal("rehash revoked live session", err)
	}
}

func TestCredentialMutationsRevalidateAuthenticatedSession(t *testing.T) {
	pool, _ := authIntegrationDatabase(t)
	ctx := t.Context()
	q := store.New(pool.Pool)
	hash, err := mail.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash) VALUES('revoked-request@example.test',$1) RETURNING id", hash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	create := store.CreateSessionParams{ID: "revoked", UserID: uid, ExpectedHash: hash, ExpectedVersion: 0, CsrfToken: "csrf", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}
	if _, err = q.CreateSession(ctx, create); err != nil {
		t.Fatal(err)
	}
	if err = q.DeleteSession(ctx, "revoked"); err != nil {
		t.Fatal(err)
	}
	seed, err := authFixtureSecrets().EncryptString("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.PlatformChangePanelMFA(ctx, store.PlatformChangePanelMFAParams{UserID: uid, ExpectedVersion: 0, Secret: pgtype.Text{String: seed, Valid: true}, Enabled: true, Codes: []byte("[]"), KeepSession: "revoked"}); err == nil {
		t.Fatal("revoked request changed MFA")
	}
	if _, err = q.PlatformRotatePanelPassword(ctx, store.PlatformRotatePanelPasswordParams{UserID: uid, ExpectedHash: hash, ExpectedVersion: 0, PasswordHash: "replacement", KeepSession: "revoked"}); err == nil {
		t.Fatal("revoked request changed password")
	}
	user, err := q.GetUserByID(ctx, uid)
	if err != nil || user.CredentialVersion != 0 || user.PasswordHash != hash || user.MfaEnabled {
		t.Fatal("rejected request mutated credentials", err)
	}
}
