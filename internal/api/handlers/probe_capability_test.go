package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/bushixiucai/urmail/internal/api/middleware"
)

func TestProbeCapabilitiesCannotExposePoolOrUnrestrictedTransactions(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[DomainVerificationRepository](), reflect.TypeFor[AIHealthRepository]()} {
		for i := 0; i < typ.NumMethod(); i++ {
			method := typ.Method(i)
			for j := 0; j < method.Type.NumOut(); j++ {
				out := method.Type.Out(j)
				if out.Kind() == reflect.Func || out.String() == "*db.Pool" || out.String() == "pgx.Tx" {
					t.Fatalf("%s exposes unrestricted capability %s", typ, method.Name)
				}
			}
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[DomainVerificationHandler](), reflect.TypeFor[AIHealthHandler]()} {
		if _, ok := typ.FieldByName("Pool"); ok {
			t.Fatalf("%s exposes pool", typ)
		}
		if _, ok := typ.FieldByName("Store"); ok {
			t.Fatalf("%s exposes generic store", typ)
		}
	}
}
func TestProbesRefuseWholeRequestTransactionMiddleware(t *testing.T) {
	repo := &healthProbeFake{version: 1, authorized: true}
	for _, h := range []gin.HandlerFunc{(&DomainHandler{Verification: domainStoreProbeFake{&shapeFakeDomainStore{}}}).Verify, (&AIConfigurationHandler{HealthRepository: repo}).Health} {
		r := probeRouter(func(c *gin.Context) { c.Set(middleware.ContextQueries, struct{}{}); h(c) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil))
		if w.Code != 503 {
			t.Fatalf("miswired probe status=%d", w.Code)
		}
	}
}
func TestProbeRepositoryRejectsMissingScope(t *testing.T) {
	if _, err := NewDomainVerificationRepository(nil).ReadVerification(context.Background(), ProbeActor{}, 1); !errors.Is(err, ErrProbeForbidden) {
		t.Fatalf("unscoped domain: %v", err)
	}
	if _, err := NewAIHealthRepository(nil).ReadHealth(context.Background(), ProbeActor{}); !errors.Is(err, ErrProbeForbidden) {
		t.Fatalf("unscoped AI: %v", err)
	}
}
