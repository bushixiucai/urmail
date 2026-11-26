package dovecotauth_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bushixiucai/urmail/internal/dovecotauth"
	"github.com/bushixiucai/urmail/internal/mail/dovecot"
)

// copyRows is the minimal row iterator over a materialized result set.
type copyRows struct {
	data [][]any
	i    int
}

func (r *copyRows) Next() bool { r.i++; return r.i <= len(r.data) }
func (r *copyRows) Scan(dest ...any) error {
	for j, d := range dest {
		value := r.data[r.i-1][j]
		switch v := d.(type) {
		case *string:
			*v = value.(string)
		case *int64:
			*v = value.(int64)
		default:
			_ = v
		}
	}
	return nil
}
func (r *copyRows) Err() error   { return nil }
func (r *copyRows) Close() error { return nil }

// contractSource serves the three rebuild queries from materialized rows
// that follow the production source column order.
type contractSource struct {
	tenants   [][]any
	domains   [][]any
	mailboxes [][]any
}

func (s *contractSource) Query(_ context.Context, query string, _ ...any) (dovecotauth.Rows, error) {
	switch {
	case strings.HasPrefix(query, "SELECT count(*) FROM tenants"):
		return &copyRows{data: [][]any{{int64(len(s.tenants))}}}, nil
	case strings.HasPrefix(query, "SELECT count(*) FROM domains"):
		return &copyRows{data: [][]any{{int64(len(s.domains))}}}, nil
	case strings.HasPrefix(query, "SELECT count(*) FROM mailboxes"):
		return &copyRows{data: [][]any{{int64(len(s.mailboxes))}}}, nil
	case strings.HasPrefix(query, "SELECT id, status FROM tenants"):
		return &copyRows{data: s.tenants}, nil
	case strings.HasPrefix(query, "SELECT tenant_id, id, status, "):
		return &copyRows{data: s.domains}, nil
	case strings.HasPrefix(query, "SELECT m.address"):
		return &copyRows{data: s.mailboxes}, nil
	}
	return nil, errors.New("unexpected query: " + query)
}

// TestAuthSnapshotServesRenderedQueries is the cutover-blockers Blocker 1
// regression: the snapshot schema (dovecotauth.schemaSQL) and the dovecot
// SQL the renderer emits must share ONE column contract, proven by
// executing the rendered queries against a real snapshot build. The drift
// variant referenced migrations_sqlite columns (tenants.id,
// domains.verified_at, domains.provisioning_state) that the snapshot never
// had, so every IMAP authentication failed on load.
func TestAuthSnapshotServesRenderedQueries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dovecot-auth.db")
	src := &contractSource{
		tenants:   [][]any{{int64(1), "active"}},
		domains:   [][]any{{int64(1), int64(10), "active", int64(1)}},
		mailboxes: [][]any{{"alice@example.test", "$6$hash", int64(1), int64(10), "active", int64(1048576), "managed", `require "fileinto";`}},
	}
	ctx := context.Background()
	manager := dovecotauth.NewManager(path, src)
	if _, err := manager.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot not published: %v", err)
	}

	node := dovecot.NodeConfig{
		Hostname:      "box.example.test",
		PrimaryDomain: "example.test",
		StorageRoot:   "/srv/urmail/data/mail",
		TLSCertFile:   "/certs/mail.cert",
		TLSKeyFile:    "/certs/mail.key",
		AuthEngine:    "sqlite",
		AuthDBPath:    path,
	}
	files, err := dovecot.Generate(node)
	if err != nil {
		t.Fatal(err)
	}
	conf := files[dovecot.FileDBConf]

	// Single-source proof: the rendered config carries the exported
	// predicates verbatim, and none of the drifted migration-only columns.
	if !strings.Contains(conf, dovecotauth.ReadyJoin) {
		t.Fatalf("auth config does not carry the shared ReadyJoin predicate:\n%s", conf)
	}
	for _, banned := range []string{"t.id = m.tenant_id", "d.id = m.domain_id", "d.verified_at", "d.provisioning_state"} {
		if strings.Contains(conf, banned) {
			t.Fatalf("auth config references the drifted column %q:\n%s", banned, conf)
		}
	}

	password, user, iterate := renderedQueries(t, conf)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var addr, hash string
	if err = db.QueryRowContext(ctx, password).Scan(&addr, &hash); err != nil {
		t.Fatalf("password_query failed on the real snapshot: %v\n%s", err, password)
	}
	if addr != "alice@example.test" || hash != "$6$hash" {
		t.Fatalf("password_query got %q/%q", addr, hash)
	}

	var uAddr, uid, gid, home, quotaRule, acl, sieve string
	var sieveDir, before2, after, redirect sql.NullString
	if err = db.QueryRowContext(ctx, user).Scan(&uAddr, &uid, &gid, &home, &quotaRule, &acl, &sieve, &sieveDir, &before2, &after, &redirect); err != nil {
		t.Fatalf("user_query failed on the real snapshot: %v\n%s", err, user)
	}
	if uAddr != "alice@example.test" || quotaRule != "*:bytes=1048576" || acl != "" {
		t.Fatalf("user_query fields %q/%q/%q", uAddr, quotaRule, acl)
	}

	var listed string
	if err = db.QueryRowContext(ctx, iterate).Scan(&listed); err != nil {
		t.Fatalf("iterate_query failed on the real snapshot: %v", err)
	}
	if listed != "alice@example.test" {
		t.Fatalf("iterate_query listed %q", listed)
	}

	// Flip the domain to not-ready in the published copy: the shared
	// predicate must hide the mailbox (mail_ready semantics live in the
	// snapshot column, not a procedural function).
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err = raw.ExecContext(ctx, `UPDATE domains SET mail_ready = 0`); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, password).Scan(&addr, &hash); err == nil {
		t.Fatal("password_query accepted a mailbox on a not-ready domain")
	}
}

// renderedQueries extracts the three auth SQL statements from the rendered
// db.conf with dovecot's placeholder expansion applied.
func renderedQueries(t *testing.T, conf string) (password, user, iterate string) {
	t.Helper()
	for _, line := range strings.Split(conf, "\n") {
		name, value, found := strings.Cut(line, " = ")
		if !found {
			continue
		}
		sqlText := strings.TrimSuffix(strings.TrimSpace(value), ";")
		sqlText = strings.ReplaceAll(sqlText, "%u", "alice@example.test")
		sqlText = strings.ReplaceAll(sqlText, "%d", "example.test")
		sqlText = strings.ReplaceAll(sqlText, "%n", "alice")
		sqlText = strings.ReplaceAll(sqlText, "%s", "imap")
		switch name {
		case "password_query":
			password = sqlText
		case "user_query":
			user = sqlText
		case "iterate_query":
			iterate = sqlText
		}
	}
	if password == "" || user == "" || iterate == "" {
		t.Fatalf("rendered auth config missing queries:\n%s", conf)
	}
	return password, user, iterate
}
