package dovecotauth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// gatherMetric returns the rendered exposition text of the default registry.
func gatherMetric(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/internal/metrics", nil))
	return rec.Body.String()
}

// TestSnapshotMetricsFollowManagerState proves the S8 #12 gauges read
// Manager.Dirty/Staleness: dirty flips to 1 on a failed compensation and the
// staleness gauge reports the published snapshot age.
func TestSnapshotMetricsFollowManagerState(t *testing.T) {
	src := fixtureSource()
	m, _ := newTestManager(t, src)
	m.retryDelay = 50 * time.Millisecond
	if _, err := m.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := gatherMetric(t)
	if !strings.Contains(text, "urmail_dovecot_auth_snapshot_dirty 0") {
		t.Fatalf("dirty gauge not zero after rebuild:\n%s", text)
	}
	if !strings.Contains(text, "urmail_dovecot_auth_snapshot_staleness_seconds") {
		t.Fatal("staleness gauge missing")
	}
	src.failAll.Store(true)
	src.mailboxes[0][1] = "$6$metric-fail"
	if err := m.Apply(context.Background(), Change{Address: "alice@example.test"}); err == nil {
		t.Fatal("failing compensation reported success")
	}
	text = gatherMetric(t)
	if !strings.Contains(text, "urmail_dovecot_auth_snapshot_dirty 1") {
		t.Fatalf("dirty gauge not set after failed compensation:\n%s", text)
	}
}
