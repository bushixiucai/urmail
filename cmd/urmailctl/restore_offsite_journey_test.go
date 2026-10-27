package main

// OPS-002 journey tests: a REAL encrypted offsite restore round trip. The
// backupauto pipeline (backup -> encrypt -> upload -> verify), the age
// decryption ("backup decrypt"), the manifest v2 verification and the
// database restore machinery (TOC filtering, ownership finalization,
// side-effect holds, runtime privilege verification, quarantine isolation
// and explicit release) all run for real against a real PostgreSQL cluster.
// A local directory stands in for the offsite destination; only the
// transport differs from S3/SFTP, never the cryptography or the database
// restore path.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bushixiucai/urmail/internal/backupauto"
	"github.com/bushixiucai/urmail/internal/db"
	"github.com/bushixiucai/urmail/internal/testdb"
)

const (
	journeyRestoreID  = "journey-offsite-restore-0001"
	journeyTenantSlug = "journey-replay"
	journeyDomain     = "journey.example.test"
)

var (
	journeyPGBinResolved bool
	journeyPGBin         struct {
		dump, restore, psql string
	}
)

// journeyRequireBinaries skips cleanly when the real PostgreSQL tools are
// absent; the journey never execs urmailctl itself, only these fixed tools.
func journeyRequireBinaries(t *testing.T) {
	t.Helper()
	for _, name := range []string{"pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("PostgreSQL utilities unavailable; skipping restore journey")
		}
	}
	if !journeyPGBinResolved {
		dump, _ := exec.LookPath("pg_dump")
		restore, _ := exec.LookPath("pg_restore")
		psql, _ := exec.LookPath("psql")
		journeyPGBin.dump, journeyPGBin.restore, journeyPGBin.psql = dump, restore, psql
		journeyPGBinResolved = true
	}
}

func journeyRoleURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("role", "urmail_migrate")
	u.RawQuery = q.Encode()
	return u.String()
}

// journeyPSToolURL adapts a pgx-style DSN for the native PostgreSQL client
// tools. pgx understands ?role=..., libpq does not (it rejects the parameter),
// so the role is forwarded through the options keyword instead.
func journeyPSToolURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if role := q.Get("role"); role != "" {
		q.Del("role")
		options := "-c role=" + role
		if existing := q.Get("options"); existing != "" {
			options = existing + " " + options
		}
		q.Set("options", options)
	}
	// libpq does not decode "+" as a space in URI parameters, so encode
	// spaces explicitly.
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String()
}

func journeyMigrate(t *testing.T, dsn string) {
	t.Helper()
	migrator, err := db.NewMigrator(journeyRoleURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.Up(context.Background()); err != nil {
		_ = migrator.Close()
		t.Fatal(err)
	}
	if err = migrator.Close(); err != nil {
		t.Fatal(err)
	}
}

// dirOffsite is a real local-directory offsite destination: objects land as
// files whose bytes must survive a download unchanged. It implements the
// same OffsiteStore contract the S3 and SFTP clients satisfy.
type dirOffsite struct{ dir string }

func (s dirOffsite) Upload(_ context.Context, name string, r io.Reader, _ int64) error {
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return err
	}
	path := tmp.Name()
	if _, err = io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return err
	}
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return err
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return os.Rename(path, filepath.Join(s.dir, name))
}

func (s dirOffsite) List(_ context.Context) ([]backupauto.RemoteObject, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var objects []backupauto.RemoteObject
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		objects = append(objects, backupauto.RemoteObject{Name: entry.Name(), Size: info.Size(), LastModified: info.ModTime()})
	}
	return objects, nil
}

func (s dirOffsite) Download(_ context.Context, name string, w io.Writer) error {
	f, err := os.Open(filepath.Join(s.dir, name)) // #nosec G304 -- test-owned directory with fixed object names.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return err
}

func (s dirOffsite) Delete(_ context.Context, name string) error {
	return os.Remove(filepath.Join(s.dir, name))
}

// journeyRunner hands the real pg_dump artifact to the backupauto pipeline.
type journeyRunner struct {
	dumpPath string
}

func (r journeyRunner) RunBackup(_ context.Context, stamp string) ([]backupauto.LocalArtifact, string, bool, error) {
	f, err := os.Open(r.dumpPath) // #nosec G304 -- test-owned spool path.
	if err != nil {
		return nil, "", false, err
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		_ = f.Close()
		return nil, "", false, err
	}
	info, err := f.Stat()
	_ = f.Close()
	if err != nil {
		return nil, "", false, err
	}
	return []backupauto.LocalArtifact{{
		Path: r.dumpPath, Name: "urmail-" + stamp + ".dump", Kind: backupauto.KindPgDump,
		Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil)),
	}}, backupauto.ScopeComplete, true, nil
}

func journeyConnect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// journeySeed plants the mail-adjacent facts the restore must preserve.
func journeySeed(t *testing.T, dsn string) {
	t.Helper()
	conn := journeyConnect(t, dsn)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	// brand/tenant/domain/mailbox (canonical ownership), the per-mailbox
	// settings row (migration 0126) and a subscription renewal fact in the
	// migration-0087 fixture style.
	script := "DO $seed$ DECLARE b bigint;t bigint;d bigint;m bigint; BEGIN\n" +
		" INSERT INTO brands(slug,name) VALUES('journey-source','Journey Source') RETURNING id INTO b;\n" +
		" INSERT INTO tenants(brand_id,name,slug,status) VALUES(b,'Journey Tenant','" + journeyTenantSlug + "','active') RETURNING id INTO t;\n" +
		" INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES(t,'" + journeyDomain + "','active','journey-token',now()) RETURNING id INTO d;\n" +
		" INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES(t,d,'alice','alice@" + journeyDomain + "','journey-hash','active') RETURNING id INTO m;\n" +
		" INSERT INTO mailbox_settings(mailbox_id,tenant_id,display_name,signature_text,preferences) VALUES(m,t,'Journey Alice','sent from the journey','{\"signature_default\":true}'::jsonb);\n" +
		" INSERT INTO subscription_renewal_attempts(tenant_id,subscription_id,period_end,state) VALUES(t,4242,now()+interval '24 hours','pending');\n" +
		"END $seed$;"
	if _, err := conn.Exec(ctx, script); err != nil {
		t.Fatal(err)
	}
}

func journeyWriteIdentity(t *testing.T, dir string) (identityPath, recipient string) {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	// Each call must produce a distinct file: tests deliberately create a
	// second, wrong identity alongside a valid one and then decrypt with the
	// valid key. Reusing one filename silently replaced the good key.
	file, err := os.CreateTemp(dir, "journey-identity-*.key")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	if _, err = file.WriteString(identity.String() + "\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, identity.Recipient().String()
}

func journeyBeginQuarantine(t *testing.T, dsn string) {
	t.Helper()
	pool, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.PlatformTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var active bool
	if err = tx.QueryRow(context.Background(), "SELECT urmail_begin_restore_quarantine()").Scan(&active); err != nil || !active {
		t.Fatalf("begin restore quarantine: active=%t %v", active, err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func journeyRelease(t *testing.T, dsn string) bool {
	t.Helper()
	pool, err := db.OpenPlatform(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.PlatformTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var released bool
	if err = tx.QueryRow(context.Background(), "SELECT urmail_release_restore_quarantine()").Scan(&released); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return released
}

// journeyRefusalErr reports whether err is the restore-quarantine guard
// rejection (SQLSTATE 55000).
func journeyRefusalErr(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55000"
}

// journeyAssertFenced proves new tenants, domains and mailboxes are refused
// while the isolation marker is active.
func journeyAssertFenced(t *testing.T, dsn string) {
	t.Helper()
	conn := journeyConnect(t, dsn)
	ctx := context.Background()
	insertTenant := "INSERT INTO tenants(brand_id,name,slug,status) SELECT id,'Fence Probe','fence-probe','active' FROM brands WHERE slug='journey-source'"
	if _, err := conn.Exec(ctx, insertTenant); !journeyRefusalErr(err) {
		t.Fatalf("new tenant must be refused while restore quarantine is active, got %v", err)
	}
	insertDomain := "INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) SELECT t.id,'fence." + journeyDomain + "','active','tok',now() FROM tenants t WHERE t.slug='" + journeyTenantSlug + "'"
	if _, err := conn.Exec(ctx, insertDomain); !journeyRefusalErr(err) {
		t.Fatalf("new domain must be refused while restore quarantine is active, got %v", err)
	}
	insertMailbox := "INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) SELECT t.id,d.id,'fence','fence@" + journeyDomain + "','x','active' FROM tenants t JOIN domains d ON d.tenant_id=t.id WHERE t.slug='" + journeyTenantSlug + "' AND d.fqdn='" + journeyDomain + "'"
	if _, err := conn.Exec(ctx, insertMailbox); !journeyRefusalErr(err) {
		t.Fatalf("new mailbox must be refused while restore quarantine is active, got %v", err)
	}
}

// journeyRestoreDatabaseArchive mirrors restoreDatabaseArchive using the
// PATH-resolved tools (the production function pins /usr/bin, which does
// not exist off the isolated Linux container). The database machinery it
// drives is the production one: db.FilterRestoreTOC, db.RestoreSQLPrefix
// and the all-in-one-transaction psql consumer.
func journeyRestoreDatabaseArchive(ctx context.Context, dsn, dump, workDir string) error {
	list := exec.CommandContext(ctx, journeyPGBin.restore, "--list", "--", dump)
	var toc restoreTOCBuffer
	list.Stdout = &toc
	list.Stderr = io.Discard
	if err := list.Run(); err != nil {
		return fmt.Errorf("read restore object list: %w", err)
	}
	filtered, err := db.FilterRestoreTOC(toc.String())
	if err != nil {
		return err
	}
	listPath := filepath.Join(workDir, "journey-restore.list")
	if err = os.WriteFile(listPath, []byte(filtered), 0o600); err != nil {
		return err
	}
	sqlPath := filepath.Join(workDir, "journey-restore.sql")
	producer := exec.CommandContext(ctx, journeyPGBin.restore, "--no-owner", "--use-list", listPath, "--file=-", "--", dump)
	var sqlBuffer bytes.Buffer
	producer.Stdout = &sqlBuffer
	producer.Stderr = io.Discard
	if err = producer.Run(); err != nil {
		return fmt.Errorf("prepare complete restore SQL: %w", err)
	}
	full := db.RestoreSQLPrefix + sqlBuffer.String() + "\nCOMMIT;\n"
	if err = os.WriteFile(sqlPath, []byte(full), 0o600); err != nil {
		return err
	}
	consumer := exec.CommandContext(ctx, journeyPGBin.psql, "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--file", sqlPath, "--dbname", journeyPSToolURL(journeyRoleURL(dsn)))
	var stderr bytes.Buffer
	consumer.Stderr = &stderr
	if err = consumer.Run(); err != nil {
		return fmt.Errorf("atomic database restore failed; writers remain quarantined: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func journeyRestorePipeline(t *testing.T, dsn, dump, restoreID string) {
	t.Helper()
	ctx := context.Background()
	work := t.TempDir()
	if err := journeyRestoreDatabaseArchive(ctx, dsn, dump, work); err != nil {
		t.Fatal(err)
	}
	if err := db.FinalizeRestore(ctx, journeyRoleURL(dsn), restoreID); err != nil {
		t.Fatal(err)
	}
	migrator, err := db.NewMigrator(journeyRoleURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrator.Close() }()
	if err = migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.HoldRestoredSideEffects(ctx, journeyRoleURL(dsn), restoreID); err != nil {
		t.Fatal(err)
	}
	if err = db.VerifyRuntimePrivileges(ctx, journeyRoleURL(dsn)); err != nil {
		t.Fatal(err)
	}
}

// journeyAssertFacts proves the mail-adjacent facts survived with correct
// ownership and that the restore holds are observable.
func journeyAssertFacts(t *testing.T, dsn string) {
	t.Helper()
	conn := journeyConnect(t, dsn)
	ctx := context.Background()
	var tenantCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM tenants WHERE slug='"+journeyTenantSlug+"'").Scan(&tenantCount); err != nil || tenantCount != 1 {
		t.Fatalf("restored tenant fact lost: count=%d %v", tenantCount, err)
	}
	var settingsOK bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(\n"+
		" SELECT 1 FROM mailbox_settings s JOIN mailboxes m ON m.id=s.mailbox_id AND m.tenant_id=s.tenant_id\n"+
		" JOIN domains d ON d.id=m.domain_id AND d.tenant_id=m.tenant_id\n"+
		" WHERE m.address='alice@"+journeyDomain+"' AND d.fqdn='"+journeyDomain+"' AND s.display_name='Journey Alice' AND s.signature_text='sent from the journey')").Scan(&settingsOK); err != nil || !settingsOK {
		t.Fatalf("restored mailbox_settings fact (0126) lost or mis-owned: %v %v", settingsOK, err)
	}
	var renewalOK bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(\n"+
		" SELECT 1 FROM subscription_renewal_attempts r JOIN tenants t ON t.id=r.tenant_id\n"+
		" WHERE t.slug='"+journeyTenantSlug+"' AND r.subscription_id=4242 AND r.state='canceled' AND r.last_error LIKE '%RESTORE_RECONCILIATION_REQUIRED%')").Scan(&renewalOK); err != nil || !renewalOK {
		t.Fatalf("restored subscription fact (0087 style) lost: %v %v", renewalOK, err)
	}
	var decoyAbsent bool
	if err := conn.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM brands WHERE slug='journey-decoy')").Scan(&decoyAbsent); err != nil || !decoyAbsent {
		t.Fatalf("target pre-restore fixture survived the overwrite: %v %v", decoyAbsent, err)
	}
	var platformOwned bool
	if err := conn.QueryRow(ctx, "SELECT bool_and(tableowner='urmail_platform') FROM pg_tables\n"+
		" WHERE schemaname='public' AND tablename IN ('tenants','domains','mailboxes','mailbox_settings')").Scan(&platformOwned); err != nil || !platformOwned {
		t.Fatalf("restored ownership is not the runtime platform owner: %v %v", platformOwned, err)
	}
	var reviewRecorded bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM settings WHERE scope='platform' AND key='restore_replay_review')").Scan(&reviewRecorded); err != nil || !reviewRecorded {
		t.Fatalf("restore side-effect hold review record missing: %v %v", reviewRecorded, err)
	}
}

// TestOffsiteEncryptedRestoreJourney is the end-to-end OPS-002 proof:
// real pipeline backup -> encrypt -> upload (local directory offsite) ->
// operator pull -> decrypt (including two failure injections) -> manifest
// v2 verification -> full database restore on a SECOND fresh database ->
// facts, ownership, holds, isolation and explicit release all verified.
func TestOffsiteEncryptedRestoreJourney(t *testing.T) {
	journeyRequireBinaries(t)
	if os.Getenv("URMAIL_TEST_DATABASE_URL") == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL is not set; skipping PostgreSQL restore journey")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
	defer cancel()

	source := testdb.New(t)
	journeyMigrate(t, source)
	journeySeed(t, source)

	offsite := t.TempDir()
	identityPath, recipient := journeyWriteIdentity(t, offsite)
	wrongIdentity, _ := journeyWriteIdentity(t, offsite)

	// 1. Real local backup of the seeded database (pg_dump, custom format).
	spool := t.TempDir()
	dumpPath := filepath.Join(spool, "journey.dump")
	dump := exec.CommandContext(ctx, journeyPGBin.dump, "--format=custom", "--file", dumpPath, "--dbname", source)
	if err := dump.Run(); err != nil {
		t.Fatalf("pg_dump: %v", err)
	}

	// 2. Real backupauto pipeline: encrypt to age, upload, verify, record run.
	platformPool, err := db.OpenPlatform(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer platformPool.Close()
	worker := &backupauto.Worker{
		Pool:       platformPool,
		Runner:     journeyRunner{dumpPath: dumpPath},
		Store:      dirOffsite{dir: offsite},
		Recipient:  recipient,
		OffsiteURI: "dir://" + offsite,
	}
	if err = worker.RunOnce(ctx); err != nil {
		t.Fatalf("backupauto pipeline: %v", err)
	}
	var runState string
	if err = platformPool.QueryRow(ctx, "SELECT state FROM backup_runs ORDER BY started_at DESC LIMIT 1").Scan(&runState); err != nil || runState != backupauto.StateSuccess {
		t.Fatalf("backup run not recorded as success: %q %v", runState, err)
	}
	offsiteStore := dirOffsite{dir: offsite}
	objects, err := offsiteStore.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manifestObject := ""
	cipherCount := 0
	for _, o := range objects {
		if strings.HasSuffix(o.Name, ".manifest.json") {
			manifestObject = o.Name
		} else if strings.HasSuffix(o.Name, ".age") {
			cipherCount++
		}
	}
	if manifestObject == "" || cipherCount != 1 {
		t.Fatalf("offsite set incomplete: manifest=%q ciphertexts=%d", manifestObject, cipherCount)
	}

	// 3. Operator pull: download the offsite objects into a staging dir.
	staging := t.TempDir()
	manifestPath := filepath.Join(staging, manifestObject)
	if err = offsiteStore.Download(ctx, manifestObject, mustCreate(t, manifestPath)); err != nil {
		t.Fatal(err)
	}
	manifest, err := backupauto.LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != backupauto.ManifestVersion2 {
		t.Fatalf("pipeline must produce a v2 manifest, got version %d", manifest.Version)
	}
	for _, artifact := range manifest.Artifacts {
		file, err := os.Create(filepath.Join(staging, artifact.EncryptedName))
		if err != nil {
			t.Fatal(err)
		}
		if err = offsiteStore.Download(ctx, artifact.EncryptedName, file); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		_ = file.Close()
	}
	plainName := manifest.Artifacts[0].Name

	// 4a. Failure injection: a WRONG identity must fail, leaving no output.
	wrongOut := filepath.Join(staging, "wrong-identity.dump")
	if err = decryptBackupArtifact(manifestPath, plainName, wrongOut, wrongIdentity); err == nil {
		t.Fatal("decrypt with a wrong age identity must fail")
	}
	if _, err = os.Stat(wrongOut); !os.IsNotExist(err) {
		t.Fatalf("failed decrypt left an output artifact behind: %v", err)
	}

	// 4b. Failure injection: tampered ciphertext fails its checksum check.
	cipherPath := filepath.Join(staging, manifest.Artifacts[0].EncryptedName)
	raw, err := os.ReadFile(cipherPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0x40
	if err = os.WriteFile(cipherPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tamperOut := filepath.Join(staging, "tampered.dump")
	if err = decryptBackupArtifact(manifestPath, plainName, tamperOut, identityPath); err == nil {
		t.Fatal("decrypt of tampered ciphertext must fail")
	}
	if _, err = os.Stat(tamperOut); !os.IsNotExist(err) {
		t.Fatalf("failed checksum decrypt left an output artifact behind: %v", err)
	}
	if err = offsiteStore.Download(ctx, manifest.Artifacts[0].EncryptedName, mustCreate(t, cipherPath)); err != nil {
		t.Fatal(err)
	}

	// 5. Correct operator decryption with the recovery identity. The output
	// keeps the manifest artifact name because restore-side verification
	// matches local files to manifest entries by basename (see the operator
	// manual: --out "$staging/$name").
	restoredDump := filepath.Join(staging, plainName)
	if err = decryptBackupArtifact(manifestPath, plainName, restoredDump, identityPath); err != nil {
		t.Fatalf("backup decrypt: %v", err)
	}
	sum, size, err := plainFileHash(restoredDump)
	if err != nil {
		t.Fatal(err)
	}
	if sum != manifest.Artifacts[0].SHA256 || size != manifest.Artifacts[0].Size {
		t.Fatal("decrypted artifact does not match the manifest plaintext record")
	}
	// The v2 manifest gap: restore-side verification must accept version 2.
	if err = verifyBackupManifest(manifestPath, []string{restoredDump}, false); err != nil {
		t.Fatalf("restore verification rejected a decrypted v2 backup set: %v", err)
	}

	// 6. SECOND fresh database as the restore target.
	target := testdb.New(t)
	journeyMigrate(t, target)
	targetConn := journeyConnect(t, target)
	if _, err = targetConn.Exec(ctx, "INSERT INTO brands(slug,name) VALUES('journey-decoy','Old target fixture')"); err != nil {
		t.Fatal(err)
	}

	// 7. Crash-window isolation marker: the target is fenced BEFORE any
	// bytes are restored, so a failure cannot expose half state.
	journeyBeginQuarantine(t, target)
	journeyAssertFencedPreRestore(t, target)

	// 8. Real restore pipeline on the target.
	journeyRestorePipeline(t, target, restoredDump, journeyRestoreID)
	journeyAssertFacts(t, target)

	// 9. Explicit release only after every step succeeded.
	if !journeyRelease(t, target) {
		t.Fatal("explicit release refused")
	}
	if _, err = targetConn.Exec(ctx, "INSERT INTO brands(slug,name) VALUES('journey-post-release','After release')"); err != nil {
		t.Fatalf("release must restore normal writability: %v", err)
	}
}

// journeyAssertFencedPreRestore proves the fence is live before any bytes
// are restored: new tenant creation is refused with the guard SQLSTATE.
func journeyAssertFencedPreRestore(t *testing.T, dsn string) {
	t.Helper()
	conn := journeyConnect(t, dsn)
	insert := "INSERT INTO tenants(brand_id,name,slug,status) SELECT id,'Fence Probe','fence-probe','active' FROM brands WHERE slug='journey-decoy'"
	if _, err := conn.Exec(context.Background(), insert); !journeyRefusalErr(err) {
		t.Fatalf("new tenant must be refused while restore quarantine is active, got %v", err)
	}
}

// offsiteEncryptArtifact encrypts one plaintext artifact through the
// production pipeline function (EncryptAndUpload) and returns the v2
// manifest describing plaintext and ciphertext integrity.
func offsiteEncryptArtifact(ctx context.Context, store backupauto.OffsiteStore, plainPath, recipient string) (*backupauto.Manifest, error) {
	f, err := os.Open(plainPath) // #nosec G304 -- test-owned plaintext artifact.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	entry, err := backupauto.EncryptAndUpload(ctx, store, backupauto.LocalArtifact{
		Name: filepath.Base(plainPath), Path: plainPath, Kind: backupauto.KindMailTar,
		Size: info.Size(), SHA256: hex.EncodeToString(h.Sum(nil)),
	}, recipient)
	if err != nil {
		return nil, err
	}
	entry.Mode = 0o600
	return &backupauto.Manifest{
		Version: backupauto.ManifestVersion2, Scope: backupauto.ScopeComplete, Consistent: true,
		CreatedAt: time.Now().UTC(), Artifacts: []backupauto.Artifact{entry},
		Encryption: &backupauto.EncryptionInfo{Algorithm: backupauto.EncryptionAlgorithmAge, Recipient: recipient},
	}, nil
}

func mustCreate(t *testing.T, path string) io.WriteCloser {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestRestoreFailureKeepsTargetFencedUntilExplicitRelease covers the three
// failure injections of OPS-002 on one fenced target: failed decrypt (wrong
// identity), failed checksum, and a crash between the isolation marker and
// the restore. None of them may call release: new tenants, domains and
// mailboxes stay refused until the explicit platform release runs.
func TestRestoreFailureKeepsTargetFencedUntilExplicitRelease(t *testing.T) {
	journeyRequireBinaries(t)
	if os.Getenv("URMAIL_TEST_DATABASE_URL") == "" {
		t.Skip("URMAIL_TEST_DATABASE_URL is not set; skipping PostgreSQL restore journey")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	target := testdb.New(t)
	journeyMigrate(t, target)

	// Unfenced seed so every guard trigger (tenant, domain, mailbox) can be
	// probed with a NEW row once the fence is active.
	conn := journeyConnect(t, target)
	if _, err := conn.Exec(ctx, "SET ROLE urmail_platform"); err != nil {
		t.Fatal(err)
	}
	seed := "DO $seed$ DECLARE b bigint;t bigint;d bigint; BEGIN\n" +
		" INSERT INTO brands(slug,name) VALUES('journey-source','Journey Source') RETURNING id INTO b;\n" +
		" INSERT INTO tenants(brand_id,name,slug,status) VALUES(b,'Journey Tenant','" + journeyTenantSlug + "','active') RETURNING id INTO t;\n" +
		" INSERT INTO domains(tenant_id,fqdn,status,verification_token,verified_at) VALUES(t,'" + journeyDomain + "','active','tok',now()) RETURNING id INTO d;\n" +
		" INSERT INTO mailboxes(tenant_id,domain_id,local_part,address,password_hash,status) VALUES(t,d,'alice','alice@" + journeyDomain + "','x','active');\n" +
		"END $seed$;"
	if _, err := conn.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}

	// A real encrypted backup set, staged for the restore.
	staging := t.TempDir()
	identityPath, recipient := journeyWriteIdentity(t, staging)
	wrongIdentity, _ := journeyWriteIdentity(t, staging)
	plain := filepath.Join(staging, "urmail-journey.dump")
	payload := bytes.Repeat([]byte("journey artifact\n"), 64)
	if err := os.WriteFile(plain, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	store := dirOffsite{dir: staging}
	manifest, err := offsiteEncryptArtifact(ctx, store, plain, recipient)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(staging, "urmail-journey.manifest.json")
	rawManifest, err := manifestJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, []byte(rawManifest), 0o600); err != nil {
		t.Fatal(err)
	}

	// Crash-between-steps: pre-create the isolation marker, then "crash":
	// the restore is never run after this point.
	journeyBeginQuarantine(t, target)

	// Injection 1: wrong identity.
	wrongOut := filepath.Join(staging, "wrong.dump")
	if err = decryptBackupArtifact(manifestPath, "urmail-journey.dump", wrongOut, wrongIdentity); err == nil {
		t.Fatal("wrong identity must fail")
	}
	if _, err = os.Stat(wrongOut); !os.IsNotExist(err) {
		t.Fatalf("failed decrypt must not leave an artifact: %v", err)
	}
	// Injection 2: failed ciphertext checksum.
	corrupt := filepath.Join(staging, manifest.Artifacts[0].EncryptedName)
	f, err := os.OpenFile(corrupt, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte{0x00}, 10); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	if err = decryptBackupArtifact(manifestPath, "urmail-journey.dump", filepath.Join(staging, "corrupt.dump"), identityPath); err == nil {
		t.Fatal("ciphertext checksum mismatch must fail the decrypt")
	}
	// Injection 3 (the crash itself): the isolated marker stays active with
	// no restore behind it. The target must be fenced on all three guards.
	journeyAssertFenced(t, target)

	// Only the explicit release may lift the fence -- afterwards writes work.
	if !journeyRelease(t, target) {
		t.Fatal("explicit release refused")
	}
	if _, err = conn.Exec(ctx, "INSERT INTO tenants(brand_id,name,slug,status) SELECT id,'After Release','after-release','active' FROM brands WHERE slug='journey-source'"); err != nil {
		t.Fatalf("post-release tenant creation must succeed: %v", err)
	}
}

// TestSingleMailboxRestorePathUnderManifestV2 proves the controlled
// single-mailbox path: one artifact of a v2 backup set is decrypted with
// the recovery identity ("backup decrypt --artifact"), verified, its
// archive is validated against the managed-roots allowlist, and only the
// chosen mailbox's Maildir subtree materializes.
func TestSingleMailboxRestorePathUnderManifestV2(t *testing.T) {
	staging := t.TempDir()
	identityPath, recipient := journeyWriteIdentity(t, staging)

	// Build a real mail archive with two mailboxes plus a config member.
	mailPath := filepath.Join(staging, "urmail-mail-journey.tar.gz")
	buildTarGz(t, mailPath, map[string]string{
		"var/mail/" + journeyDomain + "/alice/cur/msg1": "From alice\n",
		"var/mail/" + journeyDomain + "/alice/new/msg2": "From alice\n",
		"var/mail/" + journeyDomain + "/bob/cur/msg3":   "From bob\n",
		"etc/urmail/journey-marker":                     "config fact\n",
	})
	store := dirOffsite{dir: staging}
	manifest, err := offsiteEncryptArtifact(context.Background(), store, mailPath, recipient)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(staging, "urmail-mail-journey.manifest.json")
	rawManifest, err := manifestJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestPath, []byte(rawManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = backupauto.LoadManifest(manifestPath); err != nil {
		t.Fatalf("v2 manifest must decode: %v", err)
	}

	// Negative: a v1 (plaintext) manifest has nothing to decrypt.
	v1Manifest := filepath.Join(staging, "v1.manifest.json")
	v1, err := json.Marshal(map[string]any{"version": 1, "scope": backupauto.ScopeComplete, "consistent_maintenance": true, "artifacts": []map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(v1Manifest, v1, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = decryptBackupArtifact(v1Manifest, "urmail-mail-journey.tar.gz", filepath.Join(staging, "v1.out"), identityPath); err == nil {
		t.Fatal("v1 manifest must not be decryptable")
	}
	// Negative: an artifact absent from the manifest is refused.
	if err = decryptBackupArtifact(manifestPath, "urmail-other.tar.gz", filepath.Join(staging, "absent.out"), identityPath); err == nil {
		t.Fatal("absent artifact must be refused")
	}
	// Negative: wrong identity refused.
	wrongIdentity, _ := journeyWriteIdentity(t, staging)
	if err = decryptBackupArtifact(manifestPath, "urmail-mail-journey.tar.gz", filepath.Join(staging, "wrong.out"), wrongIdentity); err == nil {
		t.Fatal("wrong identity must be refused")
	}
	// Positive: single-artifact decrypt of the mail archive only.
	singleOut := filepath.Join(staging, "mail-restored.tar.gz")
	if err = decryptBackupArtifact(manifestPath, "urmail-mail-journey.tar.gz", singleOut, identityPath); err != nil {
		t.Fatalf("single-artifact decrypt: %v", err)
	}
	sum, size, err := plainFileHash(singleOut)
	if err != nil {
		t.Fatal(err)
	}
	if sum != manifest.Artifacts[0].SHA256 || size != manifest.Artifacts[0].Size {
		t.Fatal("single-artifact decrypt does not match the manifest record")
	}
	// The archive is validated before any extraction.
	if err = validateRestoreArchive(singleOut); err != nil {
		t.Fatalf("validated mail archive rejected: %v", err)
	}

	// Selective extraction: ONLY alice's Maildir materializes.
	root := t.TempDir()
	extractSingleMailbox(t, singleOut, "var/mail/"+journeyDomain+"/alice", root)
	for _, want := range []string{"cur/msg1", "new/msg2"} {
		if _, err = os.Stat(filepath.Join(root, "var/mail", journeyDomain, "alice", want)); err != nil {
			t.Fatalf("alice member %s missing after selective extraction: %v", want, err)
		}
	}
	if _, err = os.Stat(filepath.Join(root, "var/mail", journeyDomain, "bob")); !os.IsNotExist(err) {
		t.Fatalf("bob mailbox must not materialize in a single-mailbox restore: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "etc/urmail/journey-marker")); !os.IsNotExist(err) {
		t.Fatalf("config facts must not materialize in a single-mailbox restore: %v", err)
	}
}

func buildTarGz(t *testing.T, path string, members map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for _, name := range names {
		body := members[name]
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg}
		if err = tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// extractSingleMailbox mirrors the operator's selective tar extraction:
// only members under mailboxPrefix are materialized under root.
func extractSingleMailbox(t *testing.T, archive, mailboxPrefix, root string) {
	t.Helper()
	f, err := os.Open(archive) // #nosec G304 -- test-owned staged artifact.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		name := header.Name
		if name != mailboxPrefix && !strings.HasPrefix(name, mailboxPrefix+"/") {
			continue
		}
		target := filepath.Join(root, name)
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(out, reader); err != nil {
			_ = out.Close()
			t.Fatal(err)
		}
		_ = out.Close()
	}
}
