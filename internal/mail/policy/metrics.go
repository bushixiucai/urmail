package policy

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var decisionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "urmail_policy_decisions_total",
	Help: "Policy server decisions by action.",
}, []string{"action"})
