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

var applyTotal = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_reconcile_applies_total", Help: "Configuration apply attempts by result."}, []string{"result"})
var consecutiveFailures = promauto.NewGauge(prometheus.GaugeOpts{Name: "urmail_reconcile_consecutive_failures", Help: "Consecutive configuration failures."})
var applyReady = promauto.NewGauge(prometheus.GaugeOpts{Name: "urmail_reconcile_ready", Help: "Whether the last desired configuration applied successfully."})

func observeApply(err error, failures int64) {
	result := "ok"
	ready := float64(1)
	if err != nil {
		result = "error"
		ready = 0
	}
	applyTotal.WithLabelValues(result).Inc()
	consecutiveFailures.Set(float64(failures))
	applyReady.Set(ready)
}
