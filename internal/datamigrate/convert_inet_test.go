package datamigrate

import (
	"context"
	"database/sql"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestConvertInetFormsAndSQLiteRoundTrip(t *testing.T) {
	v4Prefix := netip.MustParsePrefix("127.0.0.1/32")
	v4Net := netip.MustParsePrefix("10.0.0.0/8")
	v6Prefix := netip.MustParsePrefix("2001:db8::1/128")
	v6Net := netip.MustParsePrefix("fe80::/64")
	var nilPtr *netip.Prefix
	invalidPrefix := netip.Prefix{}

	cases := []struct {
		name   string
		pgType string
		in     any
		want   any
	}{
		{name: "inet netip.Prefix ipv4", pgType: "inet", in: v4Prefix, want: "127.0.0.1/32"},
		{name: "inet *netip.Prefix ipv4 cidr", pgType: "inet", in: &v4Net, want: "10.0.0.0/8"},
		{name: "inet string ipv4", pgType: "inet", in: "127.0.0.1/32", want: "127.0.0.1/32"},
		{name: "inet netip.Prefix ipv6", pgType: "inet", in: v6Prefix, want: "2001:db8::1/128"},
		{name: "inet *netip.Prefix ipv6 cidr", pgType: "inet", in: &v6Net, want: "fe80::/64"},
		{name: "inet string ipv6", pgType: "inet", in: "2001:db8::/48", want: "2001:db8::/48"},
		{name: "cidr netip.Prefix ipv4", pgType: "cidr", in: v4Net, want: "10.0.0.0/8"},
		{name: "cidr *netip.Prefix ipv6", pgType: "cidr", in: &v6Net, want: "fe80::/64"},
		{name: "nil value", pgType: "inet", in: nil, want: nil},
		{name: "nil *netip.Prefix", pgType: "inet", in: nilPtr, want: nil},
		{name: "invalid netip.Prefix", pgType: "inet", in: invalidPrefix, want: nil},
	}

	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `CREATE TABLE inet_roundtrip (id INTEGER PRIMARY KEY, val TEXT)`); err != nil {
		t.Fatal(err)
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cm := ColumnMap{
				Table:      "audit_logs",
				Column:     "actor_ip",
				PGType:     tc.pgType,
				SQLiteType: "TEXT",
				Encoding:   EncodingPassthrough,
			}
			got, err := ConvertValue(cm, tc.in)
			if err != nil {
				t.Fatalf("ConvertValue(%T): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ConvertValue = %#v, want %#v", got, tc.want)
			}
			id := i + 1
			if _, err := db.ExecContext(ctx, `INSERT INTO inet_roundtrip(id, val) VALUES(?, ?)`, id, got); err != nil {
				t.Fatalf("sqlite insert %#v: %v", got, err)
			}
			var stored sql.NullString
			if err := db.QueryRowContext(ctx, `SELECT val FROM inet_roundtrip WHERE id = ?`, id).Scan(&stored); err != nil {
				t.Fatalf("sqlite select: %v", err)
			}
			if tc.want == nil {
				if stored.Valid {
					t.Fatalf("stored = %q, want NULL", stored.String)
				}
			} else if !stored.Valid || stored.String != tc.want {
				t.Fatalf("stored = %#v, want %q", stored, tc.want)
			}
		})
	}
}

func TestConvertAuditLogsAndSessionsTypeMapInet(t *testing.T) {
	tmPath, err := filepath.Abs(filepath.Join("..", "..", "migrations_sqlite", "type-map.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	tm, err := LoadTypeMap(tmPath)
	if err != nil {
		t.Fatalf("LoadTypeMap: %v", err)
	}
	for _, tc := range []struct {
		table  string
		column string
	}{
		{table: "audit_logs", column: "actor_ip"},
		{table: "sessions", column: "ip"},
	} {
		cm, ok := tm[tc.table][tc.column]
		if !ok {
			t.Fatalf("missing %s.%s in type-map.tsv", tc.table, tc.column)
		}
		if cm.PGType != "inet" || cm.Encoding != EncodingPassthrough {
			t.Fatalf("%s.%s unexpected map: %+v", tc.table, tc.column, cm)
		}
	}

	// Sample row 76 from Shared/phase3-stage1/urmail-prod-schema159.dump:
	// 76  1  6  127.0.0.1  POST /api/v1/mailboxes  http  /api/v1/mailboxes  {"phase": "transaction", "status": 201, "outcome": "succeeded"}  2026-10-28 14:08:13.868414+00
	createdAt := time.Date(2026, 10, 28, 14, 8, 13, 868414000, time.UTC)
	rawRow := map[string]any{
		"id":            int64(76),
		"tenant_id":     int64(1),
		"actor_user_id": int64(6),
		"actor_ip":      netip.MustParsePrefix("127.0.0.1/32"),
		"action":        "POST /api/v1/mailboxes",
		"target_type":   "http",
		"target_id":     "/api/v1/mailboxes",
		"detail":        `{"phase": "transaction", "status": 201, "outcome": "succeeded"}`,
		"created_at":    createdAt,
	}

	cols := []string{"id", "tenant_id", "actor_user_id", "actor_ip", "action", "target_type", "target_id", "detail", "created_at"}
	vals := make([]any, len(cols))
	for i, c := range cols {
		v, err := ConvertValue(tm["audit_logs"][c], rawRow[c])
		if err != nil {
			t.Fatalf("ConvertValue(audit_logs.%s): %v", c, err)
		}
		vals[i] = v
	}

	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `CREATE TABLE audit_logs (
		id INTEGER PRIMARY KEY, tenant_id INTEGER, actor_user_id INTEGER, actor_ip TEXT,
		action TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT, detail TEXT NOT NULL, created_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO audit_logs(id, tenant_id, actor_user_id, actor_ip, action, target_type, target_id, detail, created_at) VALUES(?,?,?,?,?,?,?,?,?)`, vals...); err != nil {
		t.Fatalf("insert audit_logs row 76: %v", err)
	}
	var gotIP string
	if err := db.QueryRowContext(ctx, `SELECT actor_ip FROM audit_logs WHERE id = 76`).Scan(&gotIP); err != nil {
		t.Fatal(err)
	}
	if gotIP != "127.0.0.1/32" {
		t.Fatalf("stored actor_ip = %q, want 127.0.0.1/32", gotIP)
	}
}
