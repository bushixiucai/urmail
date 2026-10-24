package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	domainMail "github.com/bushixiucai/urmail/internal/domain/mail"
)

func TestUnsubscribeGetIsAnInertConfirmation(t *testing.T) {
	tokens, err := domainMail.NewSuppressionManager([]byte("unsubscribe-page-test-secret-32bytes"))
	if err != nil {
		t.Fatal(err)
	}
	h := &MarketingSuppressionHandler{Tokens: tokens}
	r := gin.New()
	r.GET("/unsubscribe", h.Confirmation)
	token := tokens.GenerateUnsubscribeToken(71, "person@example.test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/unsubscribe?token="+token, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `method="post"`) || !strings.Contains(w.Body.String(), `confirm_unsubscribe`) {
		t.Fatalf("invalid confirmation %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("unsubscribe token may leak through cache or referrer")
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/unsubscribe?token=%22%3E%3Cscript%3Ex%3C/script%3E", nil))
	if w.Code != 400 || strings.Contains(w.Body.String(), "<script>") || strings.Contains(w.Body.String(), "<form") {
		t.Fatalf("invalid token reflected or actionable: %d %s", w.Code, w.Body)
	}
}
