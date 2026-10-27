package loadbench

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func pgtypeT(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
