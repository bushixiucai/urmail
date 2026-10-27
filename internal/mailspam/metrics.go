// Observability for the spam verdict service.
//
// Counters make the closed loop measurable: verdicts (ham/spam/unsure),
// user feedback, milter session outcomes, and an availability gauge that is
// the in-process half of the spam:milter_unavailable operational alert (the
// telemetry kind registration lives in internal/telemetry).

package mailspam

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	verdictsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "urmail_spam_verdicts_total",
		Help: "Spam verdicts emitted by the milter, by classification.",
	}, []string{"verdict"})

	feedbackTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "urmail_spam_feedback_total",
		Help: "User spam feedback submissions, by reported verdict.",
	}, []string{"verdict"})

	sessionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "urmail_spam_milter_sessions_total",
		Help: "Milter session outcomes: ok (completed), error (protocol or IO failure), external (no local recipient).",
	}, []string{"result"})

	engineErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "urmail_spam_policy_lookup_errors_total",
		Help: "Verdicts computed without the per-mailbox policy snapshot because the policy lookup failed; mail is never blocked by such an error.",
	})

	milterAvailable = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "urmail_spam_milter_available",
		Help: "1 when the spam verdict milter is listening and accepting connections, 0 otherwise.",
	})
)

func recordVerdict(v Verdict, rejected bool) {
	verdictsTotal.WithLabelValues(string(v.Class)).Inc()
	if rejected {
		verdictsTotal.WithLabelValues("rejected").Inc()
	}
}

func recordSession(result string) {
	sessionsTotal.WithLabelValues(result).Inc()
}

// RecordPolicyLookupError counts a verdict that ran without its policy
// snapshot. Degraded verdicts stay conservative: lists and learned tokens
// are skipped, content and authentication evidence still apply.
func RecordPolicyLookupError() {
	engineErrorsTotal.Inc()
}

// RecordFeedback increments the user feedback counter. verdict is the raw
// feedback value ("spam"/"ham"); unknown values are recorded as "invalid"
// so abuse of the endpoint is visible without inventing label cardinality.
func RecordFeedback(verdict string) {
	switch verdict {
	case "spam", "ham":
		feedbackTotal.WithLabelValues(verdict).Inc()
	default:
		feedbackTotal.WithLabelValues("invalid").Inc()
	}
}

func setMilterAvailable(available bool) {
	if available {
		milterAvailable.Set(1)
	} else {
		milterAvailable.Set(0)
	}
}
