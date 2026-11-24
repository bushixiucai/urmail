package datamigrate

import (
	"path/filepath"
	"testing"
)

// TestBackupRunsOffsiteStateFidelity proves the 0160 source column survives
// the S5 import: the type map now carries backup_runs.offsite_state, so a
// pending_local_only value must not be silently collapsed to an empty string.
func TestBackupRunsOffsiteStateFidelity(t *testing.T) {
	ctx, pool, _ := s5Fixture(t)
	rawTarget, _ := s5Target(t)
	defer rawTarget.Close()
	tm, err := LoadTypeMap(absTestPath(t, filepath.Join("..", "..", "migrations_sqlite", "type-map.tsv")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO backup_runs(run_uid,state,scope,offsite_state,offsite_uri) VALUES
		('s5-offsite-01','success','local-mail-and-metadata','pending_local_only',''),
		('s5-offsite-02','success','local-mail-and-metadata','uploaded','')`); err != nil {
		t.Fatal(err)
	}
	src := pgSource{pool: pool}
	tgt := &Target{DB: rawTarget}
	if _, err = Migrate(ctx, src, tgt, tm, Config{SkipTables: []string{"schema_migrations"}, BatchSize: 2}); err != nil {
		t.Fatal(err)
	}
	for uid, want := range map[string]string{
		"s5-offsite-01": "pending_local_only",
		// A pre-0160 historical row may carry uploaded with no offsite_uri;
		// the import copies it verbatim (the CHECK constrains the state value
		// only). The fresh-backup gate, not the import, refuses uploaded
		// without an offsite_uri.
		"s5-offsite-02": "uploaded",
	} {
		var got, uri string
		if err = rawTarget.QueryRowContext(t.Context(), "SELECT offsite_state,offsite_uri FROM backup_runs WHERE run_uid=?", uid).Scan(&got, &uri); err != nil {
			t.Fatalf("%s: %v", uid, err)
		}
		if got != want {
			t.Fatalf("%s offsite_state=%q want %q (silently collapsed?)", uid, got, want)
		}
		if uri != "" {
			t.Fatalf("%s offsite_uri=%q want empty", uid, uri)
		}
	}
	report, err := VerifyRowHashes(ctx, src, tgt, tm, Config{SkipTables: []string{"schema_migrations"}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.RowHashZeroDiff {
		t.Fatalf("row hash diff: %+v", report)
	}
}
