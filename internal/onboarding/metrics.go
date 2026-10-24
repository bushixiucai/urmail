package onboarding

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var attempts = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_domain_work_attempts_total", Help: "DNS and onboarding attempts by fixed workflow, step and outcome; no tenant names or secrets."}, []string{"workflow", "step", "result"})
var duration = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "urmail_domain_work_duration_seconds", Help: "Fenced domain work duration by fixed workflow and step.", Buckets: prometheus.DefBuckets}, []string{"workflow", "step"})

func observeWork(workflow, step string, started time.Time, err error) {
	result := "completed"
	if err != nil {
		result = "requires_review"
	}
	attempts.WithLabelValues(workflow, step, result).Inc()
	duration.WithLabelValues(workflow, step).Observe(time.Since(started).Seconds())
}
