package middleware

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

func TestHTTPMetricsUseTemplatesAndObserveRecoveredPanic(t *testing.T) {
	router := gin.New()
	router.Use(HTTPMetrics(), Recover(zap.NewNop()))
	router.GET("/metrics-test/:tenant/messages/:id", func(c *gin.Context) { c.Status(204) })
	router.GET("/metrics-test-panic", func(*gin.Context) { panic("injected") })
	for _, path := range []string{"/metrics-test/private-customer@example.test/messages/secret-message-id", "/metrics-test-panic"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), "GET", path, nil))
	}
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	panicObserved := false
	templateObserved := false
	for _, family := range families {
		if family.GetName() != "urmail_http_requests_total" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
				if strings.Contains(label.GetValue(), "private-customer") || strings.Contains(label.GetValue(), "secret-message") {
					t.Fatal("customer identity leaked into metric labels")
				}
			}
			if labels["route"] == "/metrics-test/:tenant/messages/:id" && labels["status_class"] == "2xx" {
				templateObserved = true
			}
			if labels["route"] == "/metrics-test-panic" && labels["status_class"] == "5xx" {
				panicObserved = true
			}
		}
	}
	if !panicObserved || !templateObserved {
		t.Fatalf("missing metrics: panic=%v template=%v", panicObserved, templateObserved)
	}
}
