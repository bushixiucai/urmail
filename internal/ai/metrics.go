package ai

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Bounded-cardinality observability for AI usage accounting and recoverable
// failures. No tenant, mailbox, prompt, or endpoint labels are ever attached.
var (
	// RequestsTotal counts AI provider requests by bounded kind and outcome.
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "urmail_ai_requests_total",
		Help: "AI provider requests by kind and bounded outcome classification.",
	}, []string{"kind", "outcome"})

	// BudgetExceededTotal counts requests rejected before any provider call
	// because the tenant daily or monthly budget is exhausted.
	BudgetExceededTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "urmail_ai_budget_exceeded_total",
		Help: "AI requests rejected for exhausted tenant budgets before any provider call.",
	})

	// ProviderLatencySeconds observes provider call latency per provider name.
	ProviderLatencySeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "urmail_ai_provider_latency_seconds",
		Help:    "AI provider completion latency in seconds.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 12),
	}, []string{"provider"})

	// CacheHitsTotal counts assist results served from the operation
	// result cache (no provider call, no quota debit).
	CacheHitsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "urmail_ai_cache_hits_total",
		Help: "Assist results served from the operation result cache instead of a provider call.",
	})

	// PhishingPersistFailuresTotal counts phishing verdict persistences
	// that failed after a successful analysis. Such verdicts stay invisible
	// to the message API until the message is analyzed again.
	PhishingPersistFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "urmail_ai_phishing_persist_failures_total",
		Help: "Phishing verdict persistences that failed after a successful analysis.",
	})

	// BreakerTransitionsTotal counts circuit breaker state transitions.
	BreakerTransitionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "urmail_ai_breaker_transitions_total",
		Help: "Circuit breaker transitions per provider and bounded transition.",
	}, []string{"provider", "transition"})
)

// RecordOutcome increments the request counter with bounded labels.
func RecordOutcome(kind, outcome string) { RequestsTotal.WithLabelValues(kind, outcome).Inc() }
