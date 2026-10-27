// Package marketingflow implements domain rules for marketing flows:
// normalization, waiting steps and advance decisions.
package marketingflow

import "time"

// WaitReady implements the corresponding domain rule.
func WaitReady(until, now time.Time) bool { return !now.Before(until) }

// NextAfterWait implements the corresponding domain rule.
func NextAfterWait(flow Flow, current string, until, now time.Time) (string, bool, error) {
	if !WaitReady(until, now) {
		return current, false, nil
	}
	next, err := Advance(flow, current, "none")
	return next, err == nil, err
}
