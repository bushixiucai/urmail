package reconcile

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	renderTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "urmail_reconcile_renders_total",
		Help: "Configuration render attempts by result.",
	}, []string{"result"})
	renderDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "urmail_reconcile_render_duration_seconds",
		Help:    "Time spent rendering and applying configuration.",
		Buckets: prometheus.DefBuckets,
	})
)

func observeRender(start time.Time, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	renderTotal.WithLabelValues(result).Inc()
	renderDuration.Observe(time.Since(start).Seconds())
}
