package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
	"github.com/bushixiucai/urmail/internal/store"
)

type onboardingStoreStub struct {
	gotTenant int64
	tasks     []store.OnboardingTasks
}

func (s *onboardingStoreStub) ListOnboardingTasksByTenant(_ context.Context, tenantID int64) ([]store.OnboardingTasks, error) {
	s.gotTenant = tenantID
	return s.tasks, nil
}

func TestOnboardingListUsesAuthenticatedTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &onboardingStoreStub{tasks: []store.OnboardingTasks{{ID: 7, TenantID: 41, Status: "pending"}}}
	router := gin.New()
	router.GET("/tasks", func(c *gin.Context) {
		c.Set(middleware.ContextTenantID, int64(41))
		(&OnboardingHandler{Store: stub}).List(c)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tasks", nil))
	if w.Code != http.StatusOK || stub.gotTenant != 41 {
		t.Fatalf("status=%d tenant=%d body=%s", w.Code, stub.gotTenant, w.Body.String())
	}
}

func TestOnboardingListRejectsMissingTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &onboardingStoreStub{}
	router := gin.New()
	router.GET("/tasks", (&OnboardingHandler{Store: stub}).List)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/tasks", nil))
	if w.Code != http.StatusUnauthorized || stub.gotTenant != 0 {
		t.Fatalf("status=%d tenant=%d body=%s", w.Code, stub.gotTenant, w.Body.String())
	}
}
