package loadbench

import (
	"github.com/bushixiucai/urmail/internal/timeutil"
	"time"
)

func pgtypeT(t time.Time) int64 {
	return timeutil.Micro(t)
}
