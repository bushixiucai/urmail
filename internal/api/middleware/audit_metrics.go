package middleware

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var auditWriteFailures = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_audit_write_failures_total", Help: "Audit persistence failures by fixed transaction or independent sink phase."}, []string{"phase"})

// RecordAuditFailure marks an observable gap without any tenant, user or URL
// label. Transaction=true denotes the business transaction's mandatory audit.
func RecordAuditFailure(transaction bool) {
	phase := "independent"
	if transaction {
		phase = "transaction"
	}
	auditWriteFailures.WithLabelValues(phase).Inc()
}
