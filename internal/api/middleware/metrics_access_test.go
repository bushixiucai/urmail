package middleware

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMetricsMachineCredentialDoesNotTrustForwardedAddress(t *testing.T) {
	token := strings.Repeat("a", 40)
	for _, test := range []struct {
		remote, credential string
		status             int
	}{{"127.0.0.1:1234", token, 200}, {"[::1]:1234", token, 200}, {"203.0.113.1:1234", token, 403}, {"127.0.0.1:1234", "", 403}, {"127.0.0.1:1234", "wrong", 403}, {"127.0.0.1:1234", "plain:" + token, 403}} {
		router := gin.New()
		router.GET("/internal/metrics", MetricsAccess(token), func(c *gin.Context) { c.Status(200) })
		req := httptest.NewRequestWithContext(context.Background(), "GET", "/internal/metrics", nil)
		req.RemoteAddr = test.remote
		if strings.HasPrefix(test.credential, "plain:") {
			req.Header.Set("Authorization", strings.TrimPrefix(test.credential, "plain:"))
		} else {
			req.Header.Set("Authorization", "Bearer "+test.credential)
		}
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != test.status {
			t.Fatalf("remote=%s status=%d", test.remote, response.Code)
		}
	}
}
