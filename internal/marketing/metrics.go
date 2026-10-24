package marketing

import "github.com/prometheus/client_golang/prometheus"

var (
	attempts   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_marketing_attempts_total", Help: "Marketing SMTP attempts by bounded result classification."}, []string{"result"})
	recoveries = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "urmail_marketing_recoveries_total", Help: "Expired marketing leases recovered without blind resubmission."}, []string{"state"})
	lastScan   = prometheus.NewGauge(prometheus.GaugeOpts{Name: "urmail_marketing_last_scan_timestamp_seconds", Help: "Last successful marketing database scan; zero means no active scan."})
)

func init() { prometheus.MustRegister(attempts, recoveries, lastScan) }
