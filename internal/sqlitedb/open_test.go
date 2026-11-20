package sqlitedb_test

// Phase 1 S5 acceptance: the PG builtin UDFs the query layer depends on must
// exist on every pooled connection, and md5 must be byte-for-byte identical to
// PG's md5(text) (see PHASE1-S2-PG-BUILTIN-FUNCS-AUDIT-20261120.md).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/bushixiucai/urmail/internal/sqlitedb"
)

// md5 test vectors were produced independently:
//
//	psql -Atc "SELECT md5('test123'), md5(''), md5('测试')"
func TestMD5UDFMatchesPG(t *testing.T) {
	db, err := sqlitedb.Open(context.Background(), filepath.Join(t.TempDir(), "md5.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	cases := []struct {
		expr, want string
	}{
		{`md5('test123')`, "cc03e747a6afbbcbf8be7668acfebee5"},
		{`md5('')`, "d41d8cd98f00b204e9800998ecf8427e"},
		{`md5('测试')`, "db06c78d1e24cf708a14ce81c9b617ec"},
		{`md5('a"b\c' || char(10) || 'd')`, "06d844810877eeca25e3da36fa87a221"},
	}
	for _, tc := range cases {
		var got string
		if err := db.QueryRow("SELECT " + tc.expr).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("%s = %s, want %s", tc.expr, got, tc.want)
		}
	}

	// NULL in, NULL out — comparing a NULL password hash must never match a
	// digest of the empty string.
	var null sql.NullString
	if err := db.QueryRow("SELECT md5(NULL)").Scan(&null); err != nil {
		t.Fatalf("md5(NULL): %v", err)
	}
	if null.Valid {
		t.Errorf("md5(NULL) = %q, want NULL", null.String)
	}

	// PG has md5(text) but no md5(integer); refuse rather than hash "1".
	if _, err := db.Exec("SELECT md5(1)"); err == nil {
		t.Error("md5(integer) must fail like PG's undefined function")
	}
}
