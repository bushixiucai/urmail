package marketing

import "github.com/prometheus/client_golang/prometheus"

var (
	attempts    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_marketing_attempts_total", Help: "Marketing SMTP attempts by bounded result classification."}, []string{"result"})
	recoveries  = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_marketing_recoveries_total", Help: "Expired marketing leases recovered without blind resubmission."}, []string{"state"})
	lastScan    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "urmail_marketing_last_scan_timestamp_seconds", Help: "Last successful marketing database scan; zero means no active scan."})
	revocations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_marketing_revocations_total", Help: "Marketing attempts stopped by execution-time re-verification."}, []string{"reason"})

	flowAttempts   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_marketing_flow_attempts_total", Help: "Marketing flow tick outcomes per claimed position."}, []string{"result"})
	flowRecoveries = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_marketing_flow_recoveries_total", Help: "Expired flow claims and stale uncertain positions recovered."}, []string{"state"})
	flowLastScan   = prometheus.NewGauge(prometheus.GaugeOpts{Name: "urmail_marketing_flow_last_scan_timestamp_seconds", Help: "Last successful marketing flow scan; zero means no active scan."})
)

func init() {
	prometheus.MustRegister(attempts, recoveries, lastScan, revocations, flowAttempts, flowRecoveries, flowLastScan)
}
