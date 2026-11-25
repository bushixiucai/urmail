package dovecotauth

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRows is the in-memory iterator the fake source returns.
type fakeRows struct {
	data [][]any
	pos  int
	err  error
}

func (r *fakeRows) Next() bool { return r.pos < len(r.data) }
func (r *fakeRows) Scan(dest ...any) error {
	row := r.data[r.pos]
	r.pos++
	if len(dest) != len(row) {
		return errors.New("scan arity mismatch")
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *int64:
			*d = row[i].(int64)
		case *string:
			*d = row[i].(string)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}
func (r *fakeRows) Err() error   { return r.err }
func (r *fakeRows) Close() error { return nil }

// fakeSource serves the three rebuild queries and the per-row compensation
// queries, and can be made to fail writes to exercise the dirty path.
type fakeSource struct {
	tenants   [][]any
	domains   [][]any
	mailboxes [][]any
	failAll   atomic.Bool
	mu        sync.Mutex
	failAddr  map[string]bool
}

func (s *fakeSource) Query(_ context.Context, query string, args ...any) (Rows, error) {
	if s.failAll.Load() {
		return nil, errors.New("source unavailable")
	}
	if len(args) == 1 {
		if address, ok := args[0].(string); ok {
			s.mu.Lock()
			failing := s.failAddr[address]
			s.mu.Unlock()
			if failing {
				return nil, errors.New("source row unavailable")
			}
		}
	}
	switch {
	case strings.HasPrefix(query, "SELECT count(*) FROM tenants"):
		return &fakeRows{data: [][]any{{int64(len(s.tenants))}}}, nil
	case strings.HasPrefix(query, "SELECT count(*) FROM domains"):
		return &fakeRows{data: [][]any{{int64(len(s.domains))}}}, nil
	case strings.HasPrefix(query, "SELECT count(*) FROM mailboxes"):
		return &fakeRows{data: [][]any{{int64(len(s.mailboxes))}}}, nil
	case strings.HasPrefix(query, "SELECT id, status FROM tenants"):
		return &fakeRows{data: s.tenants}, nil
	case strings.HasPrefix(query, "SELECT tenant_id, id, status, "):
		if len(args) == 1 {
			id := args[0].(int64)
			for _, row := range s.domains {
				if row[1].(int64) == id {
					return &fakeRows{data: [][]any{row}}, nil
				}
			}
			return &fakeRows{}, nil
		}
		return &fakeRows{data: s.domains}, nil
	case strings.HasPrefix(query, "SELECT m.address"):
		if len(args) == 1 {
			address := args[0].(string)
			for _, row := range s.mailboxes {
				if row[0].(string) == address {
					return &fakeRows{data: [][]any{row}}, nil
				}
			}
			return &fakeRows{}, nil
		}
		return &fakeRows{data: s.mailboxes}, nil
	}
	return nil, errors.New("unexpected query: " + query)
}

func fixtureSource() *fakeSource {
	return &fakeSource{
		tenants: [][]any{{int64(1), "active"}},
		domains: [][]any{{int64(1), int64(10), "active", int64(1)}},
		mailboxes: [][]any{
			{"alice@example.test", "$6$hash", int64(1), int64(10), "active", int64(1048576), "managed", `require "fileinto";`},
			{"carol@example.test", "$6$hash", int64(1), int64(10), "locked", int64(1048576), "managed", ``},
		},
	}
}

func newTestManager(t *testing.T, src Source) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dovecot-auth.db")
	m := NewManager(path, src)
	m.retryDelay = 10 * time.Millisecond
	m.maxRetry = 50 * time.Millisecond
	return m, path
}

// TestRebuildWritesSingleFileWithoutWAL pins the §5.4 landing discipline: the
// published snapshot is one file, DELETE journal, no sidecars, 0640.
func TestRebuildWritesSingleFileWithoutWAL(t *testing.T) {
	m, path := newTestManager(t, fixtureSource())
	snap, err := m.Rebuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Mailboxes != 2 || snap.Version != SchemaVersion {
		t.Fatalf("snapshot %+v", snap)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err = os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("sidecar %s survived rebuild: %v", suffix, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != FileMode {
		t.Fatalf("snapshot mode %o, want %o", info.Mode().Perm(), FileMode)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var mode string
	if err = db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(mode, "wal") {
		t.Fatalf("snapshot journal mode %q, want delete", mode)
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("user_version=%d err=%v", version, err)
	}
}

// TestApplyCompensatesCommittedChange proves the password change lands in the
// snapshot after the main commit.
func TestApplyCompensatesCommittedChange(t *testing.T) {
	src := fixtureSource()
	m, path := newTestManager(t, src)
	if _, err := m.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.mailboxes[0][1] = "$6$newhash"
	if err := m.Apply(context.Background(), Change{Address: "alice@example.test"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var hash string
	if err = db.QueryRow(`SELECT password_hash FROM mailboxes WHERE address=?`, "alice@example.test").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "$6$newhash" {
		t.Fatalf("snapshot hash %q", hash)
	}
}

// TestApplyFailureMarksDirtyAndRetries proves a failed compensation degrades the
// snapshot and is repaired by the retry loop once the source recovers.
func TestApplyFailureMarksDirtyAndRetries(t *testing.T) {
	src := fixtureSource()
	m, path := newTestManager(t, src)
	if _, err := m.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.failAll.Store(true)
	src.mailboxes[0][1] = "$6$after-failure"
	if err := m.Apply(context.Background(), Change{Address: "alice@example.test"}); err == nil {
		t.Fatal("snapshot failure was reported as success")
	}
	if !m.Dirty() {
		t.Fatal("failed compensation did not mark the snapshot dirty")
	}
	src.failAll.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !m.Dirty() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m.Dirty() {
		t.Fatal("retry loop never cleared the dirty flag")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var hash string
	if err = db.QueryRow(`SELECT password_hash FROM mailboxes WHERE address=?`, "alice@example.test").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "$6$after-failure" {
		t.Fatalf("retry did not converge: %q", hash)
	}
}

// TestReconcileDetectsDivergence proves the startup sampling fails closed.
func TestReconcileDetectsDivergence(t *testing.T) {
	src := fixtureSource()
	m, _ := newTestManager(t, src)
	if _, err := m.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background(), 2); err != nil {
		t.Fatalf("consistent snapshot rejected: %v", err)
	}
	src.mailboxes[0][1] = "$6$diverged"
	if err := m.Reconcile(context.Background(), 2); err == nil {
		t.Fatal("diverged snapshot passed reconciliation")
	}
	if !m.Dirty() {
		t.Fatal("divergence did not mark the snapshot dirty")
	}
}

// TestRequireFreshAfterRestoreRebuilds proves the restore gate republishes the
// snapshot instead of trusting a pre-restore generation.
func TestRequireFreshAfterRestoreRebuilds(t *testing.T) {
	src := fixtureSource()
	m, _ := newTestManager(t, src)
	if _, err := m.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.mailboxes[0][1] = "$6$restored"
	snap, err := RequireFreshAfterRestore(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Mailboxes != 2 {
		t.Fatalf("restore snapshot %+v", snap)
	}
	if _, err = RequireFreshAfterRestore(context.Background(), nil); err == nil {
		t.Fatal("nil manager was accepted")
	}
}

// TestPartialRetryKeepsDirty proves one successful item in a retry batch must
// not clear the dirty flag while another item is still owed (and the repair
// rebuild itself is failing too).
func TestPartialRetryKeepsDirty(t *testing.T) {
	src := fixtureSource()
	m, _ := newTestManager(t, src)
	m.retryDelay = 200 * time.Millisecond
	m.maxRetry = 400 * time.Millisecond
	if _, err := m.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	src.failAddr = map[string]bool{"alice@example.test": true}
	src.mu.Unlock()
	src.mailboxes[0][1] = "$6$alice-new"
	src.mailboxes[1][1] = "$6$carol-new"
	if err := m.Apply(context.Background(), Change{Address: "alice@example.test"}); err == nil {
		t.Fatal("failing row reported success")
	}
	if err := m.Apply(context.Background(), Change{Address: "carol@example.test"}); err != nil {
		t.Fatal(err)
	}
	// From here every retry and the repair rebuild fail, so nothing may clear
	// the flag yet.
	src.failAll.Store(true)
	time.Sleep(900 * time.Millisecond)
	if !m.Dirty() {
		t.Fatal("dirty flag cleared while a compensation is still owed")
	}
	src.failAll.Store(false)
	src.mu.Lock()
	src.failAddr = nil
	src.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && m.Dirty() {
		time.Sleep(50 * time.Millisecond)
	}
	if m.Dirty() {
		t.Fatal("dirty flag never cleared after the retry succeeded")
	}
}
