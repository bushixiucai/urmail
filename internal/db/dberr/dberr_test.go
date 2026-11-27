package dberr

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	_ "modernc.org/sqlite"
)

func TestClassifyPostgresCodes(t *testing.T) {
	cases := map[string]Class{
		"23505": Constraint,
		"23514": Constraint,
		"23502": Constraint,
		"23P01": Constraint,
		"23503": ForeignKey,
		"42501": Forbidden,
		"57014": Unavailable,
		"08006": Unavailable,
		"53300": Unavailable,
		"XX000": Unknown,
	}
	for code, want := range cases {
		got := Classify(&pgconn.PgError{Code: code})
		if got != want {
			t.Fatalf("code %s classified %v, want %v", code, got, want)
		}
	}
}

func TestClassifySentinelsAndContext(t *testing.T) {
	cases := []struct {
		err  error
		want Class
	}{
		{fmt.Errorf("wrap: %w", ErrConstraint), Constraint},
		{fmt.Errorf("wrap: %w", ErrForeignKey), ForeignKey},
		{fmt.Errorf("wrap: %w", ErrForbidden), Forbidden},
		{fmt.Errorf("wrap: %w", ErrUnavailable), Unavailable},
		{context.DeadlineExceeded, Unavailable},
		{fmt.Errorf("boom"), Unknown},
		{nil, Unknown},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Fatalf("%v classified %v, want %v", tc.err, got, tc.want)
		}
	}
}

// TestClassifySQLiteConstraint proves the SQLite driver error really lands in
// Constraint. The S7 freeze exists because SQLite never produces PgError.
func TestClassifySQLiteConstraint(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.ExecContext(t.Context(), "CREATE TABLE t(a INTEGER PRIMARY KEY, b TEXT UNIQUE)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), "INSERT INTO t(a,b) VALUES(1,'x')"); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), "INSERT INTO t(a,b) VALUES(2,'x')")
	if err == nil {
		t.Fatal("duplicate insert succeeded")
	}
	if got := Classify(err); got != Constraint {
		t.Fatalf("sqlite unique violation classified %v (%v), want Constraint", got, err)
	}
	if _, err = db.ExecContext(t.Context(), "INSERT INTO t(a,b) VALUES(1,'y')"); err == nil || Classify(err) != Constraint {
		t.Fatalf("sqlite primary key violation classified %v (%v)", Classify(err), err)
	}
}
