package backupauto

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return v
}

func TestPlanRetentionEmptySet(t *testing.T) {
	plan := PlanRetention(nil, RetentionPolicy{KeepDaily: 7}, mustTime(t, "2026-10-27T00:00:00Z"))
	if len(plan.Delete) != 0 {
		t.Fatalf("empty set must delete nothing, got %v", plan.Delete)
	}
	if len(plan.Refusals) == 0 {
		t.Fatal("empty set must carry a refusal")
	}
}

func TestPlanRetentionSingleChain(t *testing.T) {
	now := mustTime(t, "2026-10-27T12:00:00Z")
	candidates := []RetentionCandidate{
		{ID: "d1", CreatedAt: mustTime(t, "2026-10-20T12:00:00Z"), Verified: true},
		{ID: "d2", CreatedAt: mustTime(t, "2026-10-26T12:00:00Z"), Verified: true},
		{ID: "d3", CreatedAt: mustTime(t, "2026-10-27T09:00:00Z"), Verified: true},
	}
	plan := PlanRetention(candidates, RetentionPolicy{KeepDaily: 2}, now)
	keep := map[string]bool{}
	for _, id := range plan.Keep {
		keep[id] = true
	}
	if !keep["d3"] || !keep["d2"] {
		t.Fatalf("two newest daily buckets must be kept, keep=%v", plan.Keep)
	}
	if len(plan.Delete) != 1 || plan.Delete[0] != "d1" {
		t.Fatalf("expected only d1 deletable, got %v", plan.Delete)
	}
	if len(plan.Refusals) != 0 {
		t.Fatalf("fresh chain must not refuse: %v", plan.Refusals)
	}
}

func TestPlanRetentionCorruptedNewestKeepsVerifiedHead(t *testing.T) {
	now := mustTime(t, "2026-10-27T12:00:00Z")
	candidates := []RetentionCandidate{
		{ID: "verified-old", CreatedAt: mustTime(t, "2026-10-26T00:00:00Z"), Verified: true},
		{ID: "corrupt-new", CreatedAt: mustTime(t, "2026-10-27T09:00:00Z"), Verified: false},
	}
	plan := PlanRetention(candidates, RetentionPolicy{KeepDaily: 1}, now)
	keep := map[string]bool{}
	for _, id := range plan.Keep {
		keep[id] = true
	}
	if !keep["corrupt-new"] || !keep["verified-old"] {
		t.Fatalf("corrupted newest and verified head must both be kept, keep=%v delete=%v", plan.Keep, plan.Delete)
	}
	if len(plan.Delete) != 0 {
		t.Fatalf("nothing may be deleted while the newest copy is unverified: %v", plan.Delete)
	}
}

func TestPlanRetentionSafetyThresholdRefuses(t *testing.T) {
	now := mustTime(t, "2026-10-27T12:00:00Z")
	candidates := []RetentionCandidate{
		{ID: "stale", CreatedAt: mustTime(t, "2026-10-20T12:00:00Z"), Verified: true},
		{ID: "staler", CreatedAt: mustTime(t, "2026-10-10T12:00:00Z"), Verified: true},
	}
	plan := PlanRetention(candidates, RetentionPolicy{KeepDaily: 1}, now)
	if len(plan.Delete) != 0 {
		t.Fatalf("stale verified chain must refuse deletion, got %v", plan.Delete)
	}
	if len(plan.Refusals) == 0 {
		t.Fatal("stale verified chain must carry a refusal")
	}
	for _, id := range plan.Keep {
		if id != "stale" && id != "staler" {
			t.Fatalf("unexpected keep entry %q", id)
		}
	}
}

func TestPlanRetentionFreshWithinThreshold(t *testing.T) {
	now := mustTime(t, "2026-10-27T12:00:00Z")
	candidates := []RetentionCandidate{
		{ID: "fresh", CreatedAt: mustTime(t, "2026-10-27T06:00:00Z"), Verified: true},
		{ID: "old", CreatedAt: mustTime(t, "2026-10-01T06:00:00Z"), Verified: true},
	}
	plan := PlanRetention(candidates, RetentionPolicy{KeepDaily: 1}, now)
	if len(plan.Delete) != 1 || plan.Delete[0] != "old" {
		t.Fatalf("fresh verified chain should allow pruning, got delete=%v refusals=%v", plan.Delete, plan.Refusals)
	}
}

func TestPlanRetentionMonthlyBucketKeepsNewestMonth(t *testing.T) {
	now := mustTime(t, "2026-11-01T12:00:00Z")
	candidates := []RetentionCandidate{
		{ID: "nov", CreatedAt: mustTime(t, "2026-11-01T06:00:00Z"), Verified: true},
		{ID: "oct", CreatedAt: mustTime(t, "2026-10-03T06:00:00Z"), Verified: true},
		{ID: "sep", CreatedAt: mustTime(t, "2026-09-02T06:00:00Z"), Verified: true},
	}
	plan := PlanRetention(candidates, RetentionPolicy{KeepDaily: 1, KeepMonthly: 1}, now)
	keep := map[string]bool{}
	for _, id := range plan.Keep {
		keep[id] = true
	}
	if !keep["nov"] {
		t.Fatalf("newest monthly generation must be retained, keep=%v", plan.Keep)
	}
	if keep["oct"] || keep["sep"] {
		t.Fatalf("generations outside every bucket must be deletable, keep=%v delete=%v", plan.Keep, plan.Delete)
	}
}

func TestPlanRetentionWeeklyBucket(t *testing.T) {
	now := mustTime(t, "2026-10-28T12:00:00Z")
	candidates := []RetentionCandidate{
		{ID: "w1", CreatedAt: mustTime(t, "2026-10-27T06:00:00Z"), Verified: true},
		{ID: "w2", CreatedAt: mustTime(t, "2026-10-21T06:00:00Z"), Verified: true},
		{ID: "w3", CreatedAt: mustTime(t, "2026-10-14T06:00:00Z"), Verified: true},
	}
	plan := PlanRetention(candidates, RetentionPolicy{KeepWeekly: 2}, now)
	if len(plan.Delete) != 1 || plan.Delete[0] != "w3" {
		t.Fatalf("two weekly buckets kept, third deletable: keep=%v delete=%v", plan.Keep, plan.Delete)
	}
}

func TestPlanRetentionZeroPolicyStillKeepsNewestVerified(t *testing.T) {
	now := mustTime(t, "2026-10-27T12:00:00Z")
	candidates := []RetentionCandidate{
		{ID: "a", CreatedAt: mustTime(t, "2026-10-27T06:00:00Z"), Verified: true},
		{ID: "b", CreatedAt: mustTime(t, "2026-10-26T06:00:00Z"), Verified: true},
	}
	plan := PlanRetention(candidates, RetentionPolicy{}, now)
	if len(plan.Delete) != 1 || plan.Delete[0] != "b" {
		t.Fatalf("zero policy must still prune beyond the chain heads, got %v", plan.Delete)
	}
}

func TestPlanRetentionUnverifiedNeverCountsAsCoverage(t *testing.T) {
	now := mustTime(t, "2026-10-27T12:00:00Z")
	candidates := []RetentionCandidate{
		{ID: "v1", CreatedAt: mustTime(t, "2026-10-27T06:00:00Z"), Verified: true},
		{ID: "u1", CreatedAt: mustTime(t, "2026-10-26T06:00:00Z"), Verified: false},
		{ID: "v2", CreatedAt: mustTime(t, "2026-10-25T06:00:00Z"), Verified: true},
	}
	plan := PlanRetention(candidates, RetentionPolicy{KeepDaily: 2}, now)
	for _, id := range plan.Delete {
		if id == "v2" {
			t.Fatal("an unverified copy must not consume a retention bucket v2 belongs to")
		}
	}
	found := false
	for _, id := range plan.Keep {
		if id == "v2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("v2 is the second verified daily bucket and must be kept: keep=%v delete=%v", plan.Keep, plan.Delete)
	}
}
