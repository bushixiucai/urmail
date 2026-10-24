package archive

import (
	"math"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var pendingCopies = promauto.NewGauge(prometheus.GaugeOpts{Name: "urmail_archive_spool_pending", Help: "Durable spool directories awaiting indexing; NaN before a scan."})
var copyResults = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_archive_copy_results_total", Help: "Archive copy indexing results without tenant or message labels."}, []string{"result"})

func init() { pendingCopies.Set(math.NaN()) }
