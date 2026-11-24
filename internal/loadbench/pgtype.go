package loadbench

import (
	"time"

	"github.com/bushixiucai/urmail/internal/timeutil"
)

func pgtypeT(t time.Time) int64 {
	return timeutil.Micro(t)
}
