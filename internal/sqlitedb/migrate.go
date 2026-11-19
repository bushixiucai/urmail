package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Migrate applies the migrations_sqlite chain. The chain is a directory of
// sequentially numbered files (0001_init.sql, 0002_....sql); each file is
// exactly one user_version step. Semantics (v3.2 §3.3):
//   - forward-only: user_version == len(files) → no-op;
//   - gap or fork (user_version > len, or files missing in the middle) →
//     refuse to start;
//   - re-running the whole chain on an empty database is idempotent: the
//     files use IF NOT EXISTS and user_version is only ever advanced.
//
// schema_migrations (version, dirty) is written inside the same transaction
// for tooling parity; user_version remains the authority.
func Migrate(ctx context.Context, db *sql.DB, dir fs.FS, root string) error {
	names, err := fs.ReadDir(dir, root)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var files []string
	versions := map[int]string{}
	for _, e := range names {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		n, perr := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if perr != nil {
			return fmt.Errorf("migration %s: bad version prefix", e.Name())
		}
		files = append(files, path.Join(root, e.Name()))
		if _, dup := versions[n]; dup {
			return fmt.Errorf("migration version %d appears twice", n)
		}
		versions[n] = path.Join(root, e.Name())
	}
	sort.Strings(files)
	for i := 1; i <= len(files); i++ {
		if versions[i] == "" {
			return fmt.Errorf("migration chain gap at version %d: refusing to start", i)
		}
	}

	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > len(files) {
		return fmt.Errorf("database user_version %d is ahead of the embedded chain (%d): refusing to start", current, len(files))
	}
	if current == len(files) {
		return nil // idempotent no-op
	}

	for v := current + 1; v <= len(files); v++ {
		body, rerr := fs.ReadFile(dir, versions[v])
		if rerr != nil {
			return fmt.Errorf("read migration %d: %w", v, rerr)
		}
		if err := applyFile(ctx, db, v, string(body)); err != nil {
			return fmt.Errorf("apply migration %d: %w", v, err)
		}
	}
	return nil
}

func applyFile(ctx context.Context, db *sql.DB, version int, body string) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	tx, err := db.BeginTx(cctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range splitSQLiteStatements(body) {
		if _, err := tx.ExecContext(cctx, stmt); err != nil {
			return fmt.Errorf("exec %q…: %w", firstLine(stmt), err)
		}
	}
	// PRAGMA user_version cannot be parameterized; the version is a compile
	// time constant from the chain, never user input.
	if _, err := tx.ExecContext(cctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	// Tooling parity bookkeeping; user_version stays the authority.
	if _, err := tx.ExecContext(cctx,
		"INSERT INTO schema_migrations(version, dirty) VALUES(?, 0) ON CONFLICT(version) DO UPDATE SET dirty=0",
		version); err != nil {
		return fmt.Errorf("record schema_migrations: %w", err)
	}
	if _, err := tx.ExecContext(cctx, "DELETE FROM schema_migrations WHERE version <> ?", version); err != nil {
		return fmt.Errorf("trim schema_migrations: %w", err)
	}
	return tx.Commit()
}

// splitSQLiteStatements splits a .sql file into single statements. It is
// SQLite-aware: semicolons inside 'strings', -- line comments, /* block
// comments */ and BEGIN…END trigger bodies do not split. A trailing
// non-whitespace remainder without a terminator is returned as one statement
// (parse errors then surface with the right context, never silently).
func splitSQLiteStatements(body string) []string {
	var (
		out     []string
		cur     strings.Builder
		inStr   bool
		inLine  bool
		inBlock bool
		endNest int // BEGIN…END depth: statements inside trigger bodies do not split.
	)
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inLine {
			if c == '\n' {
				inLine = false
			}
			cur.WriteByte(c)
			continue
		}
		if inBlock {
			if c == '*' && i+1 < len(body) && body[i+1] == '/' {
				inBlock = false
				cur.WriteString("*/")
				i++
				continue
			}
			cur.WriteByte(c)
			continue
		}
		if inStr {
			if c == '\'' {
				if i+1 < len(body) && body[i+1] == '\'' {
					cur.WriteString("''")
					i++
					continue
				}
				inStr = false
			}
			cur.WriteByte(c)
			continue
		}
		switch {
		case c == '-' && i+1 < len(body) && body[i+1] == '-':
			inLine = true
			cur.WriteString("--")
		case c == '/' && i+1 < len(body) && body[i+1] == '*':
			inBlock = true
			cur.WriteString("/*")
		case c == '\'':
			inStr = true
			cur.WriteByte(c)
		case c == ';':
			if endNest > 0 {
				cur.WriteByte(c)
				continue
			}
			cur.WriteByte(c)
			flush()
		default:
			cur.WriteByte(c)
		}
		// BEGIN…END tracking outside strings/comments, at word boundaries.
		// CASE…END nests the same way: an expression END must not be taken
		// for a trigger-body END, so both BEGIN and CASE open a level.
		if !inStr && !inLine && !inBlock {
			if hasWordAt(body, i, "BEGIN") || hasWordAt(body, i, "CASE") {
				endNest++
			} else if endNest > 0 && hasWordAt(body, i, "END") {
				endNest--
			}
		}
	}
	flush()
	return out
}

func hasWordAt(s string, i int, word string) bool {
	if i+len(word) > len(s) {
		return false
	}
	if !strings.EqualFold(s[i:i+len(word)], word) {
		return false
	}
	if i > 0 {
		prev := s[i-1]
		if prev == '_' || prev == '"' || (prev >= 'A' && prev <= 'Z') || (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
			return false
		}
	}
	if i+len(word) < len(s) {
		next := s[i+len(word)]
		if next == '_' || (next >= 'A' && next <= 'Z') || (next >= 'a' && next <= 'z') || (next >= '0' && next <= '9') {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
