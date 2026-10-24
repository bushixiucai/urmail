package agent

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/bushixiucai/urmail/internal/agentd"
)

var agentCalls = promauto.NewCounterVec(prometheus.CounterOpts{Name: "urmail_agent_requests_total", Help: "Typed agent requests by fixed operation and result."}, []string{"operation", "result"})
var agentDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "urmail_agent_request_duration_seconds", Help: "Typed root-agent operation duration.", Buckets: prometheus.DefBuckets}, []string{"operation"})

// Do performs one typed call and records only bounded operation labels.
func (c *Client) Do(ctx context.Context, op string, params any) (*agentd.Response, error) {
	label := op
	switch op {
	case agentd.OpHostObserve, agentd.OpMailLogRead, agentd.OpPostfixQueue, agentd.OpConfigApply, agentd.OpConfigStatus, agentd.OpCertificateStatus, agentd.OpCertificateIssue, agentd.OpCertificateRenew, agentd.OpMailboxExpunge, agentd.OpSieveActivate:
	default:
		label = "unknown"
	}
	started := time.Now()
	response, err := c.do(ctx, op, params)
	result := "ok"
	if err != nil {
		result = "error"
	}
	agentCalls.WithLabelValues(label, result).Inc()
	agentDuration.WithLabelValues(label).Observe(time.Since(started).Seconds())
	return response, err
}
