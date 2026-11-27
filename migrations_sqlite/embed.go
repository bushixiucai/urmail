// Package migrations_sqlite embeds the SQLite schema chain and type map so
// product binaries can build a staging database without a source checkout.
package migrations_sqlite

import "embed"

// FS is the migrations chain (0001_init.sql ...) rooted at ".".
//
//go:embed *.sql
var FS embed.FS

// TypeMapTSV is the frozen PG→SQLite type map consumed by datamigrate.
//
//go:embed type-map.tsv
var TypeMapTSV []byte
