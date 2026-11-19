// Package timeutil provides unix-microsecond helpers for the SQLite engine
// migration (Phase 1). SQLite stores timestamps as INTEGER (unix micro);
// these helpers convert between time.Time and int64 consistently.
package timeutil

import "time"

// Micro converts a time.Time to unix microseconds (0 for zero value).
func Micro(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// FromMicro converts unix microseconds to time.Time (zero for 0).
func FromMicro(m int64) time.Time {
	if m == 0 {
		return time.Time{}
	}
	return time.UnixMicro(m)
}

// NowMicro returns the current time as unix microseconds.
func NowMicro() int64 {
	return time.Now().UnixMicro()
}
