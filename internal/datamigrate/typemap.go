// Package datamigrate implements the Phase-1 S5 data migrator: it copies the
// PostgreSQL business tables into the SQLite single-engine schema and proves
// the copy with the five verification gates from
// PHASE1-ISOLATION-DESIGN-20261119-V3.2 §4 (normalized row hashes, schema
// diff, index comparison, golden queries, negative isolation comparison) plus
// the §4 additions (sqlite_sequence reset, numeric lexicographic audit,
// NOCASE unique-fold precheck, ILIKE/LIKE mapping inventory).
//
// The conversion contract is migrations_sqlite/type-map.tsv: every business
// table column with its PG type, SQLite type and encoding. The migrator never
// invents a conversion: columns are matched by (table, column) and values are
// encoded per the mapping (timestamptz → unix micros INTEGER, jsonb → JSON
// text, text[] → JSON array text, bytea → BLOB, boolean → 0/1, passthrough
// otherwise). Hashes are computed over the converted form on both sides, so
// the zero-diff gate proves the copy including the conversion.
package datamigrate

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// ColumnEncoding mirrors the `encoding` column of type-map.tsv.
type ColumnEncoding string

const (
	EncodingPassthrough ColumnEncoding = "-"
	EncodingUnixMicro   ColumnEncoding = "unix micro"
	EncodingJSONText    ColumnEncoding = "JSON text"
	EncodingJSONArray   ColumnEncoding = "JSON array text"
)

// ColumnMap is one row of the type map.
type ColumnMap struct {
	Table      string
	Column     string
	PGType     string
	SQLiteType string
	Encoding   ColumnEncoding
}

// TypeMap indexes the parsed type map by table and column.
type TypeMap map[string]map[string]ColumnMap

// Tables returns the sorted table names covered by the type map.
func (m TypeMap) Tables() []string {
	tables := make([]string, 0, len(m))
	for t := range m {
		tables = append(tables, t)
	}
	sortStrings(tables)
	return tables
}

// Columns returns the sorted column names of one table.
func (m TypeMap) Columns(table string) []string {
	cols := make([]string, 0, len(m[table]))
	for c := range m[table] {
		cols = append(cols, c)
	}
	sortStrings(cols)
	return cols
}

// LoadTypeMap parses a type-map.tsv (table, column, pg_type, sqlite_type,
// encoding; TSV with a header row).
func LoadTypeMap(path string) (TypeMap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open type map: %w", err)
	}
	defer f.Close()
	return ParseTypeMap(f, path)
}

// ParseTypeMap parses a type-map.tsv stream (name is used in errors). It lets
// the embedded product copy and filesystem copies share one parser.
func ParseTypeMap(r io.Reader, name string) (TypeMap, error) {
	m := TypeMap{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if first {
			first = false
			if len(parts) < 5 || parts[0] != "table" {
				return nil, fmt.Errorf("type map %s: missing header row", name)
			}
			continue
		}
		if len(parts) < 5 {
			return nil, fmt.Errorf("type map %s line %d: want 5 fields, got %d", name, lineNo, len(parts))
		}
		table, column := parts[0], parts[1]
		if m[table] == nil {
			m[table] = map[string]ColumnMap{}
		}
		// Encodings carry optional design annotations after the canonical
		// token ("JSON array text (v3.2: …)") — match by prefix.
		raw := strings.TrimSpace(parts[4])
		enc := EncodingPassthrough
		switch {
		case strings.HasPrefix(raw, string(EncodingJSONArray)):
			enc = EncodingJSONArray
		case strings.HasPrefix(raw, string(EncodingJSONText)):
			enc = EncodingJSONText
		case strings.HasPrefix(raw, string(EncodingUnixMicro)):
			enc = EncodingUnixMicro
		}
		m[table][column] = ColumnMap{
			Table:      table,
			Column:     column,
			PGType:     parts[2],
			SQLiteType: parts[3],
			Encoding:   enc,
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan type map: %w", err)
	}
	if first {
		return nil, fmt.Errorf("type map %s: empty", name)
	}
	return m, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
