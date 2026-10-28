package backupauto

import (
	"fmt"
	"sort"
	"time"
)

// RetentionCandidate is one offsite backup generation known to the planner.
type RetentionCandidate struct {
	// ID identifies the generation (manifest name or offsite prefix stamp).
	ID string
	// CreatedAt is the backup completion time (manifest created_at).
	CreatedAt time.Time
	// Verified reports that the offsite copy passed checksum verification.
	Verified bool
}

// RetentionPolicy follows the classic daily/weekly/monthly grandfather-
// father-son shape. Every field must be >= 0; zero disables a bucket.
type RetentionPolicy struct {
	KeepDaily   int
	KeepWeekly  int
	KeepMonthly int
	// SafetyThreshold is how fresh the newest VERIFIED backup must be before
	// any deletion is allowed. If the newest verified copy is older than
	// now-SafetyThreshold the planner refuses to delete anything: the chain
	// may be broken (uploads failing) and pruning during an outage can turn
	// a recoverable failure into data loss. A zero value defaults to 48h.
	SafetyThreshold time.Duration
}

// RetentionPlan is the outcome of the pure retention planner. Delete is only
// non-empty when the plan is safe to execute; Refusal explains every case in
// which the planner deliberately returns no deletions.
type RetentionPlan struct {
	Keep     []string
	Delete   []string
	Refusals []string
}

// isoWeekKey buckets a time by ISO year and week ("2026-W43").
func isoWeekKey(t time.Time) string {
	year, week := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// PlanRetention computes keep/delete sets.
//
// Invariants (E-04 erratum: protect the valid chain, never prune blindly):
//
//  1. The newest candidate is ALWAYS kept, verified or not, so a corrupted
//     newest backup is never silently removed before an operator looked at it.
//  2. If the newest candidate is not verified, the newest VERIFIED candidate
//     is also always kept: deletion decisions never remove the last restorable
//     chain head.
//  3. If the newest verified candidate is older than the safety threshold,
//     nothing is deleted and the plan carries a refusal.
//  4. Otherwise the newest KeepDaily per-day, KeepWeekly per-week (ISO week)
//     and KeepMonthly per-month verified candidates are kept; everything
//     older that no bucket claims is deleted.
func PlanRetention(candidates []RetentionCandidate, policy RetentionPolicy, now time.Time) RetentionPlan {
	threshold := policy.SafetyThreshold
	if threshold <= 0 {
		threshold = 48 * time.Hour
	}
	sorted := make([]RetentionCandidate, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.After(sorted[j].CreatedAt) })

	plan := RetentionPlan{}
	if len(sorted) == 0 {
		plan.Refusals = append(plan.Refusals, "no backups exist; nothing to retain or delete")
		return plan
	}

	for _, candidate := range sorted {
		if candidate.CreatedAt.IsZero() || candidate.CreatedAt.After(now) {
			plan.Refusals = append(plan.Refusals, "backup time is invalid or in the future; deletion disabled")
			for _, item := range sorted {
				plan.Keep = append(plan.Keep, item.ID)
			}
			return plan
		}
	}
	keep := map[string]bool{}
	// 1: newest candidate is untouchable.
	keep[sorted[0].ID] = true
	// 2: newest verified chain head is untouchable.
	newestVerified := -1
	for i, c := range sorted {
		if c.Verified {
			newestVerified = i
			break
		}
	}
	if newestVerified >= 0 {
		keep[sorted[newestVerified].ID] = true
		if now.Sub(sorted[newestVerified].CreatedAt) > threshold {
			plan.Refusals = append(plan.Refusals, fmt.Sprintf(
				"newest verified backup %s is %s old (threshold %s); deletion disabled until a fresh verified backup exists",
				sorted[newestVerified].ID, now.Sub(sorted[newestVerified].CreatedAt).Round(time.Minute), threshold))
		}
	} else {
		plan.Refusals = append(plan.Refusals, "no verified backup exists; deletion disabled")
	}
	if len(plan.Refusals) > 0 {
		for _, c := range sorted {
			plan.Keep = append(plan.Keep, c.ID)
		}
		return plan
	}

	// Buckets only claim VERIFIED candidates; unverified copies are never
	// counted as retention coverage, only tolerated as extra keeps.
	bucket := func(policyCount int, key func(time.Time) string) {
		if policyCount <= 0 {
			return
		}
		seen := map[string]bool{}
		count := 0
		for i := newestVerified; i < len(sorted) && count < policyCount; i++ {
			c := sorted[i]
			if !c.Verified || seen[key(c.CreatedAt)] {
				continue
			}
			seen[key(c.CreatedAt)] = true
			keep[c.ID] = true
			count++
		}
	}
	bucket(policy.KeepDaily, func(t time.Time) string { return t.UTC().Format("2006-01-02") })
	bucket(policy.KeepWeekly, isoWeekKey)
	bucket(policy.KeepMonthly, func(t time.Time) string { return t.UTC().Format("2006-01") })

	for _, c := range sorted {
		if keep[c.ID] {
			plan.Keep = append(plan.Keep, c.ID)
		} else {
			plan.Delete = append(plan.Delete, c.ID)
		}
	}
	return plan
}
