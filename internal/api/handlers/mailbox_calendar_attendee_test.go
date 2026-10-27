package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEventFromRejectsInvalidAttendeeBeforeWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/events", nil)
	_, ok := eventFrom(c, eventRequest{Summary: "A", StartsAt: "2026-03-01T09:00:00Z", EndsAt: "2026-03-01T10:00:00Z", Attendees: []string{"not-an-email"}})
	if ok || w.Code != 0 && !strings.Contains(w.Body.String(), "INVALID_PARAMS") {
		t.Fatal(ok, w.Code, w.Body.String())
	}
}
