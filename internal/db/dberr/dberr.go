// Package dberr classifies database failures without depending on a driver.
//
// The S7 frozen rule: a constraint conflict is 409, a permission denial is
// 403, a genuinely unavailable database is 503, and anything unclassifiable is
// 500 with the raw driver error logged. Handlers use one mapping helper so a
// new driver or SQLite cannot silently turn every failure into "unavailable".
package dberr

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

// Class is the transport-neutral class of a database failure.
type Class int

const (
	// Unknown means the driver error has no defined class; log it raw.
	Unknown Class = iota
	// Constraint is a unique/check/exclusion violation.
	Constraint
	// ForeignKey is a missing referenced row (23503).
	ForeignKey
	// Forbidden is a permission denial.
	Forbidden
	// Unavailable is a connection/lock/IO failure.
	Unavailable
)

// Sentinels let a store or test normalize a driver error without importing it.
var (
	ErrConstraint  = errors.New("database constraint violation")
	ErrForeignKey  = errors.New("database foreign key violation")
	ErrForbidden   = errors.New("database permission denied")
	ErrUnavailable = errors.New("database unavailable")
)

// Classify maps a database error to its class. It never mutates err.
func Classify(err error) Class {
	if err == nil {
		return Unknown
	}
	switch {
	case errors.Is(err, ErrConstraint):
		return Constraint
	case errors.Is(err, ErrForeignKey):
		return ForeignKey
	case errors.Is(err, ErrForbidden):
		return Forbidden
	case errors.Is(err, ErrUnavailable):
		return Unavailable
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return classifyPostgres(pg.Code)
	}
	var lite *sqlite.Error
	if errors.As(err, &lite) {
		return classifySQLite(lite.Code())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Unavailable
	}
	return Unknown
}

func classifyPostgres(code string) Class {
	switch code {
	case "23503":
		return ForeignKey
	case "23505", "23514", "23502", "23P01":
		return Constraint
	case "42501":
		return Forbidden
	case "57014", "57P01", "57P02", "57P03", "53300", "53200":
		return Unavailable
	}
	if strings.HasPrefix(code, "08") {
		return Unavailable
	}
	return Unknown
}

func classifySQLite(code int) Class {
	switch code & 0xff { // extended codes carry the primary code in the low byte
	case 19: // SQLITE_CONSTRAINT
		return Constraint
	case 3, 8, 23: // SQLITE_PERM, SQLITE_READONLY, SQLITE_AUTH
		return Forbidden
	case 1, 5, 6, 10, 11, 13, 14: // INTERNAL, BUSY, LOCKED, IOERR, CORRUPT, FULL, CANTOPEN
		return Unavailable
	}
	return Unknown
}
