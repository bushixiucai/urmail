// Package loadbench provides a deterministic, reusable load harness for
// urMail scenario benchmarks and frozen dev-reference performance budgets.
//
// The budgets in this file were fixed BEFORE the first full measurement run
// (see docs/planning/upstream-alignment/CORE004_BUDGETS.md). Relaxing a
// budget afterwards to make a test pass is forbidden by CORE-004; a budget
// change requires a re-measurement on the documented reference environment.
package loadbench

import "time"

// ReferenceEnvironment documents the environment the budgets apply to.
// They are dev-reference budgets, NOT production SLAs.
const ReferenceEnvironment = "dev machine (macOS, Apple Silicon), docker compose PostgreSQL 16 on localhost:55432, " +
	"single urmail fixture database, warm caches (2s warmup discarded), " +
	"full mode: 8 VUs x 8s per scenario; smoke mode: 2 VUs x 2s"

// FullVUs and FullDuration are the fixed full-mode load profile all budget
// numbers below were frozen against.
const (
	FullVUs      = 8
	FullDuration = 8 * time.Second
)

// Budget holds the frozen numeric ceilings/floors for one scenario.
// Latencies are ceilings (milliseconds); Throughput is a floor (ops/sec);
// ErrorRateMax is a ceiling (fraction, 0..1).
type Budget struct {
	P50Ms         float64
	P95Ms         float64
	P99Ms         float64
	MinThroughput float64
	ErrorRateMax  float64
}

// Scenario names.
const (
	ScenarioMailboxListRead = "mailbox_list_read"
	ScenarioContactCRUD     = "contact_crud_search"
	ScenarioCalendarExpand  = "calendar_expansion"
	ScenarioMarketingStep   = "marketing_claim_step"
	ScenarioNotifyDrain     = "notification_outbox_drain"
)

// Budgets is the frozen dev-reference budget set (CORE-004). Values are
// deliberately conservative proposals fixed before measurement; the dev
// machine is fast, but the harness also runs under CI contention.
var Budgets = map[string]Budget{
	ScenarioMailboxListRead: {P50Ms: 50, P95Ms: 250, P99Ms: 500, MinThroughput: 100, ErrorRateMax: 0.001},
	ScenarioContactCRUD:     {P50Ms: 50, P95Ms: 250, P99Ms: 500, MinThroughput: 100, ErrorRateMax: 0.001},
	ScenarioCalendarExpand:  {P50Ms: 50, P95Ms: 250, P99Ms: 500, MinThroughput: 100, ErrorRateMax: 0.001},
	// Marketing claims serialize per tenant on an advisory lock and honor
	// SKIP LOCKED leases; the throughput floor is intentionally lower.
	ScenarioMarketingStep: {P50Ms: 150, P95Ms: 250, P99Ms: 500, MinThroughput: 15, ErrorRateMax: 0.001},
	ScenarioNotifyDrain:   {P50Ms: 50, P95Ms: 250, P99Ms: 500, MinThroughput: 50, ErrorRateMax: 0.001},
}

// MarketingVUs is the corrected reference concurrency for the standalone
// marketing step profile: the claim/authorize/finish path serializes per
// tenant by design, so more VUs measure lock queueing, not step health.
const MarketingVUs = 2

// IsolationBudget is the frozen ceiling for mailbox list/read p95 while a
// marketing claim burst runs concurrently. This is the resource-isolation
// proof budget: the marketing path must not push normal mail reads past it.
var IsolationBudget = Budget{P50Ms: 50, P95Ms: 300, P99Ms: 800, MinThroughput: 50, ErrorRateMax: 0.001}
