package handlers

import (
	"math"
	"testing"
)

func TestAIBudgetExceededBoundaries(t *testing.T) {
	limit := AIBudget{DailyTokens: 1000, DailyCostMicros: 5000, MonthlyCostMicros: 5000}
	// Exactly at the limit is allowed; strictly crossing is refused.
	if limit.Exceeded(AIBudget{DailyTokens: 900}, AIBudget{DailyTokens: 100}) {
		t.Fatal("usage exactly reaching the budget must be allowed")
	}
	if !limit.Exceeded(AIBudget{DailyTokens: 901}, AIBudget{DailyTokens: 100}) {
		t.Fatal("crossing the daily token budget must be refused")
	}
	if !limit.Exceeded(AIBudget{MonthlyCostMicros: 0}, AIBudget{MonthlyCostMicros: 5001}) {
		t.Fatal("crossing the monthly cost budget must be refused")
	}
	// Zero limits mean unlimited: never exceeded.
	unlimited := AIBudget{}
	if unlimited.Exceeded(AIBudget{DailyTokens: 1 << 40}, AIBudget{DailyTokens: 1 << 40}) {
		t.Fatal("zero budget must mean unlimited")
	}
	// Cost and token limits are independent per window.
	if limit.Exceeded(AIBudget{DailyCostMicros: 4999}, AIBudget{DailyCostMicros: 1}) {
		t.Fatal("daily cost just under the limit must be allowed")
	}
	if !limit.Exceeded(AIBudget{DailyCostMicros: 5000}, AIBudget{DailyCostMicros: 1}) {
		t.Fatal("daily cost one micro over the limit must be refused")
	}
}

func TestAICostComputationRoundsUpMicros(t *testing.T) {
	// 1 token at 1,000,000 micros per million tokens = exactly 1 micro.
	cost, err := aiCost(1, 0, 1000000, 0)
	if err != nil || cost != 1 {
		t.Fatalf("cost=%d err=%v", cost, err)
	}
	// Fractional micros round up: conservative accounting. Two tokens at
	// 1 micro per million tokens cost 0.000002 micros, which rounds to 1.
	cost, err = aiCost(2, 0, 1, 1)
	if err != nil || cost != 1 {
		t.Fatalf("cost=%d err=%v", cost, err)
	}
	cost, err = aiCost(0, 0, 1000000, 1000000)
	if err != nil || cost != 0 {
		t.Fatalf("cost=%d err=%v", cost, err)
	}
	if _, err = aiCost(-1, 0, 1, 1); err == nil {
		t.Fatal("negative input must be refused")
	}
}

func TestAIBudgetAdditionCannotOverflowAndBypassLimit(t *testing.T) {
	for _, limit := range []AIBudget{{DailyTokens: math.MaxInt64}, {DailyCostMicros: math.MaxInt64}, {MonthlyTokens: math.MaxInt64}, {MonthlyCostMicros: math.MaxInt64}} {
		if !limit.Exceeded(AIBudget{DailyTokens: math.MaxInt64, DailyCostMicros: math.MaxInt64, MonthlyTokens: math.MaxInt64, MonthlyCostMicros: math.MaxInt64}, AIBudget{DailyTokens: 1, DailyCostMicros: 1, MonthlyTokens: 1, MonthlyCostMicros: 1}) {
			t.Fatal("overflow bypassed finite budget", limit)
		}
	}
}
