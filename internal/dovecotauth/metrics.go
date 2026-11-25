package dovecotauth

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// activeManager is the snapshot manager whose state feeds the metrics. One
// daemon owns one auth snapshot, so a single pointer is the whole binding.
var activeManager atomic.Pointer[Manager]

// SetMetricsManager binds the manager whose Dirty/Staleness the gauges report.
func SetMetricsManager(m *Manager) { activeManager.Store(m) }

// MetricsManager returns the currently bound snapshot manager, if any.
func MetricsManager() *Manager { return activeManager.Load() }

// Built reports whether at least one snapshot build has completed.
func (m *Manager) Built() bool { return m != nil && m.lastOK.Load() != 0 }

var (
	dirtyGauge = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "urmail_dovecot_auth_snapshot_dirty",
		Help: "1 when the dovecot auth snapshot lags the main store (compensation pending or failed), else 0.",
	}, func() float64 {
		if m := activeManager.Load(); m != nil && m.Dirty() {
			return 1
		}
		return 0
	})
	stalenessGauge = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "urmail_dovecot_auth_snapshot_staleness_seconds",
		Help: "Age of the newest published dovecot auth snapshot; zero before the first build.",
	}, func() float64 {
		m := activeManager.Load()
		if m == nil {
			return 0
		}
		if last := m.lastOK.Load(); last == 0 {
			return 0
		}
		return m.Staleness().Seconds()
	})
)

func init() {
	prometheus.MustRegister(dirtyGauge, stalenessGauge)
}
